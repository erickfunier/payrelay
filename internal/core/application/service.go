package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/example/payrelay/internal/core/domain"
	"github.com/example/payrelay/internal/core/ports"
)

// Service contains PayRelay's application use cases. It depends only on
// outbound ports, never on a concrete database or HTTP implementation.
type Service struct {
	repository  ports.Repository
	sender      ports.WebhookSender
	maxAttempts int
}

func NewService(repository ports.Repository, sender ports.WebhookSender, maxAttempts int) *Service {
	return &Service{repository: repository, sender: sender, maxAttempts: maxAttempts}
}

func (s *Service) CreateEndpoint(ctx context.Context, tenantID, rawURL string) (domain.Endpoint, error) {
	secret, err := newSecret()
	if err != nil {
		return domain.Endpoint{}, err
	}
	now := time.Now().UTC()
	endpoint := domain.Endpoint{TenantID: tenantID, ID: domain.NewID("we"), URL: rawURL, Secret: secret, Active: true, CreatedAt: now, UpdatedAt: now}
	if err := s.repository.CreateEndpoint(ctx, endpoint); err != nil {
		return domain.Endpoint{}, err
	}
	return endpoint, nil
}

func (s *Service) ListActiveEndpoints(ctx context.Context, tenantID string) ([]domain.Endpoint, error) {
	return s.repository.ListActiveEndpoints(ctx, tenantID)
}

func (s *Service) ListEndpoints(ctx context.Context, tenantID string) ([]domain.Endpoint, error) {
	return s.repository.ListEndpoints(ctx, tenantID)
}

func (s *Service) GetEndpoint(ctx context.Context, tenantID, endpointID string) (domain.Endpoint, error) {
	return s.repository.GetEndpoint(ctx, tenantID, endpointID)
}

func (s *Service) SetEndpointActive(ctx context.Context, tenantID, endpointID string, active bool) (domain.Endpoint, error) {
	return s.repository.SetEndpointActive(ctx, tenantID, endpointID, active)
}

func (s *Service) RotateEndpointSecret(ctx context.Context, tenantID, endpointID string) (domain.Endpoint, error) {
	secret, err := newSecret()
	if err != nil {
		return domain.Endpoint{}, err
	}
	return s.repository.RotateEndpointSecret(ctx, tenantID, endpointID, secret)
}

func (s *Service) CreateEvent(ctx context.Context, tenantID string, event domain.PaymentEvent) (domain.PaymentEvent, bool, error) {
	event.TenantID = tenantID
	if event.IdempotencyKey != "" {
		event.IdempotencyKey = tenantID + ":" + event.IdempotencyKey
	}
	endpoints, err := s.repository.ListActiveEndpoints(ctx, tenantID)
	if err != nil {
		return domain.PaymentEvent{}, false, err
	}
	fingerprint := fmt.Sprintf("%s|%s|%d|%s|%s", event.Type, event.MerchantRef, event.AmountCents, event.Currency, event.Status)
	return s.repository.CreateEvent(ctx, event, fingerprint, endpoints)
}

func (s *Service) GetEvent(ctx context.Context, tenantID, id string) (domain.PaymentEvent, error) {
	return s.repository.GetEvent(ctx, tenantID, id)
}

func (s *Service) ListDeliveries(ctx context.Context, tenantID, eventID string) ([]domain.Delivery, error) {
	return s.repository.ListDeliveries(ctx, tenantID, eventID)
}

func (s *Service) ListAttempts(ctx context.Context, tenantID, deliveryID string) ([]domain.DeliveryAttempt, error) {
	return s.repository.ListAttempts(ctx, tenantID, deliveryID)
}

func (s *Service) ReplayFailed(ctx context.Context, tenantID, deliveryID string) error {
	return s.repository.ReplayFailed(ctx, tenantID, deliveryID)
}

// DispatchOne claims one due delivery and records a durable outcome. Delivery
// is at-least-once: receivers should deduplicate by event ID.
func (s *Service) DispatchOne(ctx context.Context) (bool, error) {
	delivery, endpoint, event, found, err := s.repository.ClaimDueDelivery(ctx, time.Now().UTC())
	if err != nil || !found {
		return found, err
	}
	result, err := s.sender.Send(ctx, endpoint, event)
	if err != nil {
		return true, s.recordFailure(ctx, delivery, result.StatusCode, err.Error(), result.Duration)
	}
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return true, s.recordFailure(ctx, delivery, result.StatusCode, fmt.Sprintf("receiver returned HTTP %d", result.StatusCode), result.Duration)
	}
	return true, s.repository.RecordAttempt(ctx, delivery.ID, result.StatusCode, "", result.Duration, nil)
}

func (s *Service) recordFailure(ctx context.Context, delivery domain.Delivery, statusCode int, message string, duration time.Duration) error {
	var retryAt *time.Time
	if delivery.AttemptCount+1 < s.maxAttempts {
		next := time.Now().UTC().Add(RetryAfter(delivery.AttemptCount + 1))
		retryAt = &next
	}
	return s.repository.RecordAttempt(ctx, delivery.ID, statusCode, message, duration, retryAt)
}

func newSecret() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "whsec_" + hex.EncodeToString(bytes), nil
}
