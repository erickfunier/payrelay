# PayRelay

PayRelay is a portfolio-quality **fictional payment-event webhook delivery service**. It creates mock payment events and sends signed webhook notifications to merchant endpoints. It is deliberately not a payment processor: it cannot charge a card, integrate with payment providers, or accept/store card data.

## Development workflow

This public repository uses a two-stage Git flow:

- `master` is the protected release branch and always represents the latest releasable version.
- `develop` is the integration branch for the next release.
- Work starts from `develop` on a short-lived branch and returns to `develop` through a pull request.
- Releases are pull requests from `develop` into `master`.

See [CONTRIBUTING.md](CONTRIBUTING.md) for branch names, review expectations, and the release process.

## What Phase 1 delivers

- Create merchant webhook endpoints, each with a generated signing secret.
- Create mock `payment.*` events and persist them in SQLite.
- Idempotent event creation via `Idempotency-Key`.
- Durable per-endpoint deliveries and delivery-attempt history.
- Async HTTP dispatch with HMAC-SHA256 request signing.
- Retry of non-2xx responses and network failures with capped exponential backoff plus jitter.
- Replay failed deliveries from the API.
- Docker, Docker Compose, CI, and focused tests for signing, retry policy, and dispatch behavior.

## Phase 2: security foundation

- Bearer API-key authentication for every management endpoint under `/v1`.
- Tenant isolation: endpoints, events, idempotency keys, delivery history, and replay operations are scoped to the authenticated tenant.
- API keys are configured through `PAYRELAY_API_KEYS`, retained only as SHA-256 digests in process memory, and compared in constant time.
- Existing SQLite installations are migrated safely; pre-Phase-2 data is assigned to the `legacy` tenant and is not exposed to configured tenants.

## Phase 3: endpoint lifecycle (in progress)

- Enable or disable a tenant's webhook endpoint without deleting its delivery history.
- Rotate a signing secret on demand; the replacement secret is returned exactly once.
- The next Phase 3 increment adds strict outbound target validation and SSRF controls.

## Safety boundary and threat model

PayRelay is a local/demo platform for synthetic events only.

- Inputs represent fictional payment state (`amount_cents`, status, and a merchant reference), never PANs, CVVs, bank credentials, or real transaction tokens.
- It makes outbound HTTP calls only to the endpoint URL registered by the caller. Treat this as a development service: deploy behind egress controls and an allowlist to mitigate SSRF before any shared environment use.
- Endpoint secrets are stored in SQLite in this MVP so the worker can sign deliveries. Creation is the only API response that reveals a secret; use a managed secret store and encryption at rest in a production evolution.
- HTTPS is supported but not required locally. Production should require HTTPS, redact logs, and rotate secrets.
- Receivers must validate the timestamp and HMAC in constant time, and reject stale requests to limit replay exposure.

## Architecture

```text
Inbound adapters                 Core                    Outbound adapters
HTTP API ────────────────→ application service ───→ SQLite repository
worker scheduler ────────→        │              └──→ HMAC HTTP webhook sender
                                  domain
```

PayRelay uses a ports-and-adapters (hexagonal) design. `internal/core` contains the domain, application use cases, and outbound port interfaces. `internal/adapters` contains the HTTP API, SQLite persistence, webhook transport, and worker scheduler. `cmd/server` is only the composition root that wires those pieces together.

The API writes an event and its initial endpoint delivery jobs in one transaction. A background worker atomically claims due jobs, posts a signed JSON envelope, and persists every outcome. SQLite WAL mode keeps the MVP portable and locally runnable. See [the architecture guide](docs/architecture.md) for dependency rules and testing strategy, and [endpoint sequence diagrams](docs/endpoint-sequences.md) for each HTTP operation.

## Quick start

Prerequisites: Go 1.23+ (or Docker). Configure at least one tenant API key before starting:

```powershell
$env:PAYRELAY_API_KEYS = 'merchant_demo:prk_use_a_random_value_of_at_least_20_characters'
go run ./cmd/server
```

For Docker Compose, copy `.env.example` to `.env`, replace its placeholder key, and run `docker compose up --build`. The Go executable intentionally does not read `.env` files itself; set environment variables through your shell, IDE, container platform, or process manager.

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

## Quality gates and functional testing

The CI workflow runs unit/integration tests with the race detector and enforces at least **85% statement coverage across `internal/...`**, which contains PayRelay's production domain, API, delivery, and persistence behavior. The `cmd/` package is intentionally excluded from this measurement because it is process wiring; CI still compiles it.

Run the same report locally:

```sh
make coverage
```

For a portfolio-ready, end-to-end walkthrough with curl payloads, expected HTTP responses, delivery history, replay behavior, and signature verification, see [the functional test runbook](docs/functional-test-runbook.md).

## API examples

### Interactive documentation and Postman

After starting the server, open [http://localhost:8080/docs/](http://localhost:8080/docs/) for Swagger UI. It loads the versioned OpenAPI document served by the application and its **Try it out** controls call the local API directly.

Import [`postman/PayRelay.postman_collection.json`](postman/PayRelay.postman_collection.json) into Postman to run the same local workflow with response assertions. The collection starts with the health check and persists created IDs as collection variables for the following requests.

Register an endpoint (use a request-bin service or a local listener in development):

```sh
curl -X POST http://localhost:8080/v1/endpoints \
  -H 'Authorization: Bearer prk_use_your_configured_key' \
  -H 'Content-Type: application/json' \
  -d '{"url":"http://host.docker.internal:9000/webhooks/payrelay"}'
```

The response includes the `whsec_...` secret once. Create a **mock** event:

```sh
curl -X POST http://localhost:8080/v1/events \
  -H 'Authorization: Bearer prk_use_your_configured_key' \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: demo-order-1007' \
  -d '{"type":"payment.succeeded","merchant_reference":"demo-order-1007","amount_cents":2599,"currency":"USD","status":"succeeded"}'
```

Repeat the same request and key to safely receive the original event. Reusing the key for different event content returns `409 Conflict`.

Inspect delivery jobs and attempts:

```sh
curl -H 'Authorization: Bearer prk_use_your_configured_key' http://localhost:8080/v1/events/evt_example/deliveries
curl -H 'Authorization: Bearer prk_use_your_configured_key' http://localhost:8080/v1/deliveries/dlv_example/attempts
curl -X POST -H 'Authorization: Bearer prk_use_your_configured_key' http://localhost:8080/v1/deliveries/dlv_example/replay
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
| `PAYRELAY_API_KEYS` | required | Comma-separated `tenant-id:api-key` pairs; API keys must be at least 20 characters |

## Roadmap

1. Add endpoint updates, secret rotation, SSRF-safe endpoint policies, and rate limits.
2. Move secrets to a KMS-backed store; require TLS and add structured audit logs/metrics/tracing.
3. Introduce Postgres plus a dedicated queue for horizontal workers, DLQs, rate limiting, and endpoint circuit breaking.
4. Add an event catalog, receiver verification SDKs, an OpenAPI spec, and load/fault-injection tests.
