# PayRelay

PayRelay is a portfolio-quality **fictional payment-event webhook delivery service**. It creates mock payment events and sends signed webhook notifications to merchant endpoints. It is deliberately not a payment processor: it cannot charge a card, integrate with payment providers, or accept/store card data.

## What Phase 1 delivers

- Create merchant webhook endpoints, each with a generated signing secret.
- Create mock `payment.*` events and persist them in SQLite.
- Idempotent event creation via `Idempotency-Key`.
- Durable per-endpoint deliveries and delivery-attempt history.
- Async HTTP dispatch with HMAC-SHA256 request signing.
- Retry of non-2xx responses and network failures with capped exponential backoff plus jitter.
- Replay failed deliveries from the API.
- Docker, Docker Compose, CI, and focused tests for signing, retry policy, and dispatch behavior.

## Safety boundary and threat model

PayRelay is a local/demo platform for synthetic events only.

- Inputs represent fictional payment state (`amount_cents`, status, and a merchant reference), never PANs, CVVs, bank credentials, or real transaction tokens.
- It makes outbound HTTP calls only to the endpoint URL registered by the caller. Treat this as a development service: deploy behind egress controls and an allowlist to mitigate SSRF before any shared environment use.
- Endpoint secrets are stored in SQLite in this MVP so the worker can sign deliveries. Creation is the only API response that reveals a secret; use a managed secret store and encryption at rest in a production evolution.
- HTTPS is supported but not required locally. Production should require HTTPS, authenticate the management API, redact logs, and rotate secrets.
- Receivers must validate the timestamp and HMAC in constant time, and reject stale requests to limit replay exposure.

## Architecture

```text
REST API → SQLite (events, endpoints, deliveries, attempts)
                    ↓
               delivery worker → HMAC-signed HTTP POST → merchant endpoint
                    ↑                    │
           retry queue (backoff+jitter)  └─ 2xx succeeds; errors are recorded/retried
```

The API writes an event and its initial endpoint delivery jobs in one transaction. A background worker atomically claims due jobs, posts a signed JSON envelope, and persists every outcome. SQLite WAL mode keeps the MVP portable and locally runnable.

## Quick start

Prerequisites: Go 1.23+ (or Docker). Copy the example configuration if you want to customize it:

```sh
cp .env.example .env
go mod download
go run ./cmd/server
```

The API listens on `http://localhost:8080` by default. To run in Docker:

```sh
docker compose up --build
```

Useful development commands:

```sh
make test     # go test ./...
make lint     # go vet ./...
make build    # compile the service
make run      # start locally
```

## API examples

Register an endpoint (use a request-bin service or a local listener in development):

```sh
curl -X POST http://localhost:8080/v1/endpoints \
  -H 'Content-Type: application/json' \
  -d '{"url":"http://host.docker.internal:9000/webhooks/payrelay"}'
```

The response includes the `whsec_...` secret once. Create a **mock** event:

```sh
curl -X POST http://localhost:8080/v1/events \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: demo-order-1007' \
  -d '{"type":"payment.succeeded","merchant_reference":"demo-order-1007","amount_cents":2599,"currency":"USD","status":"succeeded"}'
```

Repeat the same request and key to safely receive the original event. Reusing the key for different event content returns `409 Conflict`.

Inspect delivery jobs and attempts:

```sh
curl http://localhost:8080/v1/events/evt_example/deliveries
curl http://localhost:8080/v1/deliveries/dlv_example/attempts
curl -X POST http://localhost:8080/v1/deliveries/dlv_example/replay
```

Every webhook is a JSON envelope and includes:

```text
X-PayRelay-Event-ID: evt_...
X-PayRelay-Timestamp: 1735689600
X-PayRelay-Signature: sha256=<hex HMAC of "timestamp.payload">
```

## Engineering trade-offs

- **SQLite over a broker/queue:** one executable, simple local setup, durable enough for a single-instance MVP. It is not a multi-node work queue.
- **At-least-once delivery:** a receiver can observe a request even when the sender cannot persist the response. Deduplicate by event ID on the receiver side.
- **Fixed retry policy:** failures retry up to `PAYRELAY_MAX_ATTEMPTS` (default five). `Retry-After`, dead-letter routing, endpoint pause/circuit breaking, and per-tenant limits are deliberately deferred.
- **No endpoint authentication model:** this is intentionally a local management API. An operator API or tenant auth is a necessary next step.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `PAYRELAY_ADDR` | `:8080` | HTTP listen address |
| `PAYRELAY_DB_PATH` | `./payrelay.db` | SQLite database path |
| `PAYRELAY_WORKER_POLL` | `500ms` | How often the worker looks for due deliveries |
| `PAYRELAY_MAX_ATTEMPTS` | `5` | Total delivery attempts before a delivery fails |
| `PAYRELAY_REQUEST_TIMEOUT` | `10s` | Outbound webhook HTTP timeout |

## Roadmap

1. Add API authentication, merchant tenancy, endpoint updates, secret rotation, and SSRF-safe endpoint policies.
2. Move secrets to a KMS-backed store; require TLS and add structured audit logs/metrics/tracing.
3. Introduce Postgres plus a dedicated queue for horizontal workers, DLQs, rate limiting, and endpoint circuit breaking.
4. Add an event catalog, receiver verification SDKs, an OpenAPI spec, and load/fault-injection tests.
