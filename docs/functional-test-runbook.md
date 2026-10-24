# Functional test runbook

This guide demonstrates the complete mock-only PayRelay flow with real HTTP requests. It is intended for local verification, demos, and technical interviews. No step uses real payment credentials or card data.

## 1. Start PayRelay

In one Bash terminal, start the API. Setting one attempt makes the failed-delivery example deterministic. Configure a local tenant and API key; all management requests except `/healthz` require it.

```bash
PAYRELAY_MAX_ATTEMPTS=1 PAYRELAY_API_KEYS=merchant_demo:prk_local_demo_key_0123456789,merchant_other:prk_other_demo_key_0123456789 go run ./cmd/server
```

Verify the service in a second terminal:

```bash
curl -i http://localhost:8080/healthz
```

Expected response:

```http
HTTP/1.1 200 OK
Content-Type: application/json

{"status":"ok"}
```

## 1.1 Verify the per-tenant rate limit

For this optional check, stop the normal run and restart with a deliberately small quota of three management requests per tenant per minute. The response includes the quota headers; a fourth request with the same API key returns `429`, while another tenant remains independent.

```bash
PAYRELAY_MAX_ATTEMPTS=1 PAYRELAY_TENANT_RATE_LIMIT=3 PAYRELAY_API_KEYS=merchant_demo:prk_local_demo_key_0123456789,merchant_other:prk_other_demo_key_0123456789 go run ./cmd/server
```

```bash
export API_KEY="prk_local_demo_key_0123456789"
export OTHER_API_KEY="prk_other_demo_key_0123456789"
```

```bash
for request in 1 2 3 4; do
  curl -si http://localhost:8080/v1/endpoints -H "Authorization: Bearer ${API_KEY}" | head -n 1
done
curl -si http://localhost:8080/v1/endpoints -H "Authorization: Bearer ${OTHER_API_KEY}" | head -n 1
```

Expected status lines:

```text
HTTP/1.1 200 OK
HTTP/1.1 200 OK
HTTP/1.1 200 OK
HTTP/1.1 429 Too Many Requests
HTTP/1.1 200 OK
```

The `429` response includes `Retry-After`, `X-RateLimit-Limit`, `X-RateLimit-Remaining`, and `X-RateLimit-Reset`. Stop this small-quota server and restart it with the normal command in section 1 before continuing the runbook.

## 2. Register a test receiver

Set `RECEIVER_URL` to a **publicly routable** HTTP request inspector or test receiver that returns `2xx`. Localhost, private-network addresses, cloud metadata addresses, and hostnames that resolve to any of them are rejected. Never paste an endpoint secret into a public request inspector's notes.

```bash
export RECEIVER_URL="https://your-test-receiver.example/webhooks/payrelay"
curl -i -X POST http://localhost:8080/v1/endpoints \
  -H "Authorization: Bearer ${API_KEY}" \
  -H 'Content-Type: application/json' \
  --data "{\"url\":\"${RECEIVER_URL}\"}"
```

Expected response (`201 Created`; values vary):

```json
{
  "id": "we_84fa...",
  "url": "https://your-test-receiver.example/webhooks/payrelay",
  "secret": "whsec_7bf...",
  "active": true,
  "created_at": "2026-10-18T...Z"
}
```

Save the returned secret securely. PayRelay returns it only at endpoint creation; endpoint listings intentionally omit it.

### Verify the SSRF guard

The registration boundary rejects unsafe targets before it stores an endpoint or performs an outbound request:

```bash
curl -i -X POST http://localhost:8080/v1/endpoints \
  -H "Authorization: Bearer ${API_KEY}" \
  -H 'Content-Type: application/json' \
  --data '{"url":"http://127.0.0.1:8080/internal"}'
```

Expected response:

```http
HTTP/1.1 400 Bad Request
Content-Type: application/json

{"error":"url must be an absolute http(s) URL with a publicly routable target"}
```

The same policy rejects `localhost`, RFC1918/private addresses, link-local addresses such as `169.254.169.254`, IPv6 loopback, URL credentials, and hostnames with any non-public DNS result. Deliveries resolve and validate the hostname again immediately before connecting and do not follow HTTP redirects.

## 3. Verify endpoint lifecycle and secret rotation

Set `ENDPOINT_ID` and `FIRST_SECRET` from the response in step 2. The following requests are safe to run against the test endpoint; no secret is sent to the receiver.

First, verify that a normal retrieval redacts the secret:

```bash
export ENDPOINT_ID="we_84fa..."
export FIRST_SECRET="whsec_7bf..."
curl -s -H "Authorization: Bearer ${API_KEY}" \
  "http://localhost:8080/v1/endpoints/${ENDPOINT_ID}"
```

Expected response: `200 OK`, with the endpoint's `id`, `url`, and `active` state, but **without** a `secret` field. `GET /v1/endpoints` provides the same redaction guarantee while including both active and disabled endpoints.

Disable the endpoint, then confirm the update:

```bash
curl -i -X PATCH "http://localhost:8080/v1/endpoints/${ENDPOINT_ID}" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H 'Content-Type: application/json' \
  --data '{"active":false}'
```

Expected response (`200 OK`):

```json
{"id":"we_84fa...","active":false}
```

The collection endpoint keeps disabled records visible while continuing to redact secrets:

```bash
curl -s -H "Authorization: Bearer ${API_KEY}" http://localhost:8080/v1/endpoints
```

Expected result: `data` contains `ENDPOINT_ID` with `"active":false` and no `secret` field.

Create an event while the endpoint is disabled, set `DISABLED_EVENT_ID` from the response, and inspect its deliveries:

```bash
curl -s -X POST http://localhost:8080/v1/events \
  -H "Authorization: Bearer ${API_KEY}" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: disabled-endpoint-check-1008' \
  --data '{"type":"payment.succeeded","merchant_reference":"disabled-endpoint-check-1008","amount_cents":2599,"currency":"USD","status":"succeeded"}'

export DISABLED_EVENT_ID="evt_..."
curl -s -H "Authorization: Bearer ${API_KEY}" \
  "http://localhost:8080/v1/events/${DISABLED_EVENT_ID}/deliveries"
```

Expected result: `{"data":[]}`. Re-enable the endpoint before continuing the normal delivery flow:

```bash
curl -i -X PATCH "http://localhost:8080/v1/endpoints/${ENDPOINT_ID}" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H 'Content-Type: application/json' \
  --data '{"active":true}'
```

Rotate the signing secret. The response contains a new `whsec_...` value that must differ from `FIRST_SECRET`; save it as the receiver's new secret. A subsequent `GET /v1/endpoints/${ENDPOINT_ID}` must again omit the `secret` field.

```bash
curl -i -X POST "http://localhost:8080/v1/endpoints/${ENDPOINT_ID}/rotate-secret" \
  -H "Authorization: Bearer ${API_KEY}"
```

Finally, prove tenant isolation. A valid API key for another tenant must not reveal or modify this endpoint:

```bash
curl -i -H "Authorization: Bearer ${OTHER_API_KEY}" \
  "http://localhost:8080/v1/endpoints/${ENDPOINT_ID}"
```

Expected response: `404 Not Found` with `{"error":"not found"}`.

## 4. Create a fictional payment event

The `Idempotency-Key` makes client retries safe. The example contains only fictional order data and an amount in minor units.

```bash
curl -i -X POST http://localhost:8080/v1/events \
  -H "Authorization: Bearer ${API_KEY}" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: demo-order-1007' \
  --data '{
    "type":"payment.succeeded",
    "merchant_reference":"demo-order-1007",
    "amount_cents":2599,
    "currency":"USD",
    "status":"succeeded"
  }'
```

Expected response (`201 Created`):

```json
{
  "event": {
    "id": "evt_2db...",
    "type": "payment.succeeded",
    "merchant_reference": "demo-order-1007",
    "amount_cents": 2599,
    "currency": "USD",
    "status": "succeeded",
    "occurred_at": "2026-10-18T...Z"
  },
  "idempotent_replay": false
}
```

Repeat the **identical** request with the same idempotency key. The service returns `200 OK`, the original event ID, and `"idempotent_replay": true`. Change any payload field while reusing that key and it returns `409 Conflict`.

## 5. Inspect the outbound webhook and delivery history

The receiver receives a signed POST similar to this:

```http
POST /webhooks/payrelay HTTP/1.1
Content-Type: application/json
User-Agent: PayRelay/1.0 (mock events only)
X-PayRelay-Event-ID: evt_2db...
X-PayRelay-Timestamp: 1792368000
X-PayRelay-Signature: sha256=4d1...

{"id":"evt_2db...","type":"payment.succeeded","data":{"id":"evt_2db...","type":"payment.succeeded","merchant_reference":"demo-order-1007","amount_cents":2599,"currency":"USD","status":"succeeded","occurred_at":"2026-10-18T...Z"},"sent_at":"2026-10-18T...Z"}
```

Use the event ID returned in step 4:

```bash
curl -s -H "Authorization: Bearer ${API_KEY}" http://localhost:8080/v1/events/evt_2db.../deliveries
```

Expected result after a `2xx` receiver response:

```json
{
  "data": [{
    "id": "dlv_a91...",
    "event_id": "evt_2db...",
    "status": "succeeded",
    "attempt_count": 1
  }]
}
```

Then inspect the immutable attempt record:

```bash
curl -s -H "Authorization: Bearer ${API_KEY}" http://localhost:8080/v1/deliveries/dlv_a91.../attempts
```

The result contains an attempt with the receiver's HTTP status code and duration in milliseconds.

## 6. Verify a signature at the receiver

The signature scheme is `HMAC_SHA256("timestamp.raw_body", endpoint_secret)`, prefixed with `sha256=`. Verify the raw body before JSON parsing and reject stale timestamps. In pseudocode:

```text
expected = "sha256=" + hex(HMAC-SHA256(secret, timestamp + "." + rawBody))
accept only if constantTimeEqual(expected, X-PayRelay-Signature)
```

`internal/adapters/webhook/signature_test.go` provides an executable Go example of both successful verification and tamper rejection.

## 7. Demonstrate a failed delivery and replay

Register a second test receiver that deliberately returns `500`, then create another mock event. With `PAYRELAY_MAX_ATTEMPTS=1`, inspect the delivery until it reports:

```json
{
  "status": "failed",
  "attempt_count": 1,
  "last_error": "receiver returned HTTP 500"
}
```

Queue it again after fixing the receiver:

```bash
curl -i -X POST http://localhost:8080/v1/deliveries/dlv_failed.../replay \
  -H "Authorization: Bearer ${API_KEY}"
```

Expected response:

```http
HTTP/1.1 202 Accepted
Content-Type: application/json

{"status":"queued"}
```

When retries are enabled (the default is five attempts), failures are retried with capped exponential backoff plus jitter. Receiver applications should still deduplicate by `X-PayRelay-Event-ID`, because webhook delivery is intentionally at-least-once.
