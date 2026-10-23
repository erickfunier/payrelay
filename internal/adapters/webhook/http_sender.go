package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/example/payrelay/internal/core/domain"
	"github.com/example/payrelay/internal/core/ports"
	"github.com/example/payrelay/internal/security/webhooktarget"
)

// HTTPSender is the outbound adapter that converts a domain event into a
// signed HTTP webhook request.
type HTTPSender struct{ client *http.Client }

var _ ports.WebhookSender = (*HTTPSender)(nil)

func NewHTTPSender(client *http.Client) *HTTPSender { return &HTTPSender{client: client} }

// NewSafeHTTPClient creates the production webhook client. It validates DNS
// answers immediately before connection and never follows redirects, so an
// endpoint cannot pivot an outbound request into a private network.
func NewSafeHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = webhooktarget.NewValidator().DialContext
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return webhooktarget.ErrUnsafeTarget
		},
	}
}

func (s *HTTPSender) Send(ctx context.Context, endpoint domain.Endpoint, event domain.PaymentEvent) (ports.DeliveryResult, error) {
	payload, err := json.Marshal(struct {
		ID     string              `json:"id"`
		Type   string              `json:"type"`
		Data   domain.PaymentEvent `json:"data"`
		SentAt time.Time           `json:"sent_at"`
	}{event.ID, event.Type, event, time.Now().UTC()})
	if err != nil {
		return ports.DeliveryResult{}, err
	}
	timestamp := time.Now().UTC().Unix()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.URL, bytes.NewReader(payload))
	if err != nil {
		return ports.DeliveryResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "PayRelay/1.0 (mock events only)")
	req.Header.Set("X-PayRelay-Event-ID", event.ID)
	req.Header.Set("X-PayRelay-Timestamp", strconv.FormatInt(timestamp, 10))
	req.Header.Set("X-PayRelay-Signature", Sign(endpoint.Secret, payload, timestamp))
	started := time.Now()
	response, err := s.client.Do(req)
	if err != nil {
		return ports.DeliveryResult{Duration: time.Since(started)}, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	return ports.DeliveryResult{StatusCode: response.StatusCode, Duration: time.Since(started)}, nil
}
