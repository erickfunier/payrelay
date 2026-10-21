package domain

import "time"

type Endpoint struct {
	TenantID  string    `json:"-"`
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Secret    string    `json:"secret,omitempty"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}

type PaymentEvent struct {
	TenantID       string    `json:"-"`
	ID             string    `json:"id"`
	Type           string    `json:"type"`
	MerchantRef    string    `json:"merchant_reference"`
	AmountCents    int64     `json:"amount_cents"`
	Currency       string    `json:"currency"`
	Status         string    `json:"status"`
	OccurredAt     time.Time `json:"occurred_at"`
	IdempotencyKey string    `json:"-"`
}

type Delivery struct {
	ID            string    `json:"id"`
	EventID       string    `json:"event_id"`
	EndpointID    string    `json:"endpoint_id"`
	Status        string    `json:"status"`
	AttemptCount  int       `json:"attempt_count"`
	NextAttemptAt time.Time `json:"next_attempt_at"`
	LastError     string    `json:"last_error,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type DeliveryAttempt struct {
	ID         string    `json:"id"`
	DeliveryID string    `json:"delivery_id"`
	Number     int       `json:"number"`
	StatusCode int       `json:"status_code,omitempty"`
	Error      string    `json:"error,omitempty"`
	DurationMS int64     `json:"duration_ms"`
	CreatedAt  time.Time `json:"created_at"`
}
