package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/example/payrelay/internal/delivery"
	"github.com/example/payrelay/internal/domain"
	"github.com/example/payrelay/internal/store"
)

func TestAPI_GivenRegisteredEndpoint_WhenEventIsCreatedAndDispatched_ThenPersistsDeliveryHistory(t *testing.T) {
	var receivedEventID string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedEventID = r.Header.Get("X-PayRelay-Event-ID")
		if r.Header.Get("X-PayRelay-Signature") == "" {
			t.Error("webhook did not include signature")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()

	handler, dispatcher := testServer(t)
	endpoint := request(t, handler, http.MethodPost, "/v1/endpoints", `{"url":"`+receiver.URL+`"}`, nil)
	if endpoint.Code != http.StatusCreated {
		t.Fatalf("create endpoint: got %d: %s", endpoint.Code, endpoint.Body.String())
	}
	var createdEndpoint domain.Endpoint
	decodeJSON(t, endpoint.Body.Bytes(), &createdEndpoint)
	if createdEndpoint.ID == "" || createdEndpoint.Secret == "" {
		t.Fatalf("endpoint is missing generated credentials: %#v", createdEndpoint)
	}

	listed := request(t, handler, http.MethodGet, "/v1/endpoints", "", nil)
	if listed.Code != http.StatusOK || bytes.Contains(listed.Body.Bytes(), []byte(createdEndpoint.Secret)) {
		t.Fatalf("endpoint listing leaked secret or failed: %d %s", listed.Code, listed.Body.String())
	}

	payload := `{"type":"payment.succeeded","merchant_reference":"order_1007","amount_cents":2599,"currency":"usd","status":"succeeded"}`
	headers := map[string]string{"Idempotency-Key": "order_1007"}
	created := request(t, handler, http.MethodPost, "/v1/events", payload, headers)
	if created.Code != http.StatusCreated {
		t.Fatalf("create event: got %d: %s", created.Code, created.Body.String())
	}
	var eventResult struct {
		Event  domain.PaymentEvent `json:"event"`
		Replay bool                `json:"idempotent_replay"`
	}
	decodeJSON(t, created.Body.Bytes(), &eventResult)
	if eventResult.Event.Currency != "USD" || eventResult.Replay {
		t.Fatalf("unexpected event response: %#v", eventResult)
	}

	replayed := request(t, handler, http.MethodPost, "/v1/events", payload, headers)
	if replayed.Code != http.StatusOK {
		t.Fatalf("idempotent replay: got %d: %s", replayed.Code, replayed.Body.String())
	}
	decodeJSON(t, replayed.Body.Bytes(), &eventResult)
	if !eventResult.Replay {
		t.Fatal("expected idempotent replay marker")
	}

	conflict := request(t, handler, http.MethodPost, "/v1/events", `{"type":"payment.succeeded","merchant_reference":"order_1007","amount_cents":2600,"currency":"USD","status":"succeeded"}`, headers)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("different idempotent request: got %d: %s", conflict.Code, conflict.Body.String())
	}

	gotEvent := request(t, handler, http.MethodGet, "/v1/events/"+eventResult.Event.ID, "", nil)
	if gotEvent.Code != http.StatusOK {
		t.Fatalf("get event: got %d", gotEvent.Code)
	}
	deliveries := request(t, handler, http.MethodGet, "/v1/events/"+eventResult.Event.ID+"/deliveries", "", nil)
	var deliveryResult struct {
		Data []domain.Delivery `json:"data"`
	}
	decodeJSON(t, deliveries.Body.Bytes(), &deliveryResult)
	if deliveries.Code != http.StatusOK || len(deliveryResult.Data) != 1 {
		t.Fatalf("deliveries: got %d %#v", deliveries.Code, deliveryResult)
	}

	found, err := dispatcher.DispatchOne(context.Background())
	if err != nil || !found || receivedEventID != eventResult.Event.ID {
		t.Fatalf("dispatch result: found=%v err=%v received=%q", found, err, receivedEventID)
	}
	attempts := request(t, handler, http.MethodGet, "/v1/deliveries/"+deliveryResult.Data[0].ID+"/attempts", "", nil)
	var attemptResult struct {
		Data []domain.DeliveryAttempt `json:"data"`
	}
	decodeJSON(t, attempts.Body.Bytes(), &attemptResult)
	if attempts.Code != http.StatusOK || len(attemptResult.Data) != 1 || attemptResult.Data[0].StatusCode != http.StatusNoContent {
		t.Fatalf("attempts: got %d %#v", attempts.Code, attemptResult)
	}

	replaySucceeded := request(t, handler, http.MethodPost, "/v1/deliveries/"+deliveryResult.Data[0].ID+"/replay", "", nil)
	if replaySucceeded.Code != http.StatusConflict {
		t.Fatalf("replay succeeded delivery: got %d", replaySucceeded.Code)
	}
}

func TestAPI_GivenFailedDelivery_WhenReplayed_ThenQueuesDeliveryAgain(t *testing.T) {
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer receiver.Close()
	handler, dispatcher := testServer(t)
	create := request(t, handler, http.MethodPost, "/v1/endpoints", `{"url":"`+receiver.URL+`"}`, nil)
	if create.Code != http.StatusCreated {
		t.Fatal(create.Body.String())
	}
	created := request(t, handler, http.MethodPost, "/v1/events", `{"type":"payment.failed","merchant_reference":"order_1008","amount_cents":500,"currency":"BRL","status":"failed"}`, nil)
	var eventResult struct {
		Event domain.PaymentEvent `json:"event"`
	}
	decodeJSON(t, created.Body.Bytes(), &eventResult)
	if _, err := dispatcher.DispatchOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	deliveries := request(t, handler, http.MethodGet, "/v1/events/"+eventResult.Event.ID+"/deliveries", "", nil)
	var result struct {
		Data []domain.Delivery `json:"data"`
	}
	decodeJSON(t, deliveries.Body.Bytes(), &result)
	if len(result.Data) != 1 || result.Data[0].Status != "failed" || result.Data[0].LastError == "" {
		t.Fatalf("expected failed delivery: %#v", result)
	}
	replayed := request(t, handler, http.MethodPost, "/v1/deliveries/"+result.Data[0].ID+"/replay", "", nil)
	if replayed.Code != http.StatusAccepted {
		t.Fatalf("replay: got %d: %s", replayed.Code, replayed.Body.String())
	}
}

func TestAPI_GivenHealthCheck_WhenRequested_ThenReturnsOK(t *testing.T) {
	handler, _ := testServer(t)
	if response := request(t, handler, http.MethodGet, "/healthz", "", nil); response.Code != http.StatusOK {
		t.Fatalf("health: got %d", response.Code)
	}
}

func TestAPI_GivenInvalidRequest_WhenHandled_ThenReturnsExpectedClientError(t *testing.T) {
	handler, _ := testServer(t)
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{name: "given_file_endpoint_url_when_creating_endpoint_then_returns_bad_request", method: http.MethodPost, path: "/v1/endpoints", body: `{"url":"file:///tmp/receiver"}`, want: http.StatusBadRequest},
		{name: "given_empty_endpoint_url_when_creating_endpoint_then_returns_bad_request", method: http.MethodPost, path: "/v1/endpoints", body: `{"url":""}`, want: http.StatusBadRequest},
		{name: "given_wrong_endpoint_url_type_when_creating_endpoint_then_returns_bad_request", method: http.MethodPost, path: "/v1/endpoints", body: `{"url":1}`, want: http.StatusBadRequest},
		{name: "given_missing_event_fields_when_creating_event_then_returns_bad_request", method: http.MethodPost, path: "/v1/events", body: `{}`, want: http.StatusBadRequest},
		{name: "given_non_payment_event_type_when_creating_event_then_returns_bad_request", method: http.MethodPost, path: "/v1/events", body: `{"type":"invoice.created","merchant_reference":"o","amount_cents":1,"currency":"USD","status":"created"}`, want: http.StatusBadRequest},
		{name: "given_non_positive_amount_when_creating_event_then_returns_bad_request", method: http.MethodPost, path: "/v1/events", body: `{"type":"payment.succeeded","merchant_reference":"o","amount_cents":0,"currency":"USD","status":"succeeded"}`, want: http.StatusBadRequest},
		{name: "given_invalid_currency_when_creating_event_then_returns_bad_request", method: http.MethodPost, path: "/v1/events", body: `{"type":"payment.succeeded","merchant_reference":"o","amount_cents":1,"currency":"US","status":"succeeded"}`, want: http.StatusBadRequest},
		{name: "given_unknown_event_field_when_creating_event_then_returns_bad_request", method: http.MethodPost, path: "/v1/events", body: `{"type":"payment.succeeded","merchant_reference":"o","amount_cents":1,"currency":"USD","status":"succeeded","unexpected":true}`, want: http.StatusBadRequest},
		{name: "given_missing_event_when_reading_event_then_returns_not_found", method: http.MethodGet, path: "/v1/events/evt_missing", want: http.StatusNotFound},
		{name: "given_missing_event_when_listing_deliveries_then_returns_not_found", method: http.MethodGet, path: "/v1/events/evt_missing/deliveries", want: http.StatusNotFound},
		{name: "given_missing_delivery_when_replaying_then_returns_conflict", method: http.MethodPost, path: "/v1/deliveries/dlv_missing/replay", want: http.StatusConflict},
		{name: "given_missing_delivery_when_listing_attempts_then_returns_empty_result", method: http.MethodGet, path: "/v1/deliveries/dlv_missing/attempts", want: http.StatusOK},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			response := request(t, handler, tt.method, tt.path, tt.body, nil)
			if response.Code != tt.want {
				t.Fatalf("got %d, want %d: %s", response.Code, tt.want, response.Body.String())
			}
		})
	}
}

func testServer(t *testing.T) (http.Handler, *delivery.Dispatcher) {
	t.Helper()
	repo, err := store.Open(filepath.Join(t.TempDir(), "payrelay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dispatcher := delivery.NewDispatcher(repo, http.DefaultClient, 1, logger)
	return NewServer(repo, dispatcher, logger), dispatcher
}

func request(t *testing.T, handler http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func decodeJSON(t *testing.T, body []byte, output any) {
	t.Helper()
	if err := json.Unmarshal(body, output); err != nil {
		t.Fatalf("decode JSON %s: %v", body, err)
	}
}
