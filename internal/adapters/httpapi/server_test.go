package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/payrelay/internal/adapters/sqlite"
	"github.com/example/payrelay/internal/adapters/webhook"
	"github.com/example/payrelay/internal/core/application"
	"github.com/example/payrelay/internal/core/domain"
	"github.com/example/payrelay/internal/targetpolicy"
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

func TestAPI_GivenEndpoint_WhenDisabledAndRotated_ThenUpdatesLifecycleWithoutLeakingSecrets(t *testing.T) {
	handler, _ := testServer(t)
	created := request(t, handler, http.MethodPost, "/v1/endpoints", `{"url":"https://receiver.example.test/webhooks"}`, nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create endpoint: %d %s", created.Code, created.Body.String())
	}
	var endpoint domain.Endpoint
	decodeJSON(t, created.Body.Bytes(), &endpoint)
	got := request(t, handler, http.MethodGet, "/v1/endpoints/"+endpoint.ID, "", nil)
	if got.Code != http.StatusOK || bytes.Contains(got.Body.Bytes(), []byte(endpoint.Secret)) {
		t.Fatalf("get endpoint: %d %s", got.Code, got.Body.String())
	}

	disabled := request(t, handler, http.MethodPatch, "/v1/endpoints/"+endpoint.ID, `{"active":false}`, nil)
	if disabled.Code != http.StatusOK || !strings.Contains(disabled.Body.String(), `"active":false`) || bytes.Contains(disabled.Body.Bytes(), []byte(endpoint.Secret)) {
		t.Fatalf("disable endpoint: %d %s", disabled.Code, disabled.Body.String())
	}
	listed := request(t, handler, http.MethodGet, "/v1/endpoints", "", nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"active":false`) {
		t.Fatalf("list endpoints: %d %s", listed.Code, listed.Body.String())
	}

	rotated := request(t, handler, http.MethodPost, "/v1/endpoints/"+endpoint.ID+"/rotate-secret", "", nil)
	var rotatedEndpoint domain.Endpoint
	decodeJSON(t, rotated.Body.Bytes(), &rotatedEndpoint)
	if rotated.Code != http.StatusOK || rotatedEndpoint.Secret == "" || rotatedEndpoint.Secret == endpoint.Secret || rotatedEndpoint.Active {
		t.Fatalf("rotate endpoint: %d %#v", rotated.Code, rotatedEndpoint)
	}

	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "Given unknown endpoint When updating Then it returns not found", path: "/v1/endpoints/we_missing", body: `{"active":true}`},
		{name: "Given missing active field When updating Then it returns bad request", path: "/v1/endpoints/" + endpoint.ID, body: `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := request(t, handler, http.MethodPatch, tt.path, tt.body, nil)
			want := http.StatusNotFound
			if tt.body == `{}` {
				want = http.StatusBadRequest
			}
			if response.Code != want {
				t.Fatalf("status = %d, want %d: %s", response.Code, want, response.Body.String())
			}
		})
	}
}

func TestAPI_GivenHealthCheck_WhenRequested_ThenReturnsOK(t *testing.T) {
	handler, _ := testServer(t)
	if response := request(t, handler, http.MethodGet, "/healthz", "", nil); response.Code != http.StatusOK {
		t.Fatalf("health: got %d", response.Code)
	}
}

func TestAPI_GivenMissingOrInvalidCredentials_WhenManagingResources_ThenRejectsRequest(t *testing.T) {
	handler, _ := testServer(t)

	tests := []struct {
		name          string
		path          string
		authorization string
		want          int
	}{
		{name: "Given no Bearer token When listing endpoints Then it returns unauthorized", path: "/v1/endpoints", want: http.StatusUnauthorized},
		{name: "Given invalid Bearer token When listing endpoints Then it returns unauthorized", path: "/v1/endpoints", authorization: "Bearer prk_invalid_key_value_which_is_long_enough", want: http.StatusUnauthorized},
		{name: "Given no credential When requesting health Then it remains public", path: "/healthz", want: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if tt.authorization != "" {
				req.Header.Set("Authorization", tt.authorization)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, tt.want, response.Body.String())
			}
		})
	}
}

func TestAPI_GivenTwoTenants_WhenResourcesAreRequestedAcrossBoundary_ThenTheyAreHidden(t *testing.T) {
	handler, _ := testServer(t)
	created := requestAs(t, handler, "prk_alpha_0123456789abcdef0123456789", http.MethodPost, "/v1/endpoints", `{"url":"https://alpha.example.test/webhooks"}`, nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create endpoint: %d %s", created.Code, created.Body.String())
	}
	var endpoint domain.Endpoint
	decodeJSON(t, created.Body.Bytes(), &endpoint)

	otherList := requestAs(t, handler, "prk_beta_0123456789abcdef01234567890", http.MethodGet, "/v1/endpoints", "", nil)
	var listed struct {
		Data []domain.Endpoint `json:"data"`
	}
	decodeJSON(t, otherList.Body.Bytes(), &listed)
	if otherList.Code != http.StatusOK || len(listed.Data) != 0 {
		t.Fatalf("tenant beta saw tenant alpha endpoints: %d %#v", otherList.Code, listed)
	}

	payload := `{"type":"payment.succeeded","merchant_reference":"tenant-boundary","amount_cents":100,"currency":"USD","status":"succeeded"}`
	createdEvent := requestAs(t, handler, "prk_alpha_0123456789abcdef0123456789", http.MethodPost, "/v1/events", payload, map[string]string{"Idempotency-Key": "shared-client-key"})
	if createdEvent.Code != http.StatusCreated {
		t.Fatalf("create event: %d %s", createdEvent.Code, createdEvent.Body.String())
	}
	var eventResult struct {
		Event domain.PaymentEvent `json:"event"`
	}
	decodeJSON(t, createdEvent.Body.Bytes(), &eventResult)
	betaEvent := requestAs(t, handler, "prk_beta_0123456789abcdef01234567890", http.MethodPost, "/v1/events", payload, map[string]string{"Idempotency-Key": "shared-client-key"})
	if betaEvent.Code != http.StatusCreated {
		t.Fatalf("tenant beta event: %d %s", betaEvent.Code, betaEvent.Body.String())
	}
	otherEvent := requestAs(t, handler, "prk_beta_0123456789abcdef01234567890", http.MethodGet, "/v1/events/"+eventResult.Event.ID, "", nil)
	if otherEvent.Code != http.StatusNotFound {
		t.Fatalf("tenant beta lookup: %d %s", otherEvent.Code, otherEvent.Body.String())
	}
}

func TestAPI_GivenDocumentation_WhenRequested_ThenServesSwaggerUIAndOpenAPIContract(t *testing.T) {
	handler, _ := testServer(t)

	tests := []struct {
		name        string
		path        string
		contentType string
		contains    string
	}{
		{
			name:        "Given documentation route When Swagger UI is requested Then it returns HTML",
			path:        "/docs/",
			contentType: "text/html",
			contains:    "swagger-ui",
		},
		{
			name:        "Given documentation route When OpenAPI contract is requested Then it returns YAML",
			path:        "/docs/openapi.yaml",
			contentType: "application/yaml",
			contains:    "openapi: 3.0.3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := request(t, handler, http.MethodGet, tt.path, "", nil)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}
			if !strings.Contains(response.Header().Get("Content-Type"), tt.contentType) {
				t.Fatalf("content type = %q, want it to contain %q", response.Header().Get("Content-Type"), tt.contentType)
			}
			if !strings.Contains(response.Body.String(), tt.contains) {
				t.Fatalf("body = %q, want it to contain %q", response.Body.String(), tt.contains)
			}
		})
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

func TestAPI_GivenUnsafeEndpointTarget_WhenCreatingEndpoint_ThenReturnsBadRequest(t *testing.T) {
	handler, _ := testServerWithTargetValidator(t, nil)
	cases := []struct {
		name string
		body string
	}{
		{name: "given loopback target when creating endpoint then it returns bad request", body: `{"url":"http://127.0.0.1:8080/hooks"}`},
		{name: "given cloud metadata target when creating endpoint then it returns bad request", body: `{"url":"http://169.254.169.254/latest/meta-data"}`},
		{name: "given localhost target when creating endpoint then it returns bad request", body: `{"url":"http://localhost:8080/hooks"}`},
		{name: "given credentialed target when creating endpoint then it returns bad request", body: `{"url":"https://user:pass@localhost/hooks"}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			response := request(t, handler, http.MethodPost, "/v1/endpoints", tt.body, nil)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusBadRequest, response.Body.String())
			}
		})
	}
}

func testServer(t *testing.T) (http.Handler, *application.Service) {
	return testServerWithTargetValidator(t, allowEndpointTargetValidator{})
}

func testServerWithTargetValidator(t *testing.T, targets EndpointTargetValidator) (http.Handler, *application.Service) {
	t.Helper()
	repo, err := sqlite.Open(filepath.Join(t.TempDir(), "payrelay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := application.NewService(repo, webhook.NewHTTPSender(http.DefaultClient), 1)
	authenticator, err := NewAPIKeyAuthenticator("merchant_alpha:prk_alpha_0123456789abcdef0123456789,merchant_beta:prk_beta_0123456789abcdef01234567890")
	if err != nil {
		t.Fatal(err)
	}
	if targets == nil {
		return NewServer(service, logger, authenticator), service
	}
	return NewServerWithTargetValidator(service, logger, authenticator, targets), service
}

type allowEndpointTargetValidator struct{}

func (allowEndpointTargetValidator) Validate(_ context.Context, rawURL string) error {
	target, err := url.ParseRequestURI(rawURL)
	if err != nil || target.Scheme != "http" && target.Scheme != "https" || target.Host == "" {
		return targetpolicy.ErrUnsafeTarget
	}
	return nil
}

func request(t *testing.T, handler http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer prk_alpha_0123456789abcdef0123456789")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func requestAs(t *testing.T, handler http.Handler, apiKey, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	requestHeaders := map[string]string{"Authorization": "Bearer " + apiKey}
	for key, value := range headers {
		requestHeaders[key] = value
	}
	return request(t, handler, method, path, body, requestHeaders)
}

func decodeJSON(t *testing.T, body []byte, output any) {
	t.Helper()
	if err := json.Unmarshal(body, output); err != nil {
		t.Fatalf("decode JSON %s: %v", body, err)
	}
}
