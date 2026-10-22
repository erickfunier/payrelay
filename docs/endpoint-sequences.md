# Endpoint sequence diagrams

This guide shows the runtime sequence for every public HTTP operation in PayRelay. All management routes under `/v1` require a configured Bearer API key; `/healthz` and `/docs/` are public.

## Health check

### `GET /healthz`

```mermaid
sequenceDiagram
    participant Client
    participant API as HTTP API
    Client->>API: GET /healthz
    API-->>Client: 200 {"status":"ok"}
```

## Webhook endpoints

### `POST /v1/endpoints`

```mermaid
sequenceDiagram
    participant Client
    participant API as HTTP API
    participant Auth as API-key authenticator
    participant Policy as Target policy + DNS
    participant App as Application service
    participant DB as SQLite
    Client->>API: POST endpoint URL + Bearer key
    API->>Auth: authenticate request
    Auth-->>API: tenant ID
    API->>Policy: validate URL and resolved addresses
    alt unsafe or unresolvable target
        Policy-->>API: reject
        API-->>Client: 400 Bad Request
    else public target
        Policy-->>API: approved
        API->>App: CreateEndpoint(tenant, URL)
        App->>App: generate whsec_ secret
        App->>DB: persist endpoint
        DB-->>App: endpoint saved
        App-->>API: endpoint + one-time secret
        API-->>Client: 201 Created
    end
```

### `GET /v1/endpoints`

```mermaid
sequenceDiagram
    participant Client
    participant API as HTTP API
    participant Auth as API-key authenticator
    participant App as Application service
    participant DB as SQLite
    Client->>API: GET /v1/endpoints + Bearer key
    API->>Auth: authenticate request
    Auth-->>API: tenant ID
    API->>App: ListEndpoints(tenant)
    App->>DB: list tenant endpoints
    DB-->>App: active and disabled endpoints
    App-->>API: endpoints
    API->>API: remove signing secrets
    API-->>Client: 200 {"data":[...]}
```

### `GET /v1/endpoints/{endpointID}`

```mermaid
sequenceDiagram
    participant Client
    participant API as HTTP API
    participant Auth as API-key authenticator
    participant App as Application service
    participant DB as SQLite
    Client->>API: GET endpoint + Bearer key
    API->>Auth: authenticate request
    Auth-->>API: tenant ID
    API->>App: GetEndpoint(tenant, endpoint ID)
    App->>DB: read tenant-scoped endpoint
    alt endpoint belongs to tenant
        DB-->>App: endpoint
        App-->>API: endpoint
        API->>API: remove signing secret
        API-->>Client: 200 OK
    else missing or belongs to another tenant
        DB-->>App: not found
        App-->>API: not found
        API-->>Client: 404 Not Found
    end
```

### `PATCH /v1/endpoints/{endpointID}`

```mermaid
sequenceDiagram
    participant Client
    participant API as HTTP API
    participant Auth as API-key authenticator
    participant App as Application service
    participant DB as SQLite
    Client->>API: PATCH {"active": true|false} + Bearer key
    API->>Auth: authenticate request
    Auth-->>API: tenant ID
    API->>App: SetEndpointActive(tenant, endpoint ID, active)
    App->>DB: update active state and updated_at
    alt endpoint belongs to tenant
        DB-->>App: updated endpoint
        App-->>API: endpoint
        API->>API: remove signing secret
        API-->>Client: 200 OK
    else endpoint missing or other tenant
        DB-->>App: not found
        App-->>API: not found
        API-->>Client: 404 Not Found
    end
```

### `POST /v1/endpoints/{endpointID}/rotate-secret`

```mermaid
sequenceDiagram
    participant Client
    participant API as HTTP API
    participant Auth as API-key authenticator
    participant App as Application service
    participant DB as SQLite
    Client->>API: POST rotate-secret + Bearer key
    API->>Auth: authenticate request
    Auth-->>API: tenant ID
    API->>App: RotateEndpointSecret(tenant, endpoint ID)
    App->>App: generate replacement whsec_ secret
    App->>DB: replace secret and update timestamp
    alt endpoint belongs to tenant
        DB-->>App: updated endpoint
        App-->>API: endpoint + one-time new secret
        API-->>Client: 200 OK
    else endpoint missing or other tenant
        DB-->>App: not found
        App-->>API: not found
        API-->>Client: 404 Not Found
    end
```

## Mock payment events

### `POST /v1/events`

```mermaid
sequenceDiagram
    participant Client
    participant API as HTTP API
    participant Auth as API-key authenticator
    participant App as Application service
    participant DB as SQLite
    Client->>API: POST mock payment event + optional Idempotency-Key
    API->>Auth: authenticate request
    Auth-->>API: tenant ID
    API->>API: validate and normalize payload
    API->>App: CreateEvent(tenant, event)
    App->>DB: list active tenant endpoints
    DB-->>App: active endpoints
    App->>DB: transaction: persist event and delivery jobs
    alt new event
        DB-->>App: event + delivery jobs
        App-->>API: created event
        API-->>Client: 201 Created
    else same idempotency key and same payload
        DB-->>App: original event
        App-->>API: idempotent replay
        API-->>Client: 200 OK
    else same idempotency key and changed payload
        DB-->>App: idempotency conflict
        App-->>API: conflict
        API-->>Client: 409 Conflict
    end
```

### `GET /v1/events/{eventID}`

```mermaid
sequenceDiagram
    participant Client
    participant API as HTTP API
    participant Auth as API-key authenticator
    participant App as Application service
    participant DB as SQLite
    Client->>API: GET event + Bearer key
    API->>Auth: authenticate request
    Auth-->>API: tenant ID
    API->>App: GetEvent(tenant, event ID)
    App->>DB: read tenant-scoped event
    alt event belongs to tenant
        DB-->>App: event
        App-->>API: event
        API-->>Client: 200 OK
    else event missing or other tenant
        DB-->>App: not found
        App-->>API: not found
        API-->>Client: 404 Not Found
    end
```

### `GET /v1/events/{eventID}/deliveries`

```mermaid
sequenceDiagram
    participant Client
    participant API as HTTP API
    participant Auth as API-key authenticator
    participant App as Application service
    participant DB as SQLite
    Client->>API: GET event deliveries + Bearer key
    API->>Auth: authenticate request
    Auth-->>API: tenant ID
    API->>App: GetEvent(tenant, event ID)
    App->>DB: verify tenant owns event
    alt event belongs to tenant
        DB-->>App: event
        API->>App: ListDeliveries(tenant, event ID)
        App->>DB: list delivery jobs
        DB-->>App: deliveries
        App-->>API: deliveries
        API-->>Client: 200 {"data":[...]}
    else event missing or other tenant
        DB-->>App: not found
        App-->>API: not found
        API-->>Client: 404 Not Found
    end
```

## Delivery operations

### `GET /v1/deliveries/{deliveryID}/attempts`

```mermaid
sequenceDiagram
    participant Client
    participant API as HTTP API
    participant Auth as API-key authenticator
    participant App as Application service
    participant DB as SQLite
    Client->>API: GET delivery attempts + Bearer key
    API->>Auth: authenticate request
    Auth-->>API: tenant ID
    API->>App: ListAttempts(tenant, delivery ID)
    App->>DB: list tenant-scoped immutable attempts
    DB-->>App: attempts (possibly empty)
    App-->>API: attempts
    API-->>Client: 200 {"data":[...]}
```

### `POST /v1/deliveries/{deliveryID}/replay`

```mermaid
sequenceDiagram
    participant Client
    participant API as HTTP API
    participant Auth as API-key authenticator
    participant App as Application service
    participant DB as SQLite
    Client->>API: POST replay + Bearer key
    API->>Auth: authenticate request
    Auth-->>API: tenant ID
    API->>App: ReplayFailed(tenant, delivery ID)
    App->>DB: queue failed delivery for retry
    alt failed delivery belongs to tenant
        DB-->>App: queued
        App-->>API: queued
        API-->>Client: 202 {"status":"queued"}
    else delivery missing, other tenant, or not failed
        DB-->>App: not found
        App-->>API: cannot replay
        API-->>Client: 409 Conflict
    end
```

## Background delivery lifecycle

This is not a public HTTP operation, but it shows how jobs created by `POST /v1/events` reach a receiver.

```mermaid
sequenceDiagram
    participant Worker as Worker scheduler
    participant App as Application service
    participant DB as SQLite
    participant Sender as HMAC HTTP sender
    participant Receiver as Public webhook receiver
    Worker->>App: DispatchOne()
    App->>DB: atomically claim due delivery
    DB-->>App: delivery, endpoint, event
    App->>Sender: Send signed JSON envelope
    Sender->>Receiver: POST signed webhook
    Receiver-->>Sender: HTTP response
    alt 2xx response
        Sender-->>App: success
        App->>DB: record successful attempt
    else non-2xx or network failure
        Sender-->>App: failure
        App->>DB: record failed attempt / schedule retry
    end
```
