package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/example/payrelay/internal/domain"
	"github.com/example/payrelay/internal/store"
)

type Dispatcher struct {
	store       *store.Store
	client      *http.Client
	maxAttempts int
	logger      *slog.Logger
}

func NewDispatcher(repo *store.Store, client *http.Client, maxAttempts int, logger *slog.Logger) *Dispatcher {
	return &Dispatcher{store: repo, client: client, maxAttempts: maxAttempts, logger: logger}
}

func (d *Dispatcher) DispatchOne(ctx context.Context) (bool, error) {
	delivery, endpoint, event, found, err := d.store.ClaimDueDelivery(ctx, time.Now().UTC())
	if err != nil || !found {
		return found, err
	}
	payload, err := json.Marshal(struct {
		ID     string              `json:"id"`
		Type   string              `json:"type"`
		Data   domain.PaymentEvent `json:"data"`
		SentAt time.Time           `json:"sent_at"`
	}{event.ID, event.Type, event, time.Now().UTC()})
	if err != nil {
		return true, err
	}
	timestamp := time.Now().UTC().Unix()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.URL, bytes.NewReader(payload))
	if err != nil {
		return true, d.recordError(ctx, delivery, 0, err.Error(), 0)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "PayRelay/1.0 (mock events only)")
	req.Header.Set("X-PayRelay-Event-ID", event.ID)
	req.Header.Set("X-PayRelay-Timestamp", strconv.FormatInt(timestamp, 10))
	req.Header.Set("X-PayRelay-Signature", Sign(endpoint.Secret, payload, timestamp))
	started := time.Now()
	response, requestErr := d.client.Do(req)
	elapsed := time.Since(started)
	statusCode := 0
	errorText := ""
	if requestErr != nil {
		errorText = requestErr.Error()
	} else {
		statusCode = response.StatusCode
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		response.Body.Close()
		if statusCode < 200 || statusCode >= 300 {
			errorText = fmt.Sprintf("receiver returned HTTP %d", statusCode)
		}
	}
	if errorText != "" {
		return true, d.recordError(ctx, delivery, statusCode, errorText, elapsed)
	}
	return true, d.store.RecordAttempt(ctx, delivery.ID, statusCode, "", elapsed, nil)
}
func (d *Dispatcher) recordError(ctx context.Context, job domain.Delivery, status int, message string, elapsed time.Duration) error {
	var retryAt *time.Time
	if job.AttemptCount+1 < d.maxAttempts {
		next := time.Now().UTC().Add(RetryAfter(job.AttemptCount + 1))
		retryAt = &next
	}
	return d.store.RecordAttempt(ctx, job.ID, status, message, elapsed, retryAt)
}

func RunWorker(ctx context.Context, d *Dispatcher, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		for {
			found, err := d.DispatchOne(ctx)
			if err != nil {
				logger.Error("delivery dispatch failed", "error", err)
				break
			}
			if !found {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
