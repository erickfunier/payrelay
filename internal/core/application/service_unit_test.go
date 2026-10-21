package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/example/payrelay/internal/core/domain"
	"github.com/example/payrelay/internal/core/ports"
)

func TestService_GivenRepositoryPorts_WhenManagingResources_ThenDelegatesUseCases(t *testing.T) {
	const tenantID = "merchant_alpha"
	repository := &fakeRepository{
		endpoints:  []domain.Endpoint{{ID: "we_existing", URL: "https://example.test", Secret: "whsec_existing", Active: true}},
		event:      domain.PaymentEvent{ID: "evt_existing", Type: "payment.succeeded"},
		deliveries: []domain.Delivery{{ID: "dlv_existing"}},
		attempts:   []domain.DeliveryAttempt{{ID: "att_existing"}},
	}
	service := NewService(repository, fakeSender{}, 3)
	ctx := context.Background()

	createdEndpoint, err := service.CreateEndpoint(ctx, tenantID, "https://receiver.test/webhooks")
	if err != nil || createdEndpoint.ID == "" || !strings.HasPrefix(createdEndpoint.Secret, "whsec_") || repository.createdEndpoint != createdEndpoint {
		t.Fatalf("create endpoint: %#v (%v)", createdEndpoint, err)
	}
	if endpoints, err := service.ListActiveEndpoints(ctx, tenantID); err != nil || len(endpoints) != 1 {
		t.Fatalf("list endpoints: %#v (%v)", endpoints, err)
	}

	event := domain.PaymentEvent{ID: "evt_new", Type: "payment.succeeded", MerchantRef: "order_1", AmountCents: 1200, Currency: "USD", Status: "succeeded"}
	createdEvent, replay, err := service.CreateEvent(ctx, tenantID, event)
	if err != nil || replay || createdEvent.ID != event.ID || repository.fingerprint != "payment.succeeded|order_1|1200|USD|succeeded" {
		t.Fatalf("create event: %#v replay=%v fingerprint=%q err=%v", createdEvent, replay, repository.fingerprint, err)
	}
	if got, err := service.GetEvent(ctx, tenantID, "evt_existing"); err != nil || got.ID != "evt_existing" {
		t.Fatalf("get event: %#v (%v)", got, err)
	}
	if got, err := service.ListDeliveries(ctx, tenantID, "evt_existing"); err != nil || len(got) != 1 {
		t.Fatalf("list deliveries: %#v (%v)", got, err)
	}
	if got, err := service.ListAttempts(ctx, tenantID, "dlv_existing"); err != nil || len(got) != 1 {
		t.Fatalf("list attempts: %#v (%v)", got, err)
	}
	if err := service.ReplayFailed(ctx, tenantID, "dlv_existing"); err != nil || repository.replayedID != "dlv_existing" {
		t.Fatalf("replay: id=%q err=%v", repository.replayedID, err)
	}
}

func TestService_GivenDeliveryResult_WhenDispatching_ThenRecordsExpectedOutcome(t *testing.T) {
	cases := []struct {
		name       string
		found      bool
		result     ports.DeliveryResult
		senderErr  error
		maxAttempt int
		wantError  string
		wantStatus int
		wantRetry  bool
	}{
		{name: "given_no_due_delivery_when_dispatching_then_reports_not_found", found: false},
		{name: "given_successful_response_when_dispatching_then_records_success", found: true, result: ports.DeliveryResult{StatusCode: 204}, maxAttempt: 3, wantStatus: 204},
		{name: "given_retryable_response_when_dispatching_then_schedules_retry", found: true, result: ports.DeliveryResult{StatusCode: 503}, maxAttempt: 3, wantStatus: 503, wantRetry: true},
		{name: "given_transport_error_when_dispatching_then_records_failure", found: true, senderErr: errors.New("offline"), maxAttempt: 1, wantError: "offline"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			repository := &fakeRepository{found: tt.found, claimedDelivery: domain.Delivery{ID: "dlv_1"}, claimedEndpoint: domain.Endpoint{ID: "we_1"}, claimedEvent: domain.PaymentEvent{ID: "evt_1"}}
			service := NewService(repository, fakeSender{result: tt.result, err: tt.senderErr}, tt.maxAttempt)
			found, err := service.DispatchOne(context.Background())
			if err != nil || found != tt.found {
				t.Fatalf("DispatchOne() found=%v err=%v", found, err)
			}
			if !tt.found {
				return
			}
			if repository.recordedStatus != tt.wantStatus {
				t.Fatalf("recorded status=%d, want %d", repository.recordedStatus, tt.wantStatus)
			}
			if tt.wantError != "" && !strings.Contains(repository.recordedError, tt.wantError) {
				t.Fatalf("recorded error=%q", repository.recordedError)
			}
			if (repository.retryAt != nil) != tt.wantRetry {
				t.Fatalf("retry scheduled=%v, want %v", repository.retryAt != nil, tt.wantRetry)
			}
		})
	}
}

type fakeRepository struct {
	endpoints       []domain.Endpoint
	event           domain.PaymentEvent
	deliveries      []domain.Delivery
	attempts        []domain.DeliveryAttempt
	createdEndpoint domain.Endpoint
	createdEvent    domain.PaymentEvent
	fingerprint     string
	replayedID      string
	found           bool
	claimedDelivery domain.Delivery
	claimedEndpoint domain.Endpoint
	claimedEvent    domain.PaymentEvent
	recordedStatus  int
	recordedError   string
	retryAt         *time.Time
}

func (f *fakeRepository) CreateEndpoint(_ context.Context, endpoint domain.Endpoint) error {
	f.createdEndpoint = endpoint
	return nil
}
func (f *fakeRepository) ListActiveEndpoints(context.Context, string) ([]domain.Endpoint, error) {
	return f.endpoints, nil
}
func (f *fakeRepository) GetEndpoint(context.Context, string, string) (domain.Endpoint, error) {
	return domain.Endpoint{}, ports.ErrNotFound
}
func (f *fakeRepository) CreateEvent(_ context.Context, event domain.PaymentEvent, fingerprint string, _ []domain.Endpoint) (domain.PaymentEvent, bool, error) {
	f.createdEvent = event
	f.fingerprint = fingerprint
	return event, false, nil
}
func (f *fakeRepository) GetEvent(context.Context, string, string) (domain.PaymentEvent, error) {
	return f.event, nil
}
func (f *fakeRepository) ClaimDueDelivery(context.Context, time.Time) (domain.Delivery, domain.Endpoint, domain.PaymentEvent, bool, error) {
	return f.claimedDelivery, f.claimedEndpoint, f.claimedEvent, f.found, nil
}
func (f *fakeRepository) RecordAttempt(_ context.Context, _ string, status int, message string, _ time.Duration, retryAt *time.Time) error {
	f.recordedStatus = status
	f.recordedError = message
	f.retryAt = retryAt
	return nil
}
func (f *fakeRepository) ListDeliveries(context.Context, string, string) ([]domain.Delivery, error) {
	return f.deliveries, nil
}
func (f *fakeRepository) ListAttempts(context.Context, string, string) ([]domain.DeliveryAttempt, error) {
	return f.attempts, nil
}
func (f *fakeRepository) ReplayFailed(_ context.Context, _ string, id string) error {
	f.replayedID = id
	return nil
}

type fakeSender struct {
	result ports.DeliveryResult
	err    error
}

func (f fakeSender) Send(context.Context, domain.Endpoint, domain.PaymentEvent) (ports.DeliveryResult, error) {
	return f.result, f.err
}
