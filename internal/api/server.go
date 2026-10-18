package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/example/payrelay/internal/delivery"
	"github.com/example/payrelay/internal/domain"
	"github.com/example/payrelay/internal/store"
)

type Server struct {
	store      *store.Store
	dispatcher *delivery.Dispatcher
	logger     *slog.Logger
	mux        *http.ServeMux
}

func NewServer(repo *store.Store, dispatcher *delivery.Dispatcher, logger *slog.Logger) http.Handler {
	s := &Server{store: repo, dispatcher: dispatcher, logger: logger, mux: http.NewServeMux()}
	s.routes()
	return s.withLogging(s.mux)
}
func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.health)
	s.mux.HandleFunc("POST /v1/endpoints", s.createEndpoint)
	s.mux.HandleFunc("GET /v1/endpoints", s.listEndpoints)
	s.mux.HandleFunc("POST /v1/events", s.createEvent)
	s.mux.HandleFunc("GET /v1/events/{eventID}", s.getEvent)
	s.mux.HandleFunc("GET /v1/events/{eventID}/deliveries", s.listDeliveries)
	s.mux.HandleFunc("GET /v1/deliveries/{deliveryID}/attempts", s.listAttempts)
	s.mux.HandleFunc("POST /v1/deliveries/{deliveryID}/replay", s.replay)
}
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type endpointInput struct {
	URL string `json:"url"`
}

func (s *Server) createEndpoint(w http.ResponseWriter, r *http.Request) {
	var input endpointInput
	if err := decode(r, &input); err != nil {
		bad(w, err)
		return
	}
	parsed, err := url.ParseRequestURI(input.URL)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" {
		bad(w, errors.New("url must be an absolute http(s) URL"))
		return
	}
	secret, err := randomSecret()
	if err != nil {
		internal(w, err)
		return
	}
	endpoint := domain.Endpoint{ID: domain.NewID("we"), URL: input.URL, Secret: secret, Active: true, CreatedAt: time.Now().UTC()}
	if err = s.store.CreateEndpoint(r.Context(), endpoint); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, endpoint)
}
func (s *Server) listEndpoints(w http.ResponseWriter, r *http.Request) {
	endpoints, err := s.store.ListActiveEndpoints(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	for i := range endpoints {
		endpoints[i].Secret = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": endpoints})
}

type eventInput struct {
	Type              string `json:"type"`
	MerchantReference string `json:"merchant_reference"`
	AmountCents       int64  `json:"amount_cents"`
	Currency          string `json:"currency"`
	Status            string `json:"status"`
}

func (s *Server) createEvent(w http.ResponseWriter, r *http.Request) {
	var input eventInput
	if err := decode(r, &input); err != nil {
		bad(w, err)
		return
	}
	input.Type = strings.TrimSpace(input.Type)
	input.MerchantReference = strings.TrimSpace(input.MerchantReference)
	input.Currency = strings.ToUpper(strings.TrimSpace(input.Currency))
	input.Status = strings.TrimSpace(input.Status)
	if input.Type == "" || input.MerchantReference == "" || input.AmountCents <= 0 || len(input.Currency) != 3 || input.Status == "" {
		bad(w, errors.New("type, merchant_reference, positive amount_cents, three-letter currency, and status are required"))
		return
	}
	if !strings.HasPrefix(input.Type, "payment.") {
		bad(w, errors.New("event type must begin with payment."))
		return
	}
	idempotency := r.Header.Get("Idempotency-Key")
	event := domain.PaymentEvent{ID: domain.NewID("evt"), Type: input.Type, MerchantRef: input.MerchantReference, AmountCents: input.AmountCents, Currency: input.Currency, Status: input.Status, OccurredAt: time.Now().UTC(), IdempotencyKey: idempotency}
	fingerprint := fmt.Sprintf("%s|%s|%d|%s|%s", event.Type, event.MerchantRef, event.AmountCents, event.Currency, event.Status)
	endpoints, err := s.store.ListActiveEndpoints(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	saved, replayed, err := s.store.CreateEvent(r.Context(), event, fingerprint, endpoints)
	if errors.Is(err, store.ErrIdempotencyConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"event": saved, "idempotent_replay": replayed})
}
func (s *Server) getEvent(w http.ResponseWriter, r *http.Request) {
	event, err := s.store.GetEvent(r.Context(), r.PathValue("eventID"))
	if errors.Is(err, store.ErrNotFound) {
		notFound(w)
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, event)
}
func (s *Server) listDeliveries(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("eventID")
	if _, err := s.store.GetEvent(r.Context(), eventID); errors.Is(err, store.ErrNotFound) {
		notFound(w)
		return
	}
	deliveries, err := s.store.ListDeliveries(r.Context(), eventID)
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": deliveries})
}
func (s *Server) listAttempts(w http.ResponseWriter, r *http.Request) {
	attempts, err := s.store.ListAttempts(r.Context(), r.PathValue("deliveryID"))
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": attempts})
}
func (s *Server) replay(w http.ResponseWriter, r *http.Request) {
	err := s.store.ReplayFailed(r.Context(), r.PathValue("deliveryID"))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "delivery was not found or is not failed"})
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}
func decode(r *http.Request, dst any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func bad(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
}
func notFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
}
func internal(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
}
func randomSecret() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "whsec_" + hex.EncodeToString(bytes), nil
}
func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		s.logger.Info("request", "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(started).Milliseconds())
	})
}
