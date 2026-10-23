package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/example/payrelay/internal/core/domain"
	"github.com/example/payrelay/internal/core/ports"
	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

var _ ports.Repository = (*Store)(nil)

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS endpoints (
 id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL DEFAULT 'legacy', url TEXT NOT NULL, secret TEXT NOT NULL, active INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS events (
 id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL DEFAULT 'legacy', type TEXT NOT NULL, merchant_reference TEXT NOT NULL, amount_cents INTEGER NOT NULL,
 currency TEXT NOT NULL, status TEXT NOT NULL, idempotency_key TEXT UNIQUE, request_fingerprint TEXT, occurred_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS deliveries (
 id TEXT PRIMARY KEY, event_id TEXT NOT NULL REFERENCES events(id), endpoint_id TEXT NOT NULL REFERENCES endpoints(id),
 status TEXT NOT NULL, attempt_count INTEGER NOT NULL DEFAULT 0, next_attempt_at INTEGER NOT NULL, last_error TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, UNIQUE(event_id, endpoint_id)
);
CREATE TABLE IF NOT EXISTS delivery_attempts (
 id TEXT PRIMARY KEY, delivery_id TEXT NOT NULL REFERENCES deliveries(id), number INTEGER NOT NULL, status_code INTEGER,
 error TEXT NOT NULL DEFAULT '', duration_ms INTEGER NOT NULL, created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS deliveries_due_idx ON deliveries(status, next_attempt_at);
CREATE INDEX IF NOT EXISTS attempts_delivery_idx ON delivery_attempts(delivery_id, number);
`)
	if err != nil {
		return err
	}
	if err := addColumnIfMissing(ctx, s.db, "endpoints", "tenant_id", "TEXT NOT NULL DEFAULT 'legacy'"); err != nil {
		return err
	}
	if err := addColumnIfMissing(ctx, s.db, "endpoints", "updated_at", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := addColumnIfMissing(ctx, s.db, "events", "tenant_id", "TEXT NOT NULL DEFAULT 'legacy'"); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE endpoints SET updated_at=created_at WHERE updated_at=0; CREATE INDEX IF NOT EXISTS endpoints_tenant_active_idx ON endpoints(tenant_id, active); CREATE INDEX IF NOT EXISTS events_tenant_idx ON events(tenant_id);`)
	return err
}

func addColumnIfMissing(ctx context.Context, db *sql.DB, table, column, definition string) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+definition)
	return err
}

func (s *Store) CreateEndpoint(ctx context.Context, endpoint domain.Endpoint) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO endpoints(id,tenant_id,url,secret,active,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, endpoint.ID, endpoint.TenantID, endpoint.URL, endpoint.Secret, boolInt(endpoint.Active), endpoint.CreatedAt.Unix(), endpoint.UpdatedAt.Unix())
	return err
}

func (s *Store) ListEndpoints(ctx context.Context, tenantID string) ([]domain.Endpoint, error) {
	return s.listEndpoints(ctx, tenantID, false)
}

func (s *Store) ListActiveEndpoints(ctx context.Context, tenantID string) ([]domain.Endpoint, error) {
	return s.listEndpoints(ctx, tenantID, true)
}

func (s *Store) listEndpoints(ctx context.Context, tenantID string, activeOnly bool) ([]domain.Endpoint, error) {
	query := `SELECT tenant_id,id,url,secret,active,created_at,updated_at FROM endpoints WHERE tenant_id=?`
	if activeOnly {
		query += ` AND active=1`
	}
	query += ` ORDER BY created_at`
	rows, err := s.db.QueryContext(ctx, query, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var endpoints []domain.Endpoint
	for rows.Next() {
		var e domain.Endpoint
		var created, updated int64
		var active int
		if err := rows.Scan(&e.TenantID, &e.ID, &e.URL, &e.Secret, &active, &created, &updated); err != nil {
			return nil, err
		}
		e.Active = active == 1
		e.CreatedAt = time.Unix(created, 0).UTC()
		e.UpdatedAt = time.Unix(updated, 0).UTC()
		endpoints = append(endpoints, e)
	}
	return endpoints, rows.Err()
}
func (s *Store) GetEndpoint(ctx context.Context, tenantID, id string) (domain.Endpoint, error) {
	var e domain.Endpoint
	var created int64
	var active int
	var updated int64
	err := s.db.QueryRowContext(ctx, `SELECT tenant_id,id,url,secret,active,created_at,updated_at FROM endpoints WHERE tenant_id=? AND id=?`, tenantID, id).Scan(&e.TenantID, &e.ID, &e.URL, &e.Secret, &active, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ports.ErrNotFound
	}
	if err != nil {
		return e, err
	}
	e.Active = active == 1
	e.CreatedAt = time.Unix(created, 0).UTC()
	e.UpdatedAt = time.Unix(updated, 0).UTC()
	return e, nil
}

func (s *Store) SetEndpointActive(ctx context.Context, tenantID, id string, active bool) (domain.Endpoint, error) {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE endpoints SET active=?,updated_at=? WHERE tenant_id=? AND id=?`, boolInt(active), now.Unix(), tenantID, id)
	if err != nil {
		return domain.Endpoint{}, err
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return domain.Endpoint{}, ports.ErrNotFound
	}
	return s.GetEndpoint(ctx, tenantID, id)
}

func (s *Store) RotateEndpointSecret(ctx context.Context, tenantID, id, secret string) (domain.Endpoint, error) {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE endpoints SET secret=?,updated_at=? WHERE tenant_id=? AND id=?`, secret, now.Unix(), tenantID, id)
	if err != nil {
		return domain.Endpoint{}, err
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return domain.Endpoint{}, ports.ErrNotFound
	}
	return s.GetEndpoint(ctx, tenantID, id)
}

func (s *Store) CreateEvent(ctx context.Context, event domain.PaymentEvent, fingerprint string, endpoints []domain.Endpoint) (domain.PaymentEvent, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.PaymentEvent{}, false, err
	}
	defer tx.Rollback()
	if event.IdempotencyKey != "" {
		var id, existingFingerprint string
		err = tx.QueryRowContext(ctx, `SELECT id,request_fingerprint FROM events WHERE tenant_id=? AND idempotency_key=?`, event.TenantID, event.IdempotencyKey).Scan(&id, &existingFingerprint)
		if err == nil {
			if existingFingerprint != fingerprint {
				return domain.PaymentEvent{}, false, ports.ErrIdempotencyConflict
			}
			existing, getErr := getEvent(ctx, tx, id)
			return existing, true, getErr
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return domain.PaymentEvent{}, false, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO events(id,tenant_id,type,merchant_reference,amount_cents,currency,status,idempotency_key,request_fingerprint,occurred_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, event.ID, event.TenantID, event.Type, event.MerchantRef, event.AmountCents, event.Currency, event.Status, nullString(event.IdempotencyKey), fingerprint, event.OccurredAt.Unix())
	if err != nil {
		return domain.PaymentEvent{}, false, err
	}
	now := time.Now().UTC().Unix()
	for _, endpoint := range endpoints {
		_, err = tx.ExecContext(ctx, `INSERT INTO deliveries(id,event_id,endpoint_id,status,attempt_count,next_attempt_at,last_error,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, domain.NewID("dlv"), event.ID, endpoint.ID, "pending", 0, now, "", now, now)
		if err != nil {
			return domain.PaymentEvent{}, false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return domain.PaymentEvent{}, false, err
	}
	return event, false, nil
}
func getEvent(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (domain.PaymentEvent, error) {
	var e domain.PaymentEvent
	var occurred int64
	err := q.QueryRowContext(ctx, `SELECT tenant_id,id,type,merchant_reference,amount_cents,currency,status,occurred_at FROM events WHERE id=?`, id).Scan(&e.TenantID, &e.ID, &e.Type, &e.MerchantRef, &e.AmountCents, &e.Currency, &e.Status, &occurred)
	if err != nil {
		return e, err
	}
	e.OccurredAt = time.Unix(occurred, 0).UTC()
	return e, nil
}
func (s *Store) GetEvent(ctx context.Context, tenantID, id string) (domain.PaymentEvent, error) {
	var e domain.PaymentEvent
	var occurred int64
	err := s.db.QueryRowContext(ctx, `SELECT tenant_id,id,type,merchant_reference,amount_cents,currency,status,occurred_at FROM events WHERE tenant_id=? AND id=?`, tenantID, id).Scan(&e.TenantID, &e.ID, &e.Type, &e.MerchantRef, &e.AmountCents, &e.Currency, &e.Status, &occurred)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ports.ErrNotFound
	}
	if err == nil {
		e.OccurredAt = time.Unix(occurred, 0).UTC()
	}
	return e, err
}

func (s *Store) ClaimDueDelivery(ctx context.Context, now time.Time) (domain.Delivery, domain.Endpoint, domain.PaymentEvent, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Delivery{}, domain.Endpoint{}, domain.PaymentEvent{}, false, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM deliveries WHERE status='pending' AND next_attempt_at<=? ORDER BY next_attempt_at LIMIT 1`, now.Unix()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Delivery{}, domain.Endpoint{}, domain.PaymentEvent{}, false, nil
	}
	if err != nil {
		return domain.Delivery{}, domain.Endpoint{}, domain.PaymentEvent{}, false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE deliveries SET status='in_progress',updated_at=? WHERE id=? AND status='pending'`, now.Unix(), id)
	if err != nil {
		return domain.Delivery{}, domain.Endpoint{}, domain.PaymentEvent{}, false, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return domain.Delivery{}, domain.Endpoint{}, domain.PaymentEvent{}, false, nil
	}
	delivery, endpoint, event, err := loadDispatch(ctx, tx, id)
	if err != nil {
		return domain.Delivery{}, domain.Endpoint{}, domain.PaymentEvent{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return domain.Delivery{}, domain.Endpoint{}, domain.PaymentEvent{}, false, err
	}
	return delivery, endpoint, event, true, nil
}
func loadDispatch(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (domain.Delivery, domain.Endpoint, domain.PaymentEvent, error) {
	var d domain.Delivery
	var next, created, updated, endpointCreated, occurred int64
	var active int
	var e domain.Endpoint
	var event domain.PaymentEvent
	err := q.QueryRowContext(ctx, `SELECT d.id,d.event_id,d.endpoint_id,d.status,d.attempt_count,d.next_attempt_at,d.last_error,d.created_at,d.updated_at,e.tenant_id,e.id,e.url,e.secret,e.active,e.created_at,v.tenant_id,v.id,v.type,v.merchant_reference,v.amount_cents,v.currency,v.status,v.occurred_at FROM deliveries d JOIN endpoints e ON e.id=d.endpoint_id JOIN events v ON v.id=d.event_id WHERE d.id=?`, id).Scan(&d.ID, &d.EventID, &d.EndpointID, &d.Status, &d.AttemptCount, &next, &d.LastError, &created, &updated, &e.TenantID, &e.ID, &e.URL, &e.Secret, &active, &endpointCreated, &event.TenantID, &event.ID, &event.Type, &event.MerchantRef, &event.AmountCents, &event.Currency, &event.Status, &occurred)
	if err != nil {
		return d, e, event, err
	}
	d.NextAttemptAt = time.Unix(next, 0).UTC()
	d.CreatedAt = time.Unix(created, 0).UTC()
	d.UpdatedAt = time.Unix(updated, 0).UTC()
	e.Active = active == 1
	e.CreatedAt = time.Unix(endpointCreated, 0).UTC()
	event.OccurredAt = time.Unix(occurred, 0).UTC()
	return d, e, event, nil
}

func (s *Store) RecordAttempt(ctx context.Context, deliveryID string, statusCode int, requestErr string, duration time.Duration, retryAt *time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	err = tx.QueryRowContext(ctx, `SELECT attempt_count FROM deliveries WHERE id=? AND status='in_progress'`, deliveryID).Scan(&count)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.ErrNotFound
	}
	if err != nil {
		return err
	}
	count++
	_, err = tx.ExecContext(ctx, `INSERT INTO delivery_attempts(id,delivery_id,number,status_code,error,duration_ms,created_at) VALUES(?,?,?,?,?,?,?)`, domain.NewID("att"), deliveryID, count, statusCode, requestErr, duration.Milliseconds(), time.Now().UTC().Unix())
	if err != nil {
		return err
	}
	state := "succeeded"
	next := time.Now().UTC().Unix()
	if requestErr != "" || statusCode < 200 || statusCode >= 300 {
		if retryAt != nil {
			state = "pending"
			next = retryAt.Unix()
		} else {
			state = "failed"
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE deliveries SET status=?,attempt_count=?,next_attempt_at=?,last_error=?,updated_at=? WHERE id=?`, state, count, next, requestErr, time.Now().UTC().Unix(), deliveryID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ListDeliveries(ctx context.Context, tenantID, eventID string) ([]domain.Delivery, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.id,d.event_id,d.endpoint_id,d.status,d.attempt_count,d.next_attempt_at,d.last_error,d.created_at,d.updated_at FROM deliveries d JOIN events e ON e.id=d.event_id WHERE e.tenant_id=? AND d.event_id=? ORDER BY d.created_at`, tenantID, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []domain.Delivery
	for rows.Next() {
		var d domain.Delivery
		var next, created, updated int64
		if err := rows.Scan(&d.ID, &d.EventID, &d.EndpointID, &d.Status, &d.AttemptCount, &next, &d.LastError, &created, &updated); err != nil {
			return nil, err
		}
		d.NextAttemptAt = time.Unix(next, 0).UTC()
		d.CreatedAt = time.Unix(created, 0).UTC()
		d.UpdatedAt = time.Unix(updated, 0).UTC()
		all = append(all, d)
	}
	return all, rows.Err()
}
func (s *Store) ListAttempts(ctx context.Context, tenantID, deliveryID string) ([]domain.DeliveryAttempt, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,a.delivery_id,a.number,a.status_code,a.error,a.duration_ms,a.created_at FROM delivery_attempts a JOIN deliveries d ON d.id=a.delivery_id JOIN events e ON e.id=d.event_id WHERE e.tenant_id=? AND a.delivery_id=? ORDER BY a.number`, tenantID, deliveryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []domain.DeliveryAttempt
	for rows.Next() {
		var a domain.DeliveryAttempt
		var created int64
		if err := rows.Scan(&a.ID, &a.DeliveryID, &a.Number, &a.StatusCode, &a.Error, &a.DurationMS, &created); err != nil {
			return nil, err
		}
		a.CreatedAt = time.Unix(created, 0).UTC()
		all = append(all, a)
	}
	return all, rows.Err()
}
func (s *Store) ReplayFailed(ctx context.Context, tenantID, deliveryID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE deliveries SET status='pending',attempt_count=0,next_attempt_at=?,last_error='',updated_at=? WHERE id=? AND status='failed' AND event_id IN (SELECT id FROM events WHERE tenant_id=?)`, time.Now().UTC().Unix(), time.Now().UTC().Unix(), deliveryID, tenantID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return ports.ErrNotFound
	}
	return nil
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
