package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/example/payrelay/internal/core/domain"
	"github.com/example/payrelay/internal/core/ports"
)

func TestStore_GivenRepeatedIdempotencyKey_WhenCreatingEvent_ThenReturnsOriginalOrConflict(t *testing.T) {
	repo, err := Open(filepath.Join(t.TempDir(), "payrelay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	ctx := context.Background()
	first := domain.PaymentEvent{ID: "evt_first", Type: "payment.succeeded", MerchantRef: "order_1", AmountCents: 2500, Currency: "USD", Status: "succeeded", OccurredAt: time.Now(), IdempotencyKey: "request_1"}
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
	repo, err := Open(filepath.Join(t.TempDir(), "payrelay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	endpoint := domain.Endpoint{ID: "we_lifecycle", URL: "https://example.test/webhooks", Secret: "whsec_lifecycle", Active: true, CreatedAt: now}
	if err := repo.CreateEndpoint(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	endpoints, err := repo.ListActiveEndpoints(ctx)
	if err != nil || len(endpoints) != 1 || endpoints[0].Secret != endpoint.Secret {
		t.Fatalf("active endpoints: %#v (%v)", endpoints, err)
	}
	gotEndpoint, err := repo.GetEndpoint(ctx, endpoint.ID)
	if err != nil || gotEndpoint.URL != endpoint.URL {
		t.Fatalf("get endpoint: %#v (%v)", gotEndpoint, err)
	}
	if _, err := repo.GetEndpoint(ctx, "we_missing"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("missing endpoint error = %v", err)
	}

	event := domain.PaymentEvent{ID: "evt_lifecycle", Type: "payment.succeeded", MerchantRef: "order_lifecycle", AmountCents: 700, Currency: "USD", Status: "succeeded", OccurredAt: now}
	if _, replay, err := repo.CreateEvent(ctx, event, "lifecycle", endpoints); err != nil || replay {
		t.Fatalf("create event: replay=%v err=%v", replay, err)
	}
	gotEvent, err := repo.GetEvent(ctx, event.ID)
	if err != nil || gotEvent.MerchantRef != event.MerchantRef {
		t.Fatalf("get event: %#v (%v)", gotEvent, err)
	}
	if _, err := repo.GetEvent(ctx, "evt_missing"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("missing event error = %v", err)
	}
	if err := repo.RecordAttempt(ctx, "dlv_missing", 500, "missing", time.Millisecond, nil); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("missing delivery attempt error = %v", err)
	}
	if attempts, err := repo.ListAttempts(ctx, "dlv_missing"); err != nil || len(attempts) != 0 {
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
	deliveries, err := repo.ListDeliveries(ctx, event.ID)
	if err != nil || len(deliveries) != 1 || deliveries[0].Status != "succeeded" || deliveries[0].AttemptCount != 2 {
		t.Fatalf("deliveries: %#v (%v)", deliveries, err)
	}
	attempts, err := repo.ListAttempts(ctx, delivery.ID)
	if err != nil || len(attempts) != 2 || attempts[0].StatusCode != 503 || attempts[1].StatusCode != 204 {
		t.Fatalf("attempts: %#v (%v)", attempts, err)
	}

	failed := domain.PaymentEvent{ID: "evt_failed", Type: "payment.failed", MerchantRef: "order_failed", AmountCents: 100, Currency: "USD", Status: "failed", OccurredAt: now}
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
	if err := repo.ReplayFailed(ctx, failedDelivery.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplayFailed(ctx, "dlv_missing"); !errors.Is(err, ports.ErrNotFound) {
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
