package ports

import (
	"context"
	"errors"
	"time"

	"github.com/example/payrelay/internal/core/domain"
)

var ErrNotFound = errors.New("not found")
var ErrIdempotencyConflict = errors.New("idempotency key already used with a different request")

// Repository is the outbound persistence port used by PayRelay use cases.
// Adapters may persist data in SQLite today and another database tomorrow.
type Repository interface {
	CreateEndpoint(context.Context, domain.Endpoint) error
	ListActiveEndpoints(context.Context, string) ([]domain.Endpoint, error)
	GetEndpoint(context.Context, string, string) (domain.Endpoint, error)
	CreateEvent(context.Context, domain.PaymentEvent, string, []domain.Endpoint) (domain.PaymentEvent, bool, error)
	GetEvent(context.Context, string, string) (domain.PaymentEvent, error)
	ClaimDueDelivery(context.Context, time.Time) (domain.Delivery, domain.Endpoint, domain.PaymentEvent, bool, error)
	RecordAttempt(context.Context, string, int, string, time.Duration, *time.Time) error
	ListDeliveries(context.Context, string, string) ([]domain.Delivery, error)
	ListAttempts(context.Context, string, string) ([]domain.DeliveryAttempt, error)
	ReplayFailed(context.Context, string, string) error
}

// WebhookSender is the outbound delivery port. Its adapter owns transport
// details such as JSON serialization, HMAC signing, and HTTP timeouts.
type WebhookSender interface {
	Send(context.Context, domain.Endpoint, domain.PaymentEvent) (DeliveryResult, error)
}

type DeliveryResult struct {
	StatusCode int
	Duration   time.Duration
}
