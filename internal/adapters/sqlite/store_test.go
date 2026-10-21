package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/example/payrelay/internal/core/domain"
	"github.com/example/payrelay/internal/core/ports"
)

func TestStore_GivenRepeatedIdempotencyKey_WhenCreatingEvent_ThenReturnsOriginalOrConflict(t *testing.T) {
	const tenantID = "merchant_alpha"
	repo, err := Open(filepath.Join(t.TempDir(), "payrelay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	ctx := context.Background()
	first := domain.PaymentEvent{TenantID: tenantID, ID: "evt_first", Type: "payment.succeeded", MerchantRef: "order_1", AmountCents: 2500, Currency: "USD", Status: "succeeded", OccurredAt: time.Now(), IdempotencyKey: "merchant_alpha:request_1"}
	saved, replay, err := repo.CreateEvent(ctx, first, "fingerprint_1", nil)
	if err != nil || replay || saved.ID != first.ID {
		t.Fatalf("first request: saved=%#v replay=%v err=%v", saved, replay, err)
	}

	second := first
	second.ID = "evt_second"
	saved, replay, err = repo.CreateEvent(ctx, second, "fingerprint_1", nil)
	if err != nil || !replay || saved.ID != first.ID {
		t.Fatalf("replay request: saved=%#v replay=%v err=%v", saved, replay, err)
	}

	_, _, err = repo.CreateEvent(ctx, second, "different_fingerprint", nil)
	if !errors.Is(err, ports.ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

func TestStore_GivenDeliveryLifecycle_WhenAttemptsAreRecorded_ThenPersistsHistoryAndReplay(t *testing.T) {
	const tenantID = "merchant_alpha"
	repo, err := Open(filepath.Join(t.TempDir(), "payrelay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	endpoint := domain.Endpoint{TenantID: tenantID, ID: "we_lifecycle", URL: "https://example.test/webhooks", Secret: "whsec_lifecycle", Active: true, CreatedAt: now}
	if err := repo.CreateEndpoint(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	endpoints, err := repo.ListActiveEndpoints(ctx, tenantID)
	if err != nil || len(endpoints) != 1 || endpoints[0].Secret != endpoint.Secret {
		t.Fatalf("active endpoints: %#v (%v)", endpoints, err)
	}
	gotEndpoint, err := repo.GetEndpoint(ctx, tenantID, endpoint.ID)
	if err != nil || gotEndpoint.URL != endpoint.URL {
		t.Fatalf("get endpoint: %#v (%v)", gotEndpoint, err)
	}
	if _, err := repo.GetEndpoint(ctx, tenantID, "we_missing"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("missing endpoint error = %v", err)
	}
	allEndpoints, err := repo.ListEndpoints(ctx, tenantID)
	if err != nil || len(allEndpoints) != 1 {
		t.Fatalf("all endpoints: %#v (%v)", allEndpoints, err)
	}
	if _, err := repo.SetEndpointActive(ctx, tenantID, endpoint.ID, false); err != nil {
		t.Fatal(err)
	}
	if active, err := repo.ListActiveEndpoints(ctx, tenantID); err != nil || len(active) != 0 {
		t.Fatalf("disabled active endpoints: %#v (%v)", active, err)
	}
	rotated, err := repo.RotateEndpointSecret(ctx, tenantID, endpoint.ID, "whsec_rotated")
	if err != nil || rotated.Secret != "whsec_rotated" || rotated.Active {
		t.Fatalf("rotated endpoint: %#v (%v)", rotated, err)
	}
	if _, err := repo.SetEndpointActive(ctx, tenantID, endpoint.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetEndpointActive(ctx, tenantID, "we_missing", true); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("missing endpoint update error = %v", err)
	}
	if _, err := repo.RotateEndpointSecret(ctx, tenantID, "we_missing", "whsec_missing"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("missing endpoint rotation error = %v", err)
	}

	event := domain.PaymentEvent{TenantID: tenantID, ID: "evt_lifecycle", Type: "payment.succeeded", MerchantRef: "order_lifecycle", AmountCents: 700, Currency: "USD", Status: "succeeded", OccurredAt: now}
	if _, replay, err := repo.CreateEvent(ctx, event, "lifecycle", endpoints); err != nil || replay {
		t.Fatalf("create event: replay=%v err=%v", replay, err)
	}
	gotEvent, err := repo.GetEvent(ctx, tenantID, event.ID)
	if err != nil || gotEvent.MerchantRef != event.MerchantRef {
		t.Fatalf("get event: %#v (%v)", gotEvent, err)
	}
	if _, err := repo.GetEvent(ctx, tenantID, "evt_missing"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("missing event error = %v", err)
	}
	if err := repo.RecordAttempt(ctx, "dlv_missing", 500, "missing", time.Millisecond, nil); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("missing delivery attempt error = %v", err)
	}
	if attempts, err := repo.ListAttempts(ctx, tenantID, "dlv_missing"); err != nil || len(attempts) != 0 {
		t.Fatalf("missing delivery attempts: %#v (%v)", attempts, err)
	}

	delivery, claimedEndpoint, claimedEvent, found, err := repo.ClaimDueDelivery(ctx, now.Add(time.Second))
	if err != nil || !found || claimedEndpoint.ID != endpoint.ID || claimedEvent.ID != event.ID || delivery.Status != "in_progress" {
		t.Fatalf("claim: delivery=%#v endpoint=%#v event=%#v found=%v err=%v", delivery, claimedEndpoint, claimedEvent, found, err)
	}
	retryAt := now.Add(time.Hour)
	if err := repo.RecordAttempt(ctx, delivery.ID, 503, "receiver returned HTTP 503", 20*time.Millisecond, &retryAt); err != nil {
		t.Fatal(err)
	}
	if _, _, _, found, err := repo.ClaimDueDelivery(ctx, now.Add(time.Minute)); err != nil || found {
		t.Fatalf("delivery should wait for retry: found=%v err=%v", found, err)
	}
	delivery, _, _, found, err = repo.ClaimDueDelivery(ctx, retryAt.Add(time.Second))
	if err != nil || !found {
		t.Fatalf("retry claim: found=%v err=%v", found, err)
	}
	if err := repo.RecordAttempt(ctx, delivery.ID, 204, "", 5*time.Millisecond, nil); err != nil {
		t.Fatal(err)
	}
	deliveries, err := repo.ListDeliveries(ctx, tenantID, event.ID)
	if err != nil || len(deliveries) != 1 || deliveries[0].Status != "succeeded" || deliveries[0].AttemptCount != 2 {
		t.Fatalf("deliveries: %#v (%v)", deliveries, err)
	}
	attempts, err := repo.ListAttempts(ctx, tenantID, delivery.ID)
	if err != nil || len(attempts) != 2 || attempts[0].StatusCode != 503 || attempts[1].StatusCode != 204 {
		t.Fatalf("attempts: %#v (%v)", attempts, err)
	}

	failed := domain.PaymentEvent{TenantID: tenantID, ID: "evt_failed", Type: "payment.failed", MerchantRef: "order_failed", AmountCents: 100, Currency: "USD", Status: "failed", OccurredAt: now}
	if _, _, err := repo.CreateEvent(ctx, failed, "failed", endpoints); err != nil {
		t.Fatal(err)
	}
	failedDelivery, _, _, found, err := repo.ClaimDueDelivery(ctx, now.Add(time.Second))
	if err != nil || !found {
		t.Fatalf("failed claim: found=%v err=%v", found, err)
	}
	if err := repo.RecordAttempt(ctx, failedDelivery.ID, 500, "receiver returned HTTP 500", time.Millisecond, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplayFailed(ctx, tenantID, failedDelivery.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplayFailed(ctx, tenantID, "dlv_missing"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("missing replay error = %v", err)
	}
}

func TestStore_GivenHelperInputs_WhenNormalized_ThenReturnsExpectedValues(t *testing.T) {
	if boolInt(true) != 1 || boolInt(false) != 0 {
		t.Fatal("bool conversion is incorrect")
	}
	if nullString("") != nil || nullString("value") != "value" {
		t.Fatal("null string conversion is incorrect")
	}
}

func TestStore_GivenPhaseOneDatabase_WhenOpened_ThenMigratesRecordsToLegacyTenant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "phase-one.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE endpoints (id TEXT PRIMARY KEY, url TEXT NOT NULL, secret TEXT NOT NULL, active INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL); INSERT INTO endpoints(id,url,secret,active,created_at) VALUES ('we_legacy','https://legacy.example.test','whsec_legacy',1,0);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	repo, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	endpoints, err := repo.ListActiveEndpoints(context.Background(), "legacy")
	if err != nil || len(endpoints) != 1 || endpoints[0].ID != "we_legacy" || endpoints[0].TenantID != "legacy" {
		t.Fatalf("migrated endpoints = %#v, err = %v", endpoints, err)
	}
}
