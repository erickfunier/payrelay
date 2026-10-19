# Architecture

PayRelay follows the ports-and-adapters (hexagonal) style so business behavior is independent of delivery mechanisms and infrastructure choices.

```text
                    ┌───────────────────────────────┐
                    │            cmd/server         │
                    │        composition root       │
                    └──────────────┬────────────────┘
                                   │ wires ports
        inbound adapters           ▼
  ┌───────────────────┐   ┌───────────────────────────┐   outbound adapters
  │ HTTP REST handler │──▶│ core/application.Service  │──▶ SQLite repository
  └───────────────────┘   │                           │──▶ HMAC HTTP sender
  ┌───────────────────┐   │ core/domain + core/ports  │
  │ worker scheduler  │──▶│                           │
  └───────────────────┘   └───────────────────────────┘
```

## Layers

| Location | Responsibility | May depend on |
| --- | --- | --- |
| `internal/core/domain` | Business entities and identifiers | Go standard library only |
| `internal/core/ports` | Repository and webhook interfaces | Domain |
| `internal/core/application` | Use cases: endpoint/event management, dispatch, replay, retry | Domain and ports |
| `internal/adapters/httpapi` | Request validation and HTTP response mapping | Application, domain, ports errors |
| `internal/adapters/sqlite` | Durable repository implementation | Ports and domain; `database/sql` |
| `internal/adapters/webhook` | JSON envelope, HMAC signing, outbound HTTP | Ports and domain; `net/http` |
| `internal/adapters/worker` | Polling and operational logging | Application dispatcher port |
| `cmd/server` | Configuration and dependency wiring | Core and adapters |

Dependency direction always points inward. The core does not import `database/sql`, `net/http`, or a concrete adapter.

## Ports

`ports.Repository` represents the durable state required by PayRelay use cases. SQLite is one implementation; a Postgres adapter can replace it without changing application logic.

`ports.WebhookSender` represents the act of sending an event. The HTTP adapter owns serialization, request headers, HMAC-SHA256 signing, timeouts, and response-body cleanup. Application logic decides whether the result succeeds, retries, or fails permanently.

## Testing strategy

- **Application tests** use fake ports to test use cases without a database or network.
- **Adapter tests** verify SQLite persistence, HTTP request/response mapping, webhook signing/transport, and worker lifecycle behavior.
- **Functional tests** exercise the public API using the [functional test runbook](functional-test-runbook.md).
- CI enforces at least 85% statement coverage across `internal/...` and runs the race detector.

This gives fast feedback for business rules while retaining integration coverage at infrastructure boundaries.
