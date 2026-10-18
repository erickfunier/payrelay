package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/example/payrelay/internal/domain"
)

func TestCreateEventHonorsIdempotencyKey(t *testing.T) {
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
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}
