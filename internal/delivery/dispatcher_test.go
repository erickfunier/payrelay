package delivery_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/example/payrelay/internal/delivery"
	"github.com/example/payrelay/internal/domain"
	"github.com/example/payrelay/internal/store"
)

func TestDispatcherSendsSignedPayloadAndRecordsSuccess(t *testing.T) {
	var observedSignature, observedTimestamp string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		observedSignature = r.Header.Get("X-PayRelay-Signature")
		observedTimestamp = r.Header.Get("X-PayRelay-Timestamp")
		if len(body) == 0 {
			t.Error("empty body")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()
	repo := testStore(t)
	defer repo.Close()
	ctx := context.Background()
	endpoint := domain.Endpoint{ID: "we_test", URL: receiver.URL, Secret: "whsec_test", Active: true, CreatedAt: time.Now().UTC()}
	if err := repo.CreateEndpoint(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	event := domain.PaymentEvent{ID: "evt_test", Type: "payment.succeeded", MerchantRef: "order_42", AmountCents: 1200, Currency: "USD", Status: "succeeded", OccurredAt: time.Now().UTC()}
	if _, _, err := repo.CreateEvent(ctx, event, "fingerprint", []domain.Endpoint{endpoint}); err != nil {
		t.Fatal(err)
	}
	d := delivery.NewDispatcher(repo, receiver.Client(), 3, slog.New(slog.NewTextHandler(io.Discard, nil)))
	found, err := d.DispatchOne(ctx)
	if err != nil || !found {
		t.Fatalf("dispatch: found=%v err=%v", found, err)
	}
	deliveries, err := repo.ListDeliveries(ctx, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deliveries[0].Status != "succeeded" || deliveries[0].AttemptCount != 1 {
		t.Fatalf("unexpected delivery: %#v", deliveries[0])
	}
	if observedSignature == "" || observedTimestamp == "" {
		t.Fatal("missing signed webhook headers")
	}
}

func TestDispatcherRetriesFailureThenMarksFailed(t *testing.T) {
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer receiver.Close()
	repo := testStore(t)
	defer repo.Close()
	ctx := context.Background()
	endpoint := domain.Endpoint{ID: "we_retry", URL: receiver.URL, Secret: "whsec_retry", Active: true, CreatedAt: time.Now().UTC()}
	if err := repo.CreateEndpoint(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	event := domain.PaymentEvent{ID: "evt_retry", Type: "payment.failed", MerchantRef: "order_43", AmountCents: 100, Currency: "USD", Status: "failed", OccurredAt: time.Now().UTC()}
	if _, _, err := repo.CreateEvent(ctx, event, "retry", []domain.Endpoint{endpoint}); err != nil {
		t.Fatal(err)
	}
	d := delivery.NewDispatcher(repo, receiver.Client(), 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := d.DispatchOne(ctx); err != nil {
		t.Fatal(err)
	}
	deliveries, _ := repo.ListDeliveries(ctx, event.ID)
	if deliveries[0].Status != "failed" || deliveries[0].AttemptCount != 1 {
		t.Fatalf("unexpected delivery: %#v", deliveries[0])
	}
	attempts, err := repo.ListAttempts(ctx, deliveries[0].ID)
	if err != nil || len(attempts) != 1 || attempts[0].StatusCode != 503 {
		t.Fatalf("unexpected attempts: %#v (%v)", attempts, err)
	}
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	repo, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	return repo
}
