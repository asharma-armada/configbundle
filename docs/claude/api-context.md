# API Reference

> **When to load this file:** Read this before working on the bundler HTTP service, the `POST /enrich` endpoint, or any Orbital GraphQL integration.

---

## Overview

The configbundle bundler exposes a single HTTP endpoint (`POST /enrich`) that Orbital calls synchronously during its publish pipeline. The bundler queries Orbital's GraphQL API, builds a ConfigBundle manifest, and returns the encoded layer bytes. The bundler is stateless — it holds no OCI credentials and never pushes to ACR.

---

## Key decisions

- **Single endpoint** — `POST /enrich` only. No other routes. No health check beyond 2xx on enrich.
- **Stateless** — no database, no persistent state; all data fetched from Orbital GraphQL per request.
- **Fail fast** — any error (GraphQL failure, timeout, bad datacenter) returns non-2xx immediately. Orbital treats non-2xx as a publish failure and retries per `ORBITAL_ENRICHER_MAX_ATTEMPTS`.
- **Auth is caller's concern** — the bundler does not issue tokens; it optionally attaches `ORBITAL_BEARER_TOKEN` as a bearer token on GraphQL requests. Empty = no auth header.

---

## Enricher API contract

### Request (Orbital → bundler)

```
POST /enrich
Content-Type: application/json

{
  "jobId": "a1b2c3d4-e5f6-...",
  "datacenter": "colo-galleon"
}
```

`datacenter` matches `DataCenter.name` in Orbital's DGraph schema.

### Response (bundler → Orbital)

```json
[
  {
    "mediaType": "application/vnd.armada.configbundle.manifest.v1+yaml",
    "data": "<standard base64-encoded manifest bytes>"
  }
]
```

- `data` is standard base64 (not URL-safe)
- Empty array `[]` is valid — enricher ran but produced no layers
- Timeout default: 30s (configured on Orbital side via `ORBITAL_ENRICHER_TIMEOUT`)

### Go types

```go
type enrichRequest struct {
    JobID      string `json:"jobId"`
    Datacenter string `json:"datacenter"`
}

type layer struct {
    MediaType string `json:"mediaType"`
    Data      string `json:"data"` // standard base64
}
```

---

## Environment variables (bundler)

| Variable | Default | Description |
|---|---|---|
| `BUNDLER_PORT` | `8020` | HTTP listen port |
| `ORBITAL_GRAPHQL_URL` | `http://orbital/graphql` | Orbital GraphQL endpoint |
| `ORBITAL_BEARER_TOKEN` | `""` | Bearer token for Orbital GraphQL (empty = no auth; required post-Spike 11) |

---

## GraphQL query pattern

```graphql
query ConfigBundleFields($dc: String!) {
  queryDataCenter(filter: { name: { eq: $dc } }) {
    name
    orbId
    # add config fields needed by cb-controller
  }
}
```

The bundler queries this endpoint using `ORBITAL_GRAPHQL_URL`. If `ORBITAL_BEARER_TOKEN` is set, attach it as `Authorization: Bearer <token>`.

---

## Orbital enricher configuration

Orbital retries failed enricher calls. These are Orbital-side settings, not configurable in the bundler:

| Variable | Default | Description |
|---|---|---|
| `ORBITAL_ENRICHER_TIMEOUT` | `30s` | Per-attempt HTTP timeout |
| `ORBITAL_ENRICHER_MAX_ATTEMPTS` | `3` | Total attempts (1 initial + 2 retries) |
| `ORBITAL_ENRICHER_MAX_RESPONSE_BYTES` | `10485760` | Max response size (10 MB) |

---

## Gotchas

- **Enricher URLs are per-request** — Orbital does not configure enricher URLs server-side. The caller supplies them in the publish request body. Do not add server-side enricher registration to either service.
- **Non-2xx = publish fails, with retry** — Orbital retries up to `ORBITAL_ENRICHER_MAX_ATTEMPTS` times (default 3) with exponential backoff (1s–10s). If all attempts fail, the publish job is marked failed, `enricher_error` is recorded, nothing is pushed to ACR. There is no partial-success path.
- **Timeout counts as a failed attempt** — per-attempt timeout (default 30s via `ORBITAL_ENRICHER_TIMEOUT`) triggers the same retry logic as a non-2xx. The bundler does not need to handle Orbital's retry — just fail fast on its own errors.
- **Response size limit** — a response body exceeding `ORBITAL_ENRICHER_MAX_RESPONSE_BYTES` (default 10 MB) causes an immediate failure with no retry.
- **`jobId` is informational** — the bundler receives it for logging/tracing but does not need to use it to query data. All data comes from `datacenter`.

---

## External references

- [Enricher integration design](../../configbundle-integration.md)
- [Local end-to-end test flow](../../configbundle-integration.md#local-end-to-end-test-flow)

---

## Domain file maintenance

Update this file when:
- The enricher request or response schema changes
- A new environment variable is added to the bundler
- The GraphQL query pattern changes materially
- An error handling convention is settled

Updates must be in the same PR as the code change that prompted them.
