package application_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/payrelay/internal/adapters/scheduler"
	"github.com/example/payrelay/internal/adapters/sqlite"
	"github.com/example/payrelay/internal/adapters/webhook"
	"github.com/example/payrelay/internal/core/application"
	"github.com/example/payrelay/internal/core/domain"
)

func TestService_GivenSuccessfulReceiver_WhenDispatching_ThenSignsAndRecordsDelivery(t *testing.T) {
	const tenantID = "merchant_alpha"
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
	endpoint := domain.Endpoint{TenantID: tenantID, ID: "we_test", URL: receiver.URL, Secret: "whsec_test", Active: true, CreatedAt: time.Now().UTC()}
	if err := repo.CreateEndpoint(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	event := domain.PaymentEvent{TenantID: tenantID, ID: "evt_test", Type: "payment.succeeded", MerchantRef: "order_42", AmountCents: 1200, Currency: "USD", Status: "succeeded", OccurredAt: time.Now().UTC()}
	if _, _, err := repo.CreateEvent(ctx, event, "fingerprint", []domain.Endpoint{endpoint}); err != nil {
		t.Fatal(err)
	}
	d := application.NewService(repo, webhook.NewHTTPSender(receiver.Client()), 3)
	found, err := d.DispatchOne(ctx)
	if err != nil || !found {
		t.Fatalf("dispatch: found=%v err=%v", found, err)
	}
	deliveries, err := repo.ListDeliveries(ctx, tenantID, event.ID)
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

func TestService_GivenFinalReceiverFailure_WhenDispatching_ThenMarksDeliveryFailed(t *testing.T) {
	const tenantID = "merchant_alpha"
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer receiver.Close()
	repo := testStore(t)
	defer repo.Close()
	ctx := context.Background()
	endpoint := domain.Endpoint{TenantID: tenantID, ID: "we_retry", URL: receiver.URL, Secret: "whsec_retry", Active: true, CreatedAt: time.Now().UTC()}
	if err := repo.CreateEndpoint(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	event := domain.PaymentEvent{TenantID: tenantID, ID: "evt_retry", Type: "payment.failed", MerchantRef: "order_43", AmountCents: 100, Currency: "USD", Status: "failed", OccurredAt: time.Now().UTC()}
	if _, _, err := repo.CreateEvent(ctx, event, "retry", []domain.Endpoint{endpoint}); err != nil {
		t.Fatal(err)
	}
	d := application.NewService(repo, webhook.NewHTTPSender(receiver.Client()), 1)
	if _, err := d.DispatchOne(ctx); err != nil {
		t.Fatal(err)
	}
	deliveries, _ := repo.ListDeliveries(ctx, tenantID, event.ID)
	if deliveries[0].Status != "failed" || deliveries[0].AttemptCount != 1 {
		t.Fatalf("unexpected delivery: %#v", deliveries[0])
	}
	attempts, err := repo.ListAttempts(ctx, tenantID, deliveries[0].ID)
	if err != nil || len(attempts) != 1 || attempts[0].StatusCode != 503 {
		t.Fatalf("unexpected attempts: %#v (%v)", attempts, err)
	}
}

func TestService_GivenTransportOrRequestFailure_WhenDispatching_ThenRecordsFailedDelivery(t *testing.T) {
	const tenantID = "merchant_alpha"
	repo := testStore(t)
	defer repo.Close()
	ctx := context.Background()

	networkEndpoint := domain.Endpoint{TenantID: tenantID, ID: "we_network", URL: "https://receiver.test", Secret: "whsec_network", Active: true, CreatedAt: time.Now().UTC()}
	if err := repo.CreateEndpoint(ctx, networkEndpoint); err != nil {
		t.Fatal(err)
	}
	networkEvent := domain.PaymentEvent{TenantID: tenantID, ID: "evt_network", Type: "payment.failed", MerchantRef: "order_network", AmountCents: 100, Currency: "USD", Status: "failed", OccurredAt: time.Now().UTC()}
	if _, _, err := repo.CreateEvent(ctx, networkEvent, "network", []domain.Endpoint{networkEndpoint}); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("offline")
	})}
	dispatcher := application.NewService(repo, webhook.NewHTTPSender(client), 1)
	if _, err := dispatcher.DispatchOne(ctx); err != nil {
		t.Fatal(err)
	}
	deliveries, err := repo.ListDeliveries(ctx, tenantID, networkEvent.ID)
	if err != nil || deliveries[0].Status != "failed" || !strings.Contains(deliveries[0].LastError, "offline") {
		t.Fatalf("network delivery: %#v (%v)", deliveries, err)
	}

	invalidEndpoint := domain.Endpoint{TenantID: tenantID, ID: "we_invalid", URL: ":// invalid", Secret: "whsec_invalid", Active: true, CreatedAt: time.Now().UTC()}
	if err := repo.CreateEndpoint(ctx, invalidEndpoint); err != nil {
		t.Fatal(err)
	}
	invalidEvent := domain.PaymentEvent{TenantID: tenantID, ID: "evt_invalid", Type: "payment.failed", MerchantRef: "order_invalid", AmountCents: 100, Currency: "USD", Status: "failed", OccurredAt: time.Now().UTC()}
	if _, _, err := repo.CreateEvent(ctx, invalidEvent, "invalid", []domain.Endpoint{invalidEndpoint}); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.DispatchOne(ctx); err != nil {
		t.Fatal(err)
	}
	deliveries, err = repo.ListDeliveries(ctx, tenantID, invalidEvent.ID)
	if err != nil || deliveries[0].Status != "failed" || deliveries[0].LastError == "" {
		t.Fatalf("invalid URL delivery: %#v (%v)", deliveries, err)
	}
}

func TestService_GivenNoDueWork_WhenDispatchingOrStoppingWorker_ThenReturnsCleanly(t *testing.T) {
	repo := testStore(t)
	defer repo.Close()
	dispatcher := application.NewService(repo, webhook.NewHTTPSender(http.DefaultClient), 1)
	if found, err := dispatcher.DispatchOne(context.Background()); err != nil || found {
		t.Fatalf("empty dispatch: found=%v err=%v", found, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	scheduler.Run(ctx, dispatcher, time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func testStore(t *testing.T) *sqlite.Store {
	t.Helper()
	repo, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	return repo
}
