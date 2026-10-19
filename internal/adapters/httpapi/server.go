package httpapi

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/example/payrelay/internal/core/application"
	"github.com/example/payrelay/internal/core/domain"
	"github.com/example/payrelay/internal/core/ports"
)

//go:embed swagger/*
var swaggerFiles embed.FS

type Server struct {
	service *application.Service
	logger  *slog.Logger
	auth    TenantAuthenticator
	mux     *http.ServeMux
}

func NewServer(service *application.Service, logger *slog.Logger, auth TenantAuthenticator) http.Handler {
	s := &Server{service: service, logger: logger, auth: auth, mux: http.NewServeMux()}
	s.routes()
	return s.withLogging(s.mux)
}
func (s *Server) routes() {
	swaggerFS, err := fs.Sub(swaggerFiles, "swagger")
	if err != nil {
		panic(fmt.Sprintf("load embedded API documentation: %v", err))
	}
	s.mux.HandleFunc("GET /docs/openapi.yaml", s.openAPI)
	s.mux.Handle("GET /docs/", http.StripPrefix("/docs/", http.FileServerFS(swaggerFS)))
	s.mux.HandleFunc("GET /healthz", s.health)
	s.handleAuthenticated("POST /v1/endpoints", s.createEndpoint)
	s.handleAuthenticated("GET /v1/endpoints", s.listEndpoints)
	s.handleAuthenticated("POST /v1/events", s.createEvent)
	s.handleAuthenticated("GET /v1/events/{eventID}", s.getEvent)
	s.handleAuthenticated("GET /v1/events/{eventID}/deliveries", s.listDeliveries)
	s.handleAuthenticated("GET /v1/deliveries/{deliveryID}/attempts", s.listAttempts)
	s.handleAuthenticated("POST /v1/deliveries/{deliveryID}/replay", s.replay)
}

type tenantContextKey struct{}

func (s *Server) handleAuthenticated(pattern string, next http.HandlerFunc) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		tenantID, authenticated := s.auth.Authenticate(r.Header.Get("Authorization"))
		if !authenticated {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), tenantContextKey{}, tenantID)))
	})
}

func tenantID(ctx context.Context) string {
	value, _ := ctx.Value(tenantContextKey{}).(string)
	return value
}

func (s *Server) openAPI(w http.ResponseWriter, _ *http.Request) {
	contract, err := swaggerFiles.ReadFile("swagger/openapi.yaml")
	if err != nil {
		internal(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	_, _ = w.Write(contract)
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
	endpoint, err := s.service.CreateEndpoint(r.Context(), tenantID(r.Context()), input.URL)
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, endpoint)
}
func (s *Server) listEndpoints(w http.ResponseWriter, r *http.Request) {
	endpoints, err := s.service.ListActiveEndpoints(r.Context(), tenantID(r.Context()))
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
	saved, replayed, err := s.service.CreateEvent(r.Context(), tenantID(r.Context()), event)
	if errors.Is(err, ports.ErrIdempotencyConflict) {
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
	event, err := s.service.GetEvent(r.Context(), tenantID(r.Context()), r.PathValue("eventID"))
	if errors.Is(err, ports.ErrNotFound) {
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
	if _, err := s.service.GetEvent(r.Context(), tenantID(r.Context()), eventID); errors.Is(err, ports.ErrNotFound) {
		notFound(w)
		return
	}
	deliveries, err := s.service.ListDeliveries(r.Context(), tenantID(r.Context()), eventID)
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": deliveries})
}
func (s *Server) listAttempts(w http.ResponseWriter, r *http.Request) {
	attempts, err := s.service.ListAttempts(r.Context(), tenantID(r.Context()), r.PathValue("deliveryID"))
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": attempts})
}
func (s *Server) replay(w http.ResponseWriter, r *http.Request) {
	err := s.service.ReplayFailed(r.Context(), tenantID(r.Context()), r.PathValue("deliveryID"))
	if errors.Is(err, ports.ErrNotFound) {
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
func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		s.logger.Info("request", "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(started).Milliseconds())
	})
}
