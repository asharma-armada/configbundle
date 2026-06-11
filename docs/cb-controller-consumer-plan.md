# CB Controller — Consumer Migration Plan

## Context

This document is a copy-paste plan for a Claude Code session in the CB Controller repository. It defines the changes required to migrate CB Controller from an independent OCI puller to a registered consumer in orb's import dispatch pipeline.

**The redesign in one sentence:** Instead of CB Controller pulling the full OCI artifact from ACR and extracting its own layer, orb pulls (or receives) the full artifact, decomposes it, and dispatches each layer to the right consumer by media type. CB Controller is the consumer for `application/vnd.armada.configbundle.manifest.v1+yaml`.

**Source of truth for the full architecture:** `docs/configbundle-integration.md` in the orbital repository.

---

## What Changes

### Before

```
CB Controller
  → pulls artifact from ACR (oras-go)
  → cosign verify
  → extract layer by media type
  → apply manifest to cluster
  → (separately) POST graph layers to orb /import/subgraph
```

### After

```
Orb (dispatch)
  → POST cb-controller /consume
       Content-Type: application/vnd.armada.configbundle.manifest.v1+yaml
       X-Orb-Tag: v5
       X-Orb-Digest: sha256:...
       X-Orb-Import-ID: <uuid>
       Body: raw manifest bytes

CB Controller
  → receive POST /consume
  → apply manifest to cluster
```

CB Controller no longer needs: OCI registry credentials, oras-go dependency, cosign verification, layer extraction logic, or knowledge of the artifact format.

---

## Consumer Endpoint Spec

### `POST /consume`

Orb calls this when it has a layer matching the registered media type.

**Request:**

```
POST /consume
Content-Type: application/vnd.armada.configbundle.manifest.v1+yaml
X-Orb-Tag: v5
X-Orb-Digest: sha256:abc123...
X-Orb-Import-ID: 550e8400-e29b-41d4-a716-446655440000

<raw manifest bytes — same bytes the bundler produced, no encoding>
```

**Response:**

- `200 OK` — accepted. Body can be empty or `{"status":"accepted"}`.
- `4xx` / `5xx` — dispatch failed. Orb will record the error in the import history entry and **continue** — DGraph import is already complete. CB Controller should not return 5xx for slow cluster operations; accept async and return 200.

**Important:** Respond quickly. Orb's dispatch is synchronous during the import pipeline. If CB Controller is slow, it delays orb's import completion status update (not the DGraph import itself, but the history record). Recommended: accept the layer, enqueue for async apply, return 200 immediately.

---

## Implementation Steps

### Step 1 — Add `POST /consume` handler

Create the handler that receives the dispatch from orb.

```go
// handler/consume.go

func (h *Handler) Consume(w http.ResponseWriter, r *http.Request) {
    mediaType := r.Header.Get("Content-Type")
    tag := r.Header.Get("X-Orb-Tag")
    digest := r.Header.Get("X-Orb-Digest")
    importID := r.Header.Get("X-Orb-Import-ID")

    body, err := io.ReadAll(io.LimitReader(r.Body, maxManifestBytes))
    if err != nil {
        http.Error(w, "failed to read body", http.StatusInternalServerError)
        return
    }

    if mediaType != "application/vnd.armada.configbundle.manifest.v1+yaml" {
        http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
        return
    }

    // Log receipt for observability
    slog.Info("received layer dispatch",
        "mediaType", mediaType,
        "tag", tag,
        "digest", digest,
        "importID", importID,
        "bytes", len(body),
    )

    // Apply the manifest (or enqueue for async apply)
    if err := h.applyManifest(r.Context(), body); err != nil {
        slog.Error("apply manifest failed", "err", err, "importID", importID)
        http.Error(w, "apply failed: "+err.Error(), http.StatusInternalServerError)
        return
    }

    w.WriteHeader(http.StatusOK)
}
```

### Step 2 — Register the route

Register `POST /consume` in your HTTP router/mux. No auth required for local network dispatch — protect via Kubernetes NetworkPolicy if needed.

### Step 3 — Add env var for the consume endpoint

```
CB_CONTROLLER_PORT=8095  (configurable; 8095 default avoids conflict with dgraph-alpha on :8080)
```

### Step 4 — Remove OCI pull logic

Remove or archive (do not delete git history):
- `oras-go` registry pull code
- `cosign` verification code  
- Layer extraction by media type logic
- Any `POST /import/subgraph` call to orb (orb now handles DGraph import itself as part of the same dispatch pipeline)

**Important:** CB Controller no longer calls orb's import API. Orb dispatches to CB Controller, not the other way around.

### Step 5 — Remove OCI-related env vars

Remove from config, manifests, and secrets:
- ACR registry URL
- ACR username / password / service principal credentials
- `ORB_OCI_*` env vars that were set on the CB Controller pod

### Step 6 — Update K8s manifests

**CB Controller deployment:**
- Remove registry credential secret mounts
- Ensure `POST /consume` port is exposed within the cluster
- Add NetworkPolicy allowing ingress from orb pod to `/consume` port

**Orb deployment:**
- Add `ORB_CONSUMERS` env var pointing at CB Controller:
  ```yaml
  - name: ORB_CONSUMERS
    value: '[{"mediaType":"application/vnd.armada.configbundle.manifest.v1+yaml","url":"http://cb-controller:8030/consume"}]'
  ```

### Step 7 — Tests

**Unit tests:**
- `TestConsume_ValidManifest` — valid body + correct headers → 200, `applyManifest` called with correct bytes
- `TestConsume_WrongMediaType` — wrong Content-Type → 415
- `TestConsume_ApplyError` — `applyManifest` returns error → 500

**Integration test:**
- Start CB Controller, POST to `/consume` with a real manifest fixture
- Assert the manifest was applied (check K8s CR state or mock apply function)

---

## What Does NOT Change

- ConfigBundle CR structure and schema — unchanged
- Reconciliation / apply logic — unchanged. The `Consume` handler is a new *trigger* for the existing apply pipeline, not a replacement. All managedFields inspection, `omitAdminOwnedServers` logic, and SSA conflict avoidance runs exactly as before — just initiated by a POST to `/consume` instead of a Zot poll. Anyone implementing the handler cold must understand: receiving the layer bytes is only the first step; the existing apply pipeline with its admin-override handling is mandatory before touching any CR.
- The bundler (`POST /bundle`) — completely unchanged, still produces the same layer bytes
- Existing cluster RBAC for applying CRs — unchanged

---

## Orb Side (to be implemented in orbital repo — not yet live)

`POST /import/artifact` — full pipeline endpoint. To be built in orb. Accepts a zip of the full OCI artifact. Pipeline:
1. **Cosign verify** — orb holds `ORB_OCI_PUBLIC_KEY_PATH` and verifies the artifact signature before any decomposition. Verification failure = reject, 400.
2. **Decompose** — split layers by media type.
3. **DGraph import** — always. `data.json.gz` + `schema.gz` → `drop_all` + `dgraph live`.
4. **Dispatch** — POST each non-graph layer to its registered consumer URL (`ORB_CONSUMERS`). Best-effort.
5. **Record** — write import history entry with per-consumer dispatch result (HTTP status + error if any).

Dispatch request headers orb sends:
- `Content-Type` = layer media type
- `X-Orb-Tag` = OCI tag imported (empty for courier uploads)
- `X-Orb-Digest` = artifact manifest digest
- `X-Orb-Import-ID` = orb import UUID

Dispatch is best-effort — DGraph import completes regardless of CB Controller response.

**What triggers `POST /import/artifact`:**
- **OCI source enabled** (`ORB_ENABLE_OCI_REGISTRY=true`): orb's poller discovers a new tag, pulls the artifact, feeds it through this pipeline internally. Same trigger as today.
- **OCI source disabled**: an external caller (admin, courier, deploy script) POSTs directly to `/import/artifact`. No automatic trigger — imports are on-demand.

**What orb records in import history:**
- Dispatch HTTP status per consumer (200 = accepted, 5xx = failed)
- Orb does NOT track whether the downstream apply succeeded — that is CB Controller's observability concern (CR conditions, controller logs). Import history is a dispatch receipt, not an end-to-end apply confirmation.

---

## Dependency Note

CB Controller now depends on orb being available at the edge to receive layer dispatches. This coupling is intentional — orb is the single artifact ingress point. If orb is down, CB Controller does not receive updates until orb recovers and the next import runs. Design CB Controller's apply logic to be idempotent — re-applying the same manifest should be a no-op.
