# Server identity: making orbId first-class in ConfigBundle

**Status:** Implemented (2026-06-13)
**Audience:** configbundle maintainers + Orbital maintainers (cross-repo design coordination)

> Implementation complete and verified end-to-end against a local minikube cluster
> with orb's divergence intake. Rebadge scenario (serviceTag mutation with stable
> orbId) confirmed to preserve admin overrides and the same `ServerConfig` child
> CR — no orphans. The orphaned-overrides migration script (Phase 4 below) is
> the remaining gap; safe to defer until first production rollout.

---

## TL;DR

The ConfigBundle CRD identifies servers by `serviceTag` (the Dell hardware tag). Dell re-stamps service tags during board swaps, and the bundle's next emission reflects the new tag. Using a mutable field as the SSA list-map key orphans `ServerConfig` CRs and strands admin overrides whenever a tag changes.

The proposal: promote Orbital's `orbId` — already API-immutable via DGraph's `@id` directive — to a first-class field in the bundle schema, use it as the list merge key for `spec.servers[]`, and continue using `hostname` as the K8s `metadata.name` (for operator readability). The mapping layer shrinks to its real purpose: field-level path-to-orbId resolution for divergence reporting.

This needs Orbital's awareness because: (1) the change relies on `@id` staying on `orbId` and on no custom mutation bypassing DGraph's auto-generated Patch, (2) the bundler must emit orbId for every relevant entity, (3) any future change to orbId format becomes a CRD-version-affecting change.

---

## Context for cross-repo readers

**configbundle** packages a datacenter's intended configuration into an OCI artifact and delivers it to Galleon edge clusters. The artifact contains:

- A **manifest layer** — YAML conforming to the `ConfigBundle` CRD schema (`spec.datacenter`, `spec.servers[]`, etc.)
- A **mapping layer** — JSON pairs of `{path, orbId, type}` so the cb-controller can translate K8s field paths back to Orbital nodes for divergence reporting

The bundler (cloud-side, sidecar with Orbital) queries Orbital's GraphQL API and produces both layers. The cb-controller (edge-side) consumes both via orb's dispatch pipeline. Orb itself is producer-agnostic — it does not know what's inside a layer; it just routes by media type.

`ServerConfig` is a child CR. The cb-controller decomposes `ConfigBundle.spec.servers[]` into one `ServerConfig` per server entry. The `ServerConfig` is the actuation target for the (future) ServerConfig controller, which will push iDRAC settings via Redfish.

---

## Problem

### Current identifier choices

| Surface | Identifier | Notes |
|---|---|---|
| `ConfigBundleSpec.servers[]` listMapKey | `serviceTag` | Dell hardware tag |
| `ServerSpec.serviceTag` | source of truth in spec | |
| `ServerConfig.metadata.name` | `strings.ToLower(hostname)` | child CR's K8s name |
| Mapping layer entries | `path` → `orbId` for datacenter, server, idracSettings | only place orbId is materialized today |
| Divergence reporter | walks managedFields with `serviceTag` to resolve `orbId` | requires mapping cache |

### Why this is fragile

**serviceTag is mutable in Orbital.** When Dell replaces a board under warranty, the new board has a new service tag. Orbital's GraphQL exposes `serviceTag` as a regular String field on `Server` — present in `ServerPatch` and freely updatable via `updateServer(filter:{...}, set:{serviceTag:"..."})`. The bundle's next emission reflects the new tag.

**Hostname is also mutable.** Same mechanic — `hostname` is a regular String field, included in `ServerPatch`, freely updatable.

**`orbId` is API-immutable by construction.** Orbital's schema declares `orbId: String! @id @search(by: [hash])` on the `ConfigItem` interface. DGraph's GraphQL schema generation excludes `@id` fields from the auto-generated `XPatch` input type, so `updateServer(filter:{...}, set:{orbId:"..."})` is rejected at schema-validation time — `orbId` simply isn't a settable field on `ServerPatch`. The only way to "change" an orbId is delete + re-add, which produces a structurally new node with a new auto-generated `id: ID!`. This is the property the proposal relies on.

### Failure modes today

1. **Service-tag rebadge orphans `ServerConfig`.** New manifest has new serviceTag; SSA treats it as a different list entry; old `ServerConfig` remains until parent delete; admin overrides on the old entry are stranded.

2. **Hostname rename orphans `ServerConfig`.** Same mechanic at the metadata.name layer — old SC by old hostname, new SC by new hostname.

3. **Cross-system correlation requires a lookup.** When Orbital alerts mention `colo:srv-001`, K8s operators can't grep for it directly — orbId lives only in the controller's in-memory mapping cache.

4. **Cold-start fragility for divergence reporter.** If the reporter ticks before the mapping layer has been dispatched (race window), it cannot resolve any orbId and skips the tick. Server-level identity recovery depends on cache availability.

---

## Proposed design

### Schema changes

Add `orbId` as a required field at each addressable level.

```go
// ConfigBundle represents a datacenter's intended configuration.
type ConfigBundleSpec struct {
    // OrbID is the immutable Orbital identifier of the datacenter node.
    // +kubebuilder:validation:Required
    OrbID string `json:"orbId"`

    // Datacenter is the human-readable datacenter name (matches ConfigBundle CR name).
    // +kubebuilder:validation:Required
    Datacenter string `json:"datacenter"`

    // Servers is the list of server configurations.
    // +listType=map
    // +listMapKey=orbId             ← changed from serviceTag
    Servers []ServerSpec `json:"servers,omitempty"`

    Takeover []TakeoverEntry `json:"takeover,omitempty"`
}

type ServerSpec struct {
    // OrbID is the immutable Orbital identifier (e.g. "colo:srv-001").
    // Identity key for SSA list merge. Never changes for the same physical server,
    // even when serviceTag is re-stamped or hostname is renamed.
    // +kubebuilder:validation:Required
    OrbID string `json:"orbId"`

    // ServiceTag is the Dell hardware tag. Mutable (board swaps).
    // +kubebuilder:validation:Required
    ServiceTag string `json:"serviceTag"`

    // +optional
    Hostname *string `json:"hostname,omitempty"`

    // +optional
    OobIP *string `json:"oobIP,omitempty"`

    // +optional
    Idrac IdracSpec `json:"idrac,omitempty"`
}
```

**No changes** to `IdracSpec` (or any future nested struct). Nested types are not independently addressable in K8s — they don't need spec-level orbId. The mapping layer continues to carry their orbIds for field-level divergence reporting.

### Decomposition: propagate OrbID to ServerConfig

`ServerConfig` (the child CR materialised one-per-server by the decomposition reconciler) carries the same OrbID as its parent's `spec.servers[].orbId`:

```go
type ServerConfigSpec struct {
    // OrbID is the immutable Orbital identifier (mirrors ConfigBundle.spec.servers[].orbId).
    // +kubebuilder:validation:Required
    OrbID string `json:"orbId"`

    ServiceTag string  `json:"serviceTag"`
    Hostname   *string `json:"hostname,omitempty"`
    OobIP      *string `json:"oobIP,omitempty"`
    Idrac      IdracSpec
}
```

The cross-system grep argument — operators receiving an Orbital alert for `colo:srv-001` need to find the relevant K8s object — applies to both CRs. On the parent: `kubectl get cb -o yaml | grep colo:srv-001`. On the child: `kubectl get serverconfig -o yaml | grep colo:srv-001`. Without OrbID on the child, the second lookup requires a hostname/serviceTag round-trip through the parent.

The actuation target (ServerConfig controller) also benefits: when it writes Redfish telemetry to logs/events, including `orbId` makes those events directly correlatable with Orbital audit rows. Without it the controller would either need to look up its parent (extra Get) or emit only hostname/serviceTag, which loses the rebadge stability the rest of the design preserves.

Listed as a printer column on `ServerConfig` for `kubectl get serverconfig -o wide` visibility (alongside Hostname and OobIP, which are already at `priority=1`).

### What stays the same

- `ServerConfig.metadata.name` remains `lowercase(hostname)`. K8s names are an operator-facing surface; matching the day-to-day mental model (operators type hostnames) outweighs the rare orphan-on-rename cost. This matches the K8s precedent for hardware-tied resources (`Node` uses hostname-like names; the immutable identifier sits in `spec.providerID`).
- The OCI bundle structure (3 layers: graph data, schema, manifest+mapping) is unchanged.
- Orb's dispatch model (producer-agnostic, media-type routing) is unchanged.
- The mapping layer still exists but shrinks scope.

### What the mapping layer becomes

Today the mapping layer carries:

```json
{"path": "spec", "orbId": "colo:colo-galleon", "type": "DataCenter"}
{"path": "spec.servers[serviceTag=JQK3V64]", "orbId": "colo:srv-001", "type": "Server"}
{"path": "spec.servers[serviceTag=JQK3V64].idrac", "orbId": "colo:srv-001-idrac", "type": "IdracSettings"}
```

After this change:

```json
{"path": "spec.servers[orbId=colo:srv-001].idrac", "orbId": "colo:srv-001-idrac", "type": "IdracSettings"}
```

- Datacenter entries removed (now `spec.orbId`)
- Server entries removed (now `spec.servers[].orbId`)
- Only field-level entries remain (the parts that aren't independently addressable in K8s)

The mapping layer's job becomes singular: **resolve per-field paths to their owning Orbital node for divergence reporting**.

---

## How the user experience changes

### Operators using `kubectl`

`kubectl get` and `kubectl describe` — no functional change. Hostnames still drive the kubectl name surface.

`kubectl get -o yaml` shows two new fields (`spec.orbId`, `spec.servers[].orbId`). Operators may or may not care.

`kubectl apply --server-side --field-manager=local:admin -f -` (admin override) — the YAML must now identify the target server by `orbId` instead of `serviceTag`, because `orbId` is the listMapKey:

```yaml
spec:
  servers:
  - orbId: colo:srv-001        # ← was: serviceTag: JQK3V64
    idrac:
      sshEnabled: true
```

This is the ergonomic cost. ServiceTag is on the physical chassis; orbId lives in the CR spec (and in Orbital's database). An operator standing in front of a server can read the tag; they cannot read the orbId. The pure-kubectl workflow becomes two steps instead of one:

```bash
# 1. resolve hostname → orbId from the CR itself (no orbital lookup needed)
ORB_ID=$(kubectl get cb colo-galleon -o jsonpath='{.spec.servers[?(@.hostname=="r09-u22.colo-galleon")].orbId}')

# 2. apply the override
kubectl apply --server-side --field-manager=local:daniel -f - <<EOF
apiVersion: armada.ai/v1
kind: ConfigBundle
metadata:
  name: colo-galleon
  namespace: default
spec:
  servers:
    - orbId: $ORB_ID
      idrac:
        sshEnabled: true
EOF
```

Because orbId is first-class in the spec, the lookup is a single `jsonpath` query against the CR itself — no `kubectl exec`, no orbital UI, no out-of-band tools. This is the supported workflow.

### Optional convenience: CLI wrapper

A thin wrapper (e.g. `cb-override --hostname r09-u22.colo-galleon --field idrac.sshEnabled=true`) can collapse the two-step kubectl flow into one command, with variants accepting `--service-tag` or `--orb-id` for different operator habits. Useful polish; not a rollout blocker.

### Cross-system correlation gains

When Orbital alerts say `server colo:srv-001 has stale config`:

```bash
kubectl get cb colo-galleon -o yaml | grep colo:srv-001
# ← finds the server entry immediately
```

Today this requires an extra hop through the mapping layer or a hostname-via-orbital lookup.

---

## Trade-offs

| | Today (serviceTag-as-key) | Proposed (orbId-as-key) |
|---|---|---|
| Rebadge → admin overrides survive? | ❌ overrides orphaned | ✅ preserved |
| Hostname rename → server entries survive? | ✅ (only metadata.name churns) | ✅ (only metadata.name churns) |
| Operator types in admin SSA | serviceTag (on chassis) | orbId (database lookup) |
| `kubectl` daily UX | unchanged | unchanged |
| Cross-system correlation | mapping-layer dependent | direct grep |
| Schema complexity | minimal | +1 required field on CB, +1 on Server |
| Mapping layer scope | mixed (entity + field) | tight (field only) |
| Divergence reporter cold-start | requires mapping cache for any identity | server identity from spec |
| Migration cost | n/a | one-time bundler+controller redeploy, re-import, possible admin override re-apply |

---

## Migration plan

1. **Orbital coordination (this doc + sign-off):** confirm the schema-level dependencies in [Open questions for Orbital](#open-questions-for-orbital) — primarily that `@id` stays on `orbId` and no custom mutation bypasses DGraph's auto-generated Patch. The GraphQL response already includes `orbId` on every Server, IdracSettings, and DataCenter node — verified in `internal/bundler/orbital.go:72-81`.

2. **Bundler change (configbundle repo):**
   - Emit `spec.orbId` and `spec.servers[].orbId` in the manifest YAML.
   - Drop server/datacenter entries from the mapping layer (or leave for one release as fallback).
   - Continue to emit field-level mapping entries (idrac.*).

3. **Schema change (configbundle repo):**
   - Add `OrbID` field to `ConfigBundleSpec` and `ServerSpec`, marked Required.
   - Change `+listMapKey=serviceTag` → `+listMapKey=orbId` on `ServerSpec`.
   - Regenerate CRD.

4. **Controller change (configbundle repo):**
   - Divergence reporter reads `ServerSpec.OrbID` directly (skip mapping lookup for server identity).
   - Mapping layer parser continues to handle field-level entries only.
   - Path-format updates: `spec.servers[serviceTag=X]` → `spec.servers[orbId=Y]` in any string-pattern code. Touch points: `extractAdminPaths` and `walkFields` in `divergence_reporter.go` (path emission); `extractServiceTag` and `buildTakeover` in `bundler/handler.go` (takeover round-trip — see "Takeover pipeline impact" below).

5. **(Optional) CLI wrapper:** a thin convenience tool that takes `--hostname`, `--service-tag`, or `--orb-id` and emits the correct admin YAML can be shipped as a polish item. **Not a rollout blocker** — pure kubectl is a fully viable workflow because the orbId is first-class in the spec and trivially queryable:
   ```bash
   ORB_ID=$(kubectl get cb colo-galleon -o jsonpath='{.spec.servers[?(@.hostname=="r09-u22.colo-galleon")].orbId}')
   kubectl apply --server-side --field-manager=local:daniel -f - <<EOF
   ...
       - orbId: $ORB_ID
         idrac: { sshEnabled: true }
   EOF
   ```
   Two-line workflow. The wrapper collapses it to one — useful, but operators who already script kubectl don't need it.

6. **Cluster migration:**
   - Deploy bundler + controller together.
   - Force one re-import per datacenter; this populates orbId fields on all CRs.
   - **Run the orphaned-overrides migration script** (see below) to translate every existing `local:*` managedFields entry from the old `serviceTag` key to the new `orbId` key. Without this, admin overrides applied before the migration are silently orphaned — the K8s API server doesn't error, the field manager just loses ownership against an entry that no longer exists by its old key.

7. **CRD version:** the listMapKey change is structurally significant — K8s itself uses the listMapKey for SSA merge logic, and existing managedFields entries reference the old key. The API server doesn't refuse the change but the SSA semantics shift mid-flight. Treat the rollout as schema-affecting (not "non-breaking") and consider a CRD version bump (`v1` → `v1beta2` or `v2alpha1`) to signal the identity-contract change to downstream consumers.

### Orphaned-overrides migration script

Required deliverable in the same release as the schema change. Script behavior:

```text
for each ConfigBundle CR in every cluster:
  for each managedFields entry where manager starts with "local:":
    for each owned field path matching spec.servers[serviceTag=X].<rest>:
      lookup orbId for serviceTag X (from the same CR's spec, post-reimport)
      re-apply the field via SSA with the same field manager,
        targeting spec.servers[orbId=Y].<rest>
      record the translation in a migration audit log
    after all fields translated for that manager:
      release ownership of the old serviceTag-keyed paths
        (apply with the old paths removed, --field-manager same as before)
```

Output: per-cluster migration audit log listing every translated override, every manager affected, and any unresolved paths (e.g. an override on a server that no longer exists in the new manifest). Operators must review unresolved entries before deleting the audit log.

Without this script the migration is operationally risky: divergence reports based on old-key overrides may be lost during the rollout window, with no automated audit trail.

---

## Open questions for Orbital

The configbundle team needs Orbital to confirm or weigh in on:

1. **Schema-level immutability dependencies.** `orbId` is API-immutable today via the `@id` directive (DGraph excludes `@id` fields from auto-generated `XPatch` types, so `updateServer(set:{orbId:"..."})` is a schema-validation error). Configbundle takes a hard dependency on this. Confirm:
   - `@id` will stay on `orbId` across future schema revisions — removing it would silently re-enable mutation.
   - No custom GraphQL mutations bypass DGraph's auto-generated Patch by allowing `orbId` to be set on existing nodes.
   - Backup/restore flows preserve `orbId` values (rather than re-keying by auto-generated `id: ID!`).
   - There is no roadmap item that would force a one-time global re-keying (e.g. tenant rename, namespace consolidation).

2. **OrbId format stability.** Today server orbIds follow `<namespace>:<sequence>` (`colo:srv-001`). If Orbital ever needed to change this format (e.g. switch to UUIDs, or change the separator), every configbundle CR in every cluster becomes affected. Either freeze the format as part of the public contract, or commit to providing a migration tool when it changes. The `:` separator specifically must remain stable since it appears verbatim in CR spec strings and SSA listMapKey lookups.

3. **GraphQL coverage** — already confirmed in `internal/bundler/orbital.go:72-81`. The bundler queries `orbId` on `DataCenter`, `Server`, and `IdracSettings` today. Spec-level usage adds no new GraphQL requirement.

4. **Cross-tenant orbId uniqueness.** OrbIds are unique within a tenant (the `<namespace>:` prefix scopes them). ConfigBundle is single-tenant per CR today, so the listMapKey is unambiguous. If federation across tenants ever lands, document whether two physical servers in different tenants could share the same orbId. Today's answer is "tenant-scoped, no cross-tenant federation" — please confirm.

---

## What this does NOT change

- Orb is still producer-agnostic. No layer dispatch logic moves to orb.
- The mapping layer still exists (field-level only).
- The OCI bundle structure stays at 3 layers.
- The SSA single-owner model is unchanged.
- The takeover pipeline (ADR-006) is unchanged.
- ServerConfig children are still owned by ConfigBundle via OwnerReferences.

---

## References

- ADR-005: Divergence mapping layer — establishes the mapping layer's purpose
- ADR-006: Divergence takeover pipeline — depends on serviceTag-based path encoding (would need path-format update)
- ADR-007: SSA pointer fields — pointer-field convention applies to new orbId fields as well (orbId is required, so it's a value type)
- Cluster API `Machine.spec.providerID` — analogous pattern (immutable cross-system identifier in spec, mutable name in metadata.name)
- Kubernetes `Node.spec.providerID` — same pattern

---

## Decision status

Proposed pending Orbital review. Not yet implemented.

If Orbital signs off on the open questions, configbundle will:
1. Implement and ship the schema + bundler + controller changes (CRD version bump per migration step 7)
2. Ship the orphaned-overrides migration script alongside the release (required deliverable, per migration plan)
3. Document the migration in the cluster runbook
4. Optionally ship a `cb-override` CLI wrapper as a follow-up polish item (not gating — pure kubectl is a supported workflow)
