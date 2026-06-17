# ADR-008: Release stale ownership claims via SSA-as-manager

**Date:** 2026-06-15
**Status:** accepted

---

## Context

The takeover pipeline (ADR-006) applies controller intent to fields the cloud admin marked for force-resolution. The mechanism is K8s Server-Side Apply with `client.ForceOwnership`. For the **reject** case (cloud intent value differs from local override), force-conflicts works: a value conflict triggers ownership transfer, `local:admin`'s claim on the field is removed, the field becomes solely owned by `configbundle-controller`.

The **accept** case is different. Orbital's accept-divergence semantic means the cloud admin has decided to adopt the local override into intent. After the next export, the manifest's intent value for that field matches the local override. When `processTakeover` does the force-conflicts Apply, K8s SSA sees no value conflict (both managers want the same value), and **force-conflicts is a no-op for ownership transfer.** The result is **shared ownership** — both `local:admin` and `configbundle-controller` appear in the field's managedFields entries.

Shared ownership is functionally benign — the divergence-reporter short-circuits on `reflect.DeepEqual(intended, override)` (`divergence_reporter.go:145`) so the loop converges. But it violates orbital's divergence-semantic contract:

> Cloud admin is the superuser. Accept = orbital adopts override into intent. Reject = orbital intent wins. Ignore = orbital disengages.
>
> **Only `ignore` retains local ownership. Accept and Reject MUST release the local claim.**

Without an explicit release pass, an accept-resolved field looks identical in managedFields to an actively-divergent field — `local:admin` still listed as a co-owner. This breaks local-admin tooling that introspects ownership (`kubectl get cb ... --show-managed-fields | grep 'local:'` is the documented way to audit pending overrides).

## Decision

After a successful takeover Apply, run a second step (`releaseOtherClaims`) that uses the **K8s-documented "transferring ownership between managers" protocol** ([upstream docs](https://kubernetes.io/docs/reference/using-api/server-side-apply/#transferring-ownership-between-managers)) to release stale claims. The protocol has three steps:

1. Manager A (`local:admin`) owns field F.
2. Manager B (`configbundle-controller`) Applies F with the **same value** → both managers share ownership of F.
3. Manager A re-Applies a body that **omits F** → SSA's release-on-omit rule strips A's claim on F. A's other claims (fields still in the new body) persist. B becomes sole owner.

Steps 1–2 happen earlier in `processTakeover` (the force-conflicts Apply). This step runs step 3 for every non-self manager that currently claims any takeover target. cb-controller submits the Apply with `client.FieldOwner(<other manager>)` — K8s does not authenticate the FieldManager string, and this delegation pattern is the recommended mechanism when a controller mediates ownership transfer.

The Apply body is reconstructed: for each non-self manager that claims at least one takeover-target field, build a `ConfigBundle` body containing all of that manager's currently-claimed fields with current live values, EXCEPT the takeover targets. Submit as `client.Apply` with `FieldOwner=<that manager's name>`. The release-on-omit rule then strips just the takeover-target claims.

## Rationale

**Why not just rely on shared ownership.**

Functionally fine for orbital's loop, but breaks the project's stated ownership semantic. Orbital UI and `kubectl` introspection treat managedFields as the source of truth for "who claims this field." A `local:admin` entry that survives a cloud-admin accept is a stale claim with no semantic owner — confusing for operators, breaks `kubectl get cb -o yaml --show-managed-fields | grep 'local:'` workflows.

**Why SSA-as-manager (the documented protocol) rather than JSON Patch on managedFields.**

A JSON Patch with `op: remove` against `/metadata/managedFields/N/fieldsV1/...` also achieves the strip and is shorter to implement. We considered it and rejected it: the K8s docs explicitly warn against editing managedFields directly, and the documented mechanism for the exact use case (transferring ownership between managers) is precisely what we need. SSA-as-manager works through the K8s API server's intended protocol — same audit trail K8s expects, same code path other controllers use, no special cases for managedFields handling.

The tradeoff is implementation complexity: the Apply body must be reconstructed from each manager's current claims (live spec values at the paths they own), then submitted as that manager. The body must:

- Include the listMapKey (`orbId`) for server entries where the manager retains at least one non-takeover claim (so SSA can match the entry and preserve those claims).
- Include all the manager's *other* claimed fields with their current live values (so release-on-omit doesn't strip claims the manager legitimately retains — pending divergences, Ignore-resolved fields).
- OMIT the takeover-target fields (so release-on-omit strips just those).
- OMIT the entire server entry — including its listMapKey — when *every* leaf the manager held on that server was a takeover target. Including just `{orbId: X}` would preserve the listMapKey claim and leave a residual "manager touched this entry" marker in `managedFields`, violating the "Accept/Reject = full release" semantic. With the entry absent from the body, SSA's release-on-omit strips the entry-presence marker (`".":{}`) and the listMapKey (`f:orbId:{}`) too — leaving zero residual ownership for that server. (See **Refinement** below.)
- NOT include any field the manager doesn't own (so this Apply doesn't extend the manager's claims). In particular, do **not** include `serviceTag` even though it's CRD-Required: K8s validates Required-fields against the merged final state, not the individual Apply body. cb-controller's own claim on `serviceTag` keeps it present in the object.

### Refinement (2026-06-15): full-release for entries with no surviving claims

The first version of `reconstructServerList` always included `{orbId: X}` for any server the manager touched, on the theory that the listMapKey was structurally required for SSA to identify the entry. That was correct *if* you wanted to preserve claims on other leaves of that entry — but wrong when **all** of the manager's leaves on that entry were takeover targets. In that case the entry remained in the release body as `{orbId: X}`, SSA recorded "manager owns orbId + entry-presence marker," and `kubectl get cb --show-managed-fields | grep local:admin` kept showing the manager even though every meaningful claim had been released.

Operationally this broke the documented audit invariant: "surviving `local:*` entries indicate actual unresolved or Ignore-resolved overrides." The residual listMapKey claim was a false positive.

The fix in `reconstructServerList`:

```go
entryTouched := reconstructServerEntry(newEntry, srcEntry, keyOwnedMap, excludedFields)
if len(newEntry) == 1 {  // only orbId, no leaves left
    continue              // omit the entry entirely; SSA's release-on-omit
                          // strips the listMapKey + entry-presence marker too
}
out = append(out, newEntry)
```

Note this is consistent with orbital's three-action semantic: a server fully resolved through Accept and/or Reject leaves no residue. A server with at least one Ignore-resolved or pending field retains its listMapKey claim *because* it retains real claims on that entry — the listMapKey is then load-bearing for those other claims.

**Companion fix: omit `spec` from the release body when `newSpec` is empty.**

The entry-level fix above only addressed the *contents* of `spec.servers[]`. When every server entry was fully-released (all servers in the manager's claims had only takeover-target leaves), `reconstructApplyExcluding` returned `newSpec = {}`. The Apply still included `"spec": {}`, which made SSA record `f:spec: {}` for that manager — a top-level "I claim the spec object itself" marker. `kubectl --show-managed-fields | grep local:admin` then *still* reported the manager, even though every individual leaf, listMapKey, and entry-presence marker had been released.

The fix in `releaseOtherClaims`:

```go
applyObj := map[string]any{
    "apiVersion": ...,
    "kind":       "ConfigBundle",
    "metadata":   map[string]any{"name": ..., "namespace": ...},
}
if len(newSpec) > 0 {
    applyObj["spec"] = newSpec  // include only when non-empty
}
```

With `spec` absent from the Apply body, SSA's release-on-omit strips the `f:spec` claim along with everything under it. The manager entry disappears from `managedFields` entirely. This closes the audit-invariant gap for the "all servers fully released" case.

The Apply must use an `unstructured.Unstructured` body rather than a typed `armadav1.ConfigBundle` — the typed struct would serialize `spec.datacenter` as an empty string (no `json:omitempty`), which would extend the manager's claims to a field cb-controller already owns and fail with a conflict.

**Why this preserves orbital's three-action semantic.**

| Action | Cloud intent | Local claim after takeover |
|---|---|---|
| **Accept** | Adopts override value | Released (this fix) |
| **Reject** | Keeps intent value, overrides edge | Released (force-conflicts in step 1, value differs) |
| **Ignore** | Disengages from field | Preserved — field is omitted from the bundle entirely, never appears in `spec.takeover[]`, never reaches this release pass |

Pending (un-resolved) divergences are also preserved: a field with no resolution row in orbital is not in `spec.takeover[]`, so the manager's claim stays intact.

**Non-fatal on failure.**

If the release fails (rare: concurrent reconcile, CRD validation regression), `processTakeover` logs the error but returns success. The takeover apply itself succeeded — values are correct. Reporting the release failure as a hard error would mask the successful value transfer. The next bundle re-runs takeover, which re-attempts the release.

### Refinement (2026-06-16): Ignore via spec.ignored[], not bundler omission

Initial design had the bundler nil out (omit) ignored fields entirely, relying on cb-controller's "if not in apply, don't claim" behavior to preserve local:* ownership. That worked for the steady state, but two problems surfaced:

1. **Divergence-reporter could not see ignored fields** because lastManifest didn't contain the intent value to compare against. Reporter skipped them via `intendedAbsent`. Orbital then loop-closed the entry (no incoming report → entry+resolution deleted on next ingest), bundler stopped emitting the omission, cb-controller silently auto-claimed the field, and the Ignore decision was undone without any signal.
2. **Operator framing.** Ignore should not be a "fire and forget" mute. It's a deliberate "leave to edge" decision that must remain visible — overrides keep being surfaced so the operator can re-decide if circumstances change.

The fix is a new CRD field `spec.ignored []IgnoredEntry`, parallel to `spec.takeover[]`:

```yaml
spec:
  servers:
    - orbId: colo:srv-1
      idrac:
        racadmEnabled: true     # intent value STAYS in spec
  ignored:
    - serverOrbId: colo:srv-1
      orbId: colo:srv-1-idrac
      field: racadmEnabled       # but don't claim it
```

cb-controller's reconcile per-field decision tree now:

| State | Action |
|---|---|
| Field in `spec.ignored[]` | OMIT from apply unconditionally — local:* retains regardless of value match |
| Field in `spec.takeover[]` | KEEP + force-claim regardless of value match |
| local:* claims AND values match | KEEP (auto-claim silently evicts local:*) |
| local:* claims AND values differ | OMIT (bow out, preserve override) |
| no local:* claim | normal apply |

The divergence-reporter no longer skips ignored fields (it has the intent value from lastManifest's preserved `spec.servers[N].<field>` and reports any local:* claim with value mismatch). Orbital sees the entry in every report, so loop closure never fires for an Ignore — the entry + resolution persist until something else changes.

Pinned by `TestBuildIgnored_IntentValuesStayInSpec` (bundler emits the directive without nilling values) and `TestOmitAdminOwnedFields_IgnoredAlwaysOmittedEvenOnValueMatch` (controller bows out unconditionally for ignored fields, even when values would otherwise trigger auto-claim).

### Refinement (2026-06-16): no steady-state co-ownership

The original `omitAdminOwnedFields` removed *every* local:*-owned leaf from the controller's apply body. That left the field as `local:*`-only after the apply, and the divergence-reporter would report every local:* claim as divergence — including the case where local:*'s value happened to match cloud intent (e.g., another admin updated intent to match a previously-overridden edge value). The result was a stuck loop: values agreed but ownership stayed split, the reporter kept emitting, no admin action could close the loop without manual Ignore.

The simplification (2026-06-16): cb-controller bows out **only** on genuine value mismatch + no explicit takeover. The three cases per local:*-owned leaf:

| Case | Action | Result |
|---|---|---|
| Field in `spec.takeover[]` | KEEP in apply body | force-conflicts pass claims, evicts local:* (explicit Accept/Reject) |
| intent value == live value | KEEP in apply body | force-conflicts silently claims, evicts local:* (no real conflict) |
| intent value != live value, no takeover | OMIT (bow out) | local:* retains; surfaces as divergence in the next report |

The main consume Apply now always carries `client.ForceOwnership`. Co-ownership is no longer a steady state — every Apply that includes a field claims it. The release-pass logic above is unchanged: still needed to clean up stale local:* manager records after the force-claim transfers ownership.

**Why this avoids the stuck loop:** when another cloud admin's edit causes values to converge while a divergence resolution is still pending, the next reconcile auto-claims the field (values match → KEEP → force) and the local:* manager is evicted. The reporter then sees no co-manager → no further divergence emitted. The pending resolution gets loop-closed by the next divergence report that omits the field.

**Why this preserves the negotiation:**

- Genuine override (values differ) → cb-controller bows out → local:* retains → reporter sees the override + value mismatch → divergence emitted → admin sees and decides.
- Explicit Reject → field appears in `spec.takeover[]` → cb-controller force-claims regardless of value → edge value reverts to intent.
- Explicit Ignore → field omitted from spec entirely (bundler-side concern, unchanged) → cb-controller never claims → local:* sole owner indefinitely.

Pinned by `TestOmitAdminOwnedFields_BowsOutOnValueMismatch`, `TestOmitAdminOwnedFields_KeepsOnValueMatchForAutoClaim`, `TestOmitAdminOwnedFields_KeepsTakeoverTargetEvenOnMismatch`, and `TestOmitAdminOwnedFields_BatchSiblingsOnSameServer` (the user-reported regression: batch Reject sshEnabled + Accept ipmiEnabled on the same IdracSettings — both fields must survive the omit pass).

**Scope of the no-co-ownership rule: VALUE fields only.** SSA's `listType=map` protocol structurally requires any manager that claims a leaf inside a list entry to also claim the entry's `listMapKey` (and entry-presence `.` marker). So `f:orbId` on a server entry, the `.` marker, and struct wrappers like `f:idrac` will appear co-owned by cb-controller and local:* whenever both have any leaf claim on that entry — both write identical values, no SSA conflict, no behavior impact. The reporter ignores these (value comparison passes), the release-pass strips them when the last value-claim is released, and force-apply doesn't need them. The invariant is "no co-ownership of fields whose values drive divergence," not "no co-ownership of any field."

## Consequences

**Positive:**

- Orbital's divergence semantic is enforced at the K8s ownership layer: Accept and Reject release local claims; Ignore preserves them.
- Local-admin tooling (`kubectl get cb ... --show-managed-fields`) becomes a reliable audit of pending overrides — surviving `local:*` entries indicate actual unresolved or Ignore-resolved overrides.
- Uses K8s's documented protocol — no direct managedFields manipulation, no special audit caveats.

**Negative:**

- The controller submits Apply requests as other field managers. K8s allows this (FieldManager is a free string), but the audit trail will show actions attributed to `local:admin` that were physically performed by `configbundle-controller`'s ServiceAccount. This is documented K8s protocol; not a security issue because the ServiceAccount's RBAC governs what writes are allowed, not the FieldManager string. Operators reading audit logs should know that `local:*` Applies originating from cb-controller's SA are ownership-release operations.
- Reconstructing the Apply body requires walking the manager's claimed-paths tree and looking up live values. ~150 lines of map traversal code in `reconstructApplyExcluding` and helpers. Unit-tested in `takeover_test.go`.

**Neutral:**

- Adds N extra Apply calls per dispatch (one per non-self manager that claims a takeover target). Latency cost is small relative to the SSA pass already running. Most dispatches will have 0 or 1 such managers.

## Implementation

`internal/controller/takeover.go`:
- `processTakeover` calls `releaseOtherClaims` after the force-conflicts Apply.
- `releaseOtherClaims` re-fetches the CR, builds an exclusion set from `spec.takeover[]`, walks managedFields for each non-self non-status manager, reconstructs their Apply body via `reconstructApplyExcluding`, and submits as `client.Apply` with `FieldOwner=<that manager's name>`.
- The Apply uses `unstructured.Unstructured` to avoid the typed-struct serialization issue with `spec.datacenter`.

Reconstruction helpers (all in `takeover.go`):
- `reconstructApplyExcluding` — top-level spec rebuild.
- `reconstructServerList` — list-map of servers; preserves the orbId listMapKey ONLY when the entry retains at least one non-takeover leaf; otherwise omits the entry entirely.
- `reconstructServerEntry` — single server entry, handles top-level fields and recurses into idrac.
- `reconstructIdracExcluding` — leaf-level idrac field rebuild with the exclusion check.

Tests: `TestReconstructApplyExcluding` in `takeover_test.go` covers:
- Surgical release: target claim omitted, other claims preserved with live values.
- Manager's only claim is a takeover target: **server entry fully omitted from release body** (releases listMapKey + entry-presence marker too).
- Manager doesn't claim any takeover target: `touched=false` (caller skips the Apply).
- Top-level Server field as takeover target.

Verified end-to-end on minikube 2026-06-15:

**Before takeover:**
```
local:admin claims: {".":{}, "f:idrac":{"f:dhcpEnabled":{}, "f:sshEnabled":{}}, "f:orbId":{}}
```

**Dispatch manifest with `sshEnabled` in `spec.takeover[]`** → controller logs:
```
INFO  takeover  takeover queued                       field=sshEnabled
INFO  takeover  released claims via SSA-as-manager    manager=local:admin
INFO  consume   async apply succeeded
```

**After takeover:**
```
local:admin claims: {".":{}, "f:idrac":{"f:dhcpEnabled":{}}, "f:orbId":{}}
```

`sshEnabled` released. `dhcpEnabled` preserved (Ignore-resolved or pending). No new fields added (no `serviceTag` injection). `f:idrac` still present because `dhcpEnabled` still lives under it. `f:orbId` and `.: {}` markers preserved because the entry retains a real claim (`dhcpEnabled`).

**Refinement scenario (2026-06-15): all claims are takeover targets.**

Before:
```
local:admin claims: {".":{}, "f:idrac":{"f:sshEnabled":{}, "f:ipmiEnabled":{}}, "f:orbId":{}}
```

Dispatch with both `sshEnabled` and `ipmiEnabled` in `spec.takeover[]` (Accept + Reject respectively). After takeover with the full-release fix:

```
$ kubectl get cb colo-galleon -o jsonpath='{.metadata.managedFields[?(@.manager=="local:admin")]}'
# (empty — no managedField entry for local:admin at all)
```

The server entry is omitted entirely from the release body; SSA's release-on-omit strips the listMapKey, the entry-presence marker, and the leaf claims in one pass. Operationally, this is the difference between "the controller technically released the override values but kept structural residue" (confusing) and "the override is fully resolved, end of story" (the intent).

## Related

- ADR-006 — Takeover pipeline ordering. This ADR extends ADR-006 with the release step.
- ADR-005 — Mapping layer. Unaffected.
- Orbital plan `docs/plans/cb-controller-retry-on-conflict.md` — the unrelated RetryOnConflict fix that addresses the 409 on mapping dispatch.
- K8s docs — [Server-Side Apply: Transferring ownership between managers](https://kubernetes.io/docs/reference/using-api/server-side-apply/#transferring-ownership-between-managers)
