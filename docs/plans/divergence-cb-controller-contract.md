# Contract — Divergence Reporting (CB Controller, Spike 7)

> **For the configbundle repo (`~/armada/configbundle`).** This is the contract orbital expects CB Controller to implement for divergence reporting.
>
> **Lives here (orbital repo)** because orbital is the consumer and defines the contract. Mirror this file into configbundle when implementation starts.
>
> **Companion:** `divergence-orbital-ingestion.md` (orbital-side ingestion plan).
>
> **Authoritative architecture:** `docs/reference/SDD-CONTEXT.md` §6, §7, §11, §12.

## What CB Controller must do

Build the **Divergence Reporter** as a scheduled `ctrl.Runnable` in the configbundle repo (Spike 7). On each tick:

1. List ConfigBundle CRs in the cluster
2. For each CR, inspect `metadata.managedFields`
3. Find every leaf field owned by `local:admin` (single fixed manager string per configbundle CLAUDE.md)
4. For each owned leaf, produce a raw report entry (see schema below) — **stays in K8s field-path terms**; no orbId translation client-side
5. **POST the full current list to orb's intake endpoint in a single call:**
   `POST {ORB_DIVERGENCE_INTAKE_URL}` — default `http://orb:8010/api/v1/divergence`

**Frequency:** configurable via `DIVERGENCE_REPORTER_SCHEDULE` (cron expression). Default: `*/5 * * * *` (every 5 minutes).

CB Controller is the producer of K8s-path data; orb is the translator and relay. CB Controller does NOT translate to `orbId`+`field`, does NOT write to S3, does NOT hold cloud credentials. The full edge↔cloud transport — including the K8s-path → orbital translation — is orb's responsibility.

## The intake payload (replace-not-merge, full set every POST)

```http
POST http://orb:8010/api/v1/divergence
Content-Type: application/json

{
  "bundleDigest": "sha256:abc123...",
  "overrides": [
    {
      "path":          "spec.servers[serviceTag=3RK3V64].idrac.sshEnabled",
      "intendedValue": false,
      "overrideValue": true,
      "who":           "local:admin",
      "when":          "2026-06-11T14:00:00Z"
    }
  ]
}
```

| Field | Source | Notes |
|---|---|---|
| `bundleDigest` | cb-controller's `status.lastAppliedDigest` | Tells orb which mapping artifact to use for translation. |
| `path` | parsed from `managedFields` entry owned by `local:admin` | Stable K8s field-path form (uses `serviceTag` as the list map key for `servers[]`). Orb walks this against the mapping to derive `orbId` + `field`. |
| `intendedValue` | read from the manifest YAML cb-controller last applied | NOT from a live cluster read or orbital query. The intent value frozen at the bundle cb-controller is reporting against — survives orb importing a newer bundle while cb-controller still applies an older one. |
| `overrideValue` | CR's current value at the path | What admin set locally. |
| `who` | always `local:admin` for MVP | Single fixed string per configbundle settled decision. |
| `when` | the time the field's ownership first transitioned to `local:admin` | Stable across reports. See "open items" below — `managedFields[].time` is updated on every re-apply by the manager; cb-controller may need a sidecar tracking annotation to preserve true first-seen. |

### Empty array = no divergence

`overrides: []` is valid and means "no local overrides currently." Orb publishes a snapshot with empty `overrides`, orbital interprets that as "all divergence resolved for this DC."

### Replace-not-merge

Every POST is the **full current divergence set**. orb intake replaces, not merges. If a field is no longer owned by `local:admin` (ownership released), it MUST disappear from the next POST — that's how orb (and orbital) learn the divergence is resolved.

### Empty array = no divergence

`POST []` is valid and signals "currently no fields locally owned." Orbital interprets a snapshot with `overrides: []` as "all divergence resolved for this DC."

## How K8s field paths get translated to orbital orbId + field

**Settled — option D2: orb hosts the mapping; cb-controller stays out of the translation.**

### What cb-bundler must do (cloud side, build time)

When producing a ConfigBundle artifact, cb-bundler creates a **new OCI layer** in the bundle:

| Layer | Media type | Contents |
|---|---|---|
| `mapping.json` | `application/vnd.armada.configbundle.mapping.v1+json` | Path → orbId map for this bundle |

The mapping is generated from orbital's data — cb-bundler already queries orbital GraphQL when building the bundle, so it knows each ConfigItem's `orbId`. It serializes that knowledge into the layer.

Mapping content — **flat list of `{path, orbId, type}` entries**:

```json
{
  "bundleDigest": "sha256:abc...",
  "items": [
    {"path": "spec",                                     "orbId": "colo:colo-galleon",  "type": "DataCenter"},
    {"path": "spec.servers[serviceTag=3RK3V64]",         "orbId": "colo:srv-001",       "type": "Server"},
    {"path": "spec.servers[serviceTag=3RK3V64].idrac",   "orbId": "colo:srv-001-idrac", "type": "IdracSettings"}
  ]
}
```

### `type` field

Each item carries the **orbital GraphQL type name** of the node identified by `orbId` (e.g. `Server`, `IdracSettings`, `DataCenter`). cb-bundler already has this — it knows the CRD type → orbital type mapping when building the bundle. The mapping ships this type alongside the orbId so orbital can dispatch `update{Type}(...)` mutations when a cloud admin clicks **Accept** on a divergence row (see `divergence-accept-mutation.md`).

Without `type`, orbital cannot auto-mutate intent on Accept and falls back to a manual flow. cb-bundler MUST emit it.

### Why flat-list, not nested

Nested formats (`servers: {serviceTag: {...}}`) hard-code each domain's `+listMapKey` field name into orb's parser. Every new CRD list (`clusters[]`, `applications[]`, …) would require orb code changes. The flat list keeps orb's parser **domain-agnostic** — it only does string prefix matching, never inspects the path internals.

### Path format convention

Both cb-bundler (producing mapping entries) and cb-controller (extracting paths from `managedFields` for divergence reports) must format paths identically. The convention mirrors K8s's `managedFields` path notation but flattened to a single string:

```
spec.servers[serviceTag=3RK3V64].idrac.sshEnabled
```

Format rules:
- Field segments separated by `.`
- List/map entry selectors as `[<keyField>=<keyValue>]` (single key only — K8s SSA `+listMapKey` allows only one)
- Quotes only on the value if it contains characters that need escaping; raw otherwise
- Path produced by cb-controller from `managedFields` MUST be byte-equal to the path produced by cb-bundler from CRD schema knowledge

This is the only convention spanning cb-bundler ↔ cb-controller. Both binaries are in the same repo, so enforcing it is local. orb depends only on prefix-matchability — it doesn't parse internals.

### Lookup algorithm (orb's intake)

Given incoming `path = "spec.servers[serviceTag=3RK3V64].idrac.sshEnabled"`:

```
matched := ""
for each item in mapping.items:
    if path starts with item.path AND len(item.path) > len(matched):
        matched = item.path
        matchedOrbId = item.orbId
if matched is empty:
    error: no mapping prefix matches path
field = path[len(matched)+1:]  // strip the matched prefix + '.'
emit { orbId: matchedOrbId, field: field }
```

For each new ConfigBundle domain configbundle adds (cluster, application, network, …), cb-bundler emits the appropriate path entries and orb requires NO code change.

### What orb does (edge side, when bundle arrives)

1. Pulls bundle from Zot (existing flow)
2. Importer recognizes the mapping-layer media type, **stores under `DataDir/mappings/<bundle-digest>.json`** (rather than dispatching it to a consumer like the manifest layer)
3. Graph layers (`data.json.gz`, `schema.gz`) imported to DGraph as today
4. Manifest layer dispatched to cb-controller as today
5. Old mappings pruned alongside old bundle artifacts

### What orb does (intake time, when cb-controller POSTs)

On `POST /api/v1/divergence`:

1. Parse the payload (see schema above)
2. Load `DataDir/mappings/<bundleDigest>.json`
3. For each `override` entry:
   - Walk `path` against the mapping → resolve to `orbId` + leaf `field`
4. Build canonical entries: `{orbId, field, intendedValue, overrideValue, who, when}`
5. Save to `DataDir/divergence/current.json` (replace, not merge)
6. Return 200 with count of entries stored

If `bundleDigest` doesn't match any stored mapping: return **422 Unprocessable Entity** with `{error: "unknown bundleDigest", digest: "..."}`. cb-controller logs, skips this report, retries next tick.

### Why cb-controller can't (and shouldn't) do the translation

- Mapping format is orb-internal — keeping it on the orb side means it can evolve without coordinating with cb-controller's release
- cb-controller stays small: it knows K8s, doesn't need to know orbital's identifier scheme
- One side of the boundary owns the translation; one side owns the K8s state. Clear split.

### What if K8s field name ≠ orbital field name?

Today they match — IdracSpec fields are named identically to IdracSettings fields. The leaf segment of the K8s path is the orbital field name. If they ever diverge, the mapping JSON can carry per-leaf name overrides; cb-controller's logic doesn't change (still sends paths).

## Resolution semantics — what cb-controller must do for the three cloud admin actions

### Accept

Cloud admin chose "accept the override as new intent." Orbital updates its intent via GraphQL mutation. cb-bundler reads new intent on next bundle build, produces a new bundle where `intendedValue == overrideValue`. **cb-controller has nothing special to do** — it consumes the new bundle like any other; ownership stays with `local:admin`.

When the next divergence reporter tick runs, the entry still appears in the report (admin hasn't released ownership). After admin releases ownership (`Resolution #2` from SDD-CONTEXT §6.2), the field disappears from the report.

### Force

Cloud admin chose "force cloud intent regardless of local override." Orbital records the decision in `DivergenceResolution{action: force, cb_consumed: false}`. cb-bundler reads un-consumed force rows when building the next bundle and emits a new section on the ConfigBundle CR:

```yaml
spec:
  takeover:
    - orbId: colo:srv-001-idrac
      field: sshEnabled
```

(or `[]` if no pending forces)

**cb-controller behavior:** when processing a new bundle that includes a `spec.takeover[]` list, for each entry it executes a separate apply with `--force-conflicts` scoped to just that field path. The local manager loses ownership. After the apply, the field is owned by `config-bundle-controller` again.

cb-bundler calls orbital's `POST /api/v1/divergence/resolutions/:id/consumed` after the bundle is pushed.

### Ignore

Cloud admin chose "leave as-is." Orbital records `DivergenceResolution{action: ignore}`. cb-bundler does nothing special — the next bundle is built from intent unchanged. cb-controller does nothing special — local override persists. The entry stays in the divergence report until admin releases ownership.

Orbital's UI tags the entry "ignored" so admin doesn't see it as needing action.

## Configuration env vars (cb-controller side)

| Var | Default | Purpose |
|---|---|---|
| `ORB_DIVERGENCE_INTAKE_URL` | `http://orb:8010/api/v1/divergence` | Where to POST entries |
| `DIVERGENCE_REPORTER_SCHEDULE` | `*/5 * * * *` | Cron expression for report cadence |
| `DIVERGENCE_REPORTER_ENABLED` | `false` | Default off; explicit enable per environment |

`DIVERGENCE_REPORT_DEST` (the previously-planned S3 destination env var) is **dropped** — cb-controller no longer writes to S3 directly.

## Schema / CRD changes in configbundle repo

| Where | Change |
|---|---|
| `api/v1/configbundle_types.go` | Add `Takeover []TakeoverEntry` to `ConfigBundleSpec`; new `TakeoverEntry struct { OrbID, Field string }`. **No other CRD field additions** — orbIds are not stored in the CR; they live in the bundle's mapping layer (which orb stores). |
| `bundle/mediatype.go` | Add `MediaTypeMapping = "application/vnd.armada.configbundle.mapping.v1+json"` |
| `internal/bundler/` | cb-bundler produces the `mapping.json` layer alongside manifest + data + schema. Queries orbital GraphQL for each ConfigItem's `orbId` while assembling the bundle. |
| `internal/bundler/orbital.go` | Query orbital `GET /api/v1/divergence/resolutions/pending-force` when building a bundle; produce `spec.takeover[]` from the response. Call `POST /api/v1/divergence/resolutions/:id/consumed` after the bundle is successfully pushed. |
| `internal/controller/` | New `divergence_reporter.go` — `ctrl.Runnable` with the cron schedule. For each ConfigBundle CR: read `managedFields`, extract paths owned by `local:admin`, read intended values from the last-applied manifest, read override values from the current CR, POST the array to orb. |
| `internal/controller/` | New `takeover.go` — after the main SSA apply, process `spec.takeover[]` by running one `--force-conflicts` apply per entry, scoped to just that field path. |
| `cmd/main.go` | Register the Divergence Reporter runnable with manager. |

After CRD changes: `make generate && make manifests`. Bump CRD schema annotation as needed.

## Testing requirements for cb-controller

- **Reporter unit test:** given a synthetic CR with managedFields, walk-up algorithm produces correct `(orbId, field)` pairs. Table-driven, cover nested cases.
- **Reporter integration (envtest):** create CR, SSA-apply as `local:admin` for one field, run reporter, assert POST body matches expected entries.
- **Takeover integration (envtest):** apply CR with a `spec.takeover[]` entry, assert local:admin field ownership is removed after reconcile.
- **End-to-end (against running orb):** existing `make test-e2e` patterns; the reporter posts to a local orb instance and orb's `/divergence` UI shows entries.

## Open items pending sign-off in orbital design session

1. **Q2 — mapping embed (this contract assumes inline `orbId` per option C).** If user picks A (separate OCI layer) instead, this contract changes substantially: consume protocol on orb extended; cb-controller fetches mapping from a different source.
2. **Resolution row → bundle handoff format.** Currently this doc proposes `GET /api/v1/divergence/resolutions/pending-force`. If a different shape preferred (e.g. embedded in publish trigger), adjust here.
3. **`when` semantics.** This doc says "when ownership first transitioned to local:admin." Confirm cb-controller can read this stably from `managedFields[].time` — needs verification that K8s doesn't update the time on every re-apply by the same manager. (Per K8s docs: `managedFields[].time` is the time of last modification by that manager, which IS updated. We may need an annotation alongside to track "first seen at." Open.)

## What this contract does NOT specify

- cb-bundler's GraphQL query shape (configbundle repo's concern)
- Exactly which K8s libraries cb-controller uses (controller-runtime patterns are configbundle's choice)
- ConfigBundle CR namespace / RBAC for the reporter (configbundle's deploy concern)
- Authentication on orb's intake endpoint (currently none; NetworkPolicy is the gate per Spike 15 decision)

## Sign-off sequence

Before configbundle starts Spike 7:
1. Orbital plan (`divergence-orbital-ingestion.md`) reviewed
2. Q2 decided (inline CR orbId vs separate OCI layer)
3. CRD field additions agreed (`OrbID` on ConfigBundleSpec/ServerSpec/IdracSpec; `Takeover []TakeoverEntry`)
4. This file copied to `configbundle/docs/decisions/` or `configbundle/docs/plans/`
5. Spike 7 picked up
