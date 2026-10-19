# Functional test runbook

This guide demonstrates the complete mock-only PayRelay flow with real HTTP requests. It is intended for local verification, demos, and technical interviews. No step uses real payment credentials or card data.

## 1. Start PayRelay

In one terminal, start the API. Setting one attempt makes the failed-delivery example deterministic. Configure a local tenant and API key; all management requests except `/healthz` require it.

```sh
PAYRELAY_MAX_ATTEMPTS=1 PAYRELAY_API_KEYS=merchant_demo:prk_local_demo_key_0123456789 go run ./cmd/server
```

On PowerShell:

```powershell
$env:PAYRELAY_MAX_ATTEMPTS = "1"
$env:PAYRELAY_API_KEYS = "merchant_demo:prk_local_demo_key_0123456789"
go run ./cmd/server
```

Verify the service in a second terminal:

```sh
curl -i http://localhost:8080/healthz
```

Expected response:

```http
HTTP/1.1 200 OK
Content-Type: application/json

{"status":"ok"}
```

## 2. Register a test receiver

Set `RECEIVER_URL` to an HTTP request inspector or a small local receiver that returns `2xx`. Never paste an endpoint secret into a public request inspector's notes.

```sh
export RECEIVER_URL="https://your-test-receiver.example/webhooks/payrelay"
export API_KEY="prk_local_demo_key_0123456789"
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

## 3. Create a fictional payment event

The `Idempotency-Key` makes client retries safe. The example contains only fictional order data and an amount in minor units.

```sh
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

## 4. Inspect the outbound webhook and delivery history

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

Use the event ID returned in step 3:

```sh
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

```sh
curl -s -H "Authorization: Bearer ${API_KEY}" http://localhost:8080/v1/deliveries/dlv_a91.../attempts
```

The result contains an attempt with the receiver's HTTP status code and duration in milliseconds.

## 5. Verify a signature at the receiver

The signature scheme is `HMAC_SHA256("timestamp.raw_body", endpoint_secret)`, prefixed with `sha256=`. Verify the raw body before JSON parsing and reject stale timestamps. In pseudocode:

```text
expected = "sha256=" + hex(HMAC-SHA256(secret, timestamp + "." + rawBody))
accept only if constantTimeEqual(expected, X-PayRelay-Signature)
```

`internal/adapters/webhook/signature_test.go` provides an executable Go example of both successful verification and tamper rejection.

## 6. Demonstrate a failed delivery and replay

Register a second test receiver that deliberately returns `500`, then create another mock event. With `PAYRELAY_MAX_ATTEMPTS=1`, inspect the delivery until it reports:

```json
{
  "status": "failed",
  "attempt_count": 1,
  "last_error": "receiver returned HTTP 500"
}
```

Queue it again after fixing the receiver:

```sh
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
