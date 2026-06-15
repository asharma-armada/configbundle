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

## The intake payload — orbital-native, replace-not-merge

cb-controller translates K8s paths locally before POSTing. The payload speaks orbital-native vocabulary only (`orbId`, `field`, `type`); no configbundle concepts cross orb's API boundary. See [`docs/reference/DIVERGENCE-INTAKE.md`](../reference/DIVERGENCE-INTAKE.md) for the canonical intake contract.

```http
POST http://orb:8010/api/v1/divergence
Content-Type: application/json

{
  "overrides": [
    {
      "orbId":         "colo:srv-001-idrac",
      "field":         "sshEnabled",
      "type":          "IdracSettings",
      "intendedValue": false,
      "overrideValue": true,
      "who":           "local:admin",
      "when":          "2026-06-11T14:00:00Z"
    }
  ]
}
```

| Field | Source (cb-controller side) | Notes |
|---|---|---|
| `orbId` | local mapping lookup: `path → orbId` | cb-controller stores the mapping from the bundle; resolves at report-time. |
| `field` | local mapping lookup: `path → field` | The leaf field name on the orbital ConfigItem. |
| `type` | local mapping lookup: `path → type` | Orbital GraphQL type name; orbital uses this to dispatch `update{Type}` on Accept. |
| `intendedValue` | read from the manifest YAML cb-controller last applied | NOT from a live cluster read or an orbital query. The intent frozen at the bundle cb-controller is reporting against — survives orb importing a newer bundle while cb-controller still applies an older one. |
| `overrideValue` | CR's current value at the path | What admin set locally. |
| `who` | always `local:admin` for MVP | Single fixed string per configbundle settled decision. |
| `when` | first observed time for the override | See open items — `managedFields[].time` updates on every re-apply by the manager, so cb-controller likely needs an annotation to preserve true first-seen. |

### Replace-not-merge

Every POST is the **full current divergence set**. orb's intake replaces, not merges. If a field is no longer owned by `local:admin` (ownership released), cb-controller MUST omit it from the next POST — that's how orb (and orbital) learn the divergence is resolved.

### Empty array = no divergence

`{"overrides": []}` is valid and means "no local overrides currently." Orb publishes a snapshot with empty entries; orbital interprets that as "all divergence resolved for this DC."

## How K8s field paths get translated to orbital orbId + field

**Settled — cb-controller owns the translation. Orb is a pipe.**

This reverses an earlier draft of this contract. See `~/.claude/projects/-Users-daniel-armada-orbital/memory/feedback_orb_orbital_agnostic_of_configbundle.md` for the principle: orb and orbital are configbundle-agnostic; translation between producer-native and orbital-native lives in the producer ecosystem. See also [`docs/reference/DIVERGENCE-INTAKE.md`](../reference/DIVERGENCE-INTAKE.md) for orb's intake contract.

### What cb-bundler must do (cloud side, build time)

When producing a ConfigBundle artifact, cb-bundler creates a **new OCI layer** in the bundle:

| Layer | Media type | Contents |
|---|---|---|
| `mapping.json` | `application/vnd.armada.configbundle.mapping.v1+json` | Path → orbId map for this bundle |

The mapping is generated from orbital's data — cb-bundler already queries orbital GraphQL when building the bundle, so it knows each ConfigItem's `orbId` and orbital GraphQL `type`. It serializes that knowledge into the layer.

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

Each item carries the **orbital GraphQL type name** of the node identified by `orbId` (e.g. `Server`, `IdracSettings`, `DataCenter`). cb-controller emits this in every divergence-report entry so orbital can dispatch `update{Type}(...)` mutations on Accept (see `divergence-accept-mutation.md`).

### Why flat-list, not nested

Nested formats (`servers: {serviceTag: {...}}`) hard-code each domain's `+listMapKey` field name into the resolver. The flat-list form keeps cb-controller's resolver decoupled from CRD shape — it only does string prefix matching, never inspects the path internals. Each new CRD list (`clusters[]`, `applications[]`, …) requires no resolver change.

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

This is a configbundle-internal convention — both binaries are in the same repo, so enforcing consistency is local. Orb never sees these paths; it only sees the orbital-native entries cb-controller posts.

### Lookup algorithm (cb-controller's resolver)

Given an observed K8s field path `spec.servers[serviceTag=3RK3V64].idrac.sshEnabled`:

```
matched := ""
for each item in localMapping.items:
    if path starts with item.path AND len(item.path) > len(matched):
        matched = item.path
        matchedOrbId = item.orbId
        matchedType  = item.type
if matched is empty:
    log warn (no prefix match); skip this entry — do not include in report
field = path[len(matched)+1:]  // strip the matched prefix + '.'
emit { orbId: matchedOrbId, field: field, type: matchedType }
```

Longest-prefix wins. cb-controller maintains the mapping locally; the lookup is in-process.

### What orb does (edge side, when bundle arrives)

1. Pulls bundle from Zot (existing flow)
2. **Dispatcher routes the mapping layer to cb-controller** — same generic mechanism as the manifest layer. cb-controller registers as the consumer for the mapping media type via `ORB_CONSUMERS`.
3. Graph layers (`data.json.gz`, `schema.gz`) imported to DGraph as today
4. Manifest layer dispatched to cb-controller as today

Orb has **no special case** for the mapping layer. It's just another media type with a registered consumer.

### What cb-controller does (edge side, mapping receipt)

1. Receives the mapping layer via its registered HTTP endpoint (e.g. `POST /mapping` with `X-Orb-Digest` header)
2. Stores it locally keyed by bundle digest — disk, ConfigMap, or in-memory; cb-controller's choice
3. Uses it during the next divergence report tick to resolve paths

### What orb does (intake time, when cb-controller POSTs)

On `POST /api/v1/divergence`:

1. Validate JSON shape and required fields (`orbId`, `field`, `type`, `intendedValue`, `overrideValue`, `who`, `when`)
2. Replace the stored set with the new entries
3. Return 200 with `{"stored": N}`

That's it. No mapping lookup, no path resolution, no bundle awareness. See [`DIVERGENCE-INTAKE.md`](../reference/DIVERGENCE-INTAKE.md).

### What if K8s field name ≠ orbital field name?

Today they match — IdracSpec fields are named identically to IdracSettings fields. The leaf segment of the K8s path is the orbital field name. If they ever diverge, the mapping JSON can carry per-leaf name overrides; the resolver handles them locally in cb-controller, and orb is unaffected (it only sees the post-translation entries).

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

### Why one apply per takeover entry (not whole-manifest)

K8s SSA's `force` flag operates per-request on the apply body submitted — there is **no per-field force inside a single apply call**. Submitting one combined manifest with `force: true` would force ALL conflicts on every field in that manifest, which is not what we want — we only want to wrest ownership of the specific fields the cloud admin chose to force.

The pattern below achieves de-facto per-field force semantics:

1. Apply the bundle's regular config WITHOUT `force` — cb-controller-owned fields update; `local:admin`-owned fields stay put on conflict.
2. For each `spec.takeover[]` entry, submit a **dedicated apply manifest containing only that single field**, with `force: true`:
   ```yaml
   apiVersion: armada.io/v1
   kind: IdracSettings
   metadata:
     name: srv-001-idrac
   spec:
     sshEnabled: true
   ```
3. That narrow apply takes ownership of just `sshEnabled` away from `local:admin`. Other `local:admin`-owned fields on the same object are not touched because they're not in the apply body.

**Critical:** the takeover apply MUST use the same `fieldManager` string as the bundle's normal apply (e.g., `cb-controller`). If it uses a different name, `local:admin` loses ownership but the bundle's normal apply still doesn't own the field — so the next reconcile produces a fresh conflict on a now-unowned field, which is a worse state. Both applies (normal + takeover) must claim the same manager.

Cost: N+1 API calls per bundle when there are N takeovers. Fine at our scale.

cb-bundler calls orbital's `POST /api/v1/divergence/resolutions/:id/consumed` after the bundle is pushed.

### Why we do NOT use SSA shared management (conflict resolution option 3)

K8s SSA offers three conflict resolutions: (1) overwrite + become sole manager (`--force-conflicts`), (2) drop the field from your manifest + give up the claim, (3) match the server's current value + become a **shared** manager (both managers appear in `managedFields` for that field).

We use (1) for Force and (2) for Ignore. **We never use (3).** Reasons:

- **Shared management does not enable writes.** It's a declarative co-existence state — both managers agree on the current value. The next time either tries to change it, the conflict re-fires. It doesn't unblock mutation; it only signals "I also care about this field."
- **Our ownership model is single-owner with explicit handoff.** At any moment exactly one side is canonical: Accept → cb-controller (with new value); Force → cb-controller (reclaimed); Ignore → `local:admin` (cb-controller drops the field). Co-ownership would muddy "who really set this?" in the audit trail.
- **Divergence detection does not need it.** cb-controller observes admin-owned fields by reading `managedFields` regardless of whether cb-controller co-owns them. The observation pipeline is independent of the apply pipeline.
- **K8s designed option 3 for peer-equal controllers** (e.g. HPA + custom autoscaler both legitimately managing `spec.replicas`). Our model is hierarchical — orbital intent is canonical; local override is an exception to be resolved — not peer-equal. If we ever reach for option 3, it's a signal we've drifted from the divergence-resolution model and should flag.

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
