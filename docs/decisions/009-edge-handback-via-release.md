# ADR-009: Local release auto-reverts to last-imported intent

**Date:** 2026-06-17
**Status:** accepted

---

## Context

After ADR-008, the consume pipeline supports two outcomes for a divergent field:

- **Takeover (Accept/Reject)** — cb-controller force-claims, evicting the local:* manager. Value lands at orbital intent (Reject) or at the previously-overridden value the cloud admin adopted (Accept).
- **Ignore** — cb-controller bows out unconditionally. Local:* manager retains ownership; value stays at override.

A third outcome — *the local admin themselves decides to give back the field* — was unsupported. The current consume pipeline makes "release while Ignore is active" an architecturally tolerated but inert state:

1. local:admin SSA-releases the field (re-apply with the field omitted).
2. managedFields no longer carries a local:* claim on the field.
3. Field's value stays at the override value (SSA release-on-omit changes ownership, not values).
4. `spec.Ignored[]` still carries the directive.
5. The defensive sweep at `consume.go:491-497` keeps cb-controller out of the field on every subsequent reconcile.

The result is an **orphan**: a field that no manager claims, whose value matches nobody's intent. local:admin gave it up; cloud admin never adopted it; cb-controller is forbidden by the defensive strip from picking it up.

The orphan state violates the invariant *every field's value has an explicit owner whose intent matches that value*. The defensive strip codifies the violation rather than resolving it.

Three earlier framings were considered and rejected:

- *"Cloud admin must re-decide (Accept/Reject)"* — true today, but couples the edge admin's give-back to a cloud-side action, defeating the purpose.
- *"Local admin must explicitly post a handback to an HTTP endpoint"* — adds new protocol surface; doesn't fit `kubectl apply`-driven workflows.
- *"Defer to the next bundle import — the defensive strip's removal alone fixes it"* — correct but operator-paced. A release-then-wait-hours model is not the user expectation behind "give-back."

The right primitive is to make the release itself load-bearing: SSA release of a local:* claim is the signal; cb-controller acts on it directly.

---

## Decision

**A local:* manager releasing a field — when no other local:* manager still claims it — is a binding signal that the override has ended. cb-controller MUST reclaim the field and restore the value to the last-imported intent.**

This applies **regardless** of whether `spec.Ignored[]` contains the field. Two cases collapse into one rule:

| Scenario | Pre-release | After release (ADR-009) |
|---|---|---|
| Pending divergence (no resolution yet), edge admin gives up | local:admin owns, value = override; cb-controller bowed out | cb-controller sole owner, value = intent |
| Ignore resolution active, edge admin retracts | local:admin owns, value = override; spec.Ignored has the entry | cb-controller sole owner, value = intent; spec.Ignored entry becomes inert |

Both express the same operator intent: *stop holding this field; restore cloud intent.*

---

## Rationale

**Closes the orphan state.** A field with no local:* claim and value ≠ intent is an architecture violation. ADR-009 establishes that cb-controller MUST detect and rectify such states.

**Honors the operator's intent.** A local:* SSA release is a deliberate act. Today, that act has no value-level effect. With this ADR, the release becomes load-bearing.

**Aligns with the edge-driven sovereignty model.** Cloud admin remains the superuser of intent values. Local admin's authority is bidirectional: override (claim with override value) and give-back (release, returning to intent). Both are unilateral edge actions, both reversible via the next cloud publish.

**Reuses existing infrastructure.** The trigger is a managedFields-change watcher — the divergence reporter already has one (`divergence_reporter_controller.go:206-247`). The action is a replay of the last-imported manifest through `applyManifest`. The intent value lives in the existing `last-applied-spec-<cb>` ConfigMap (`consume.go:357-360`). No new protocol surface; no orbital callback; no new CRD field.

**The defensive strip becomes wrong.** The sweep at `consume.go:491-497` is the *only* code in the system that gives `spec.Ignored` semantic weight without an active local:* claim. Every other path — `omitAdminOwnedFields`'s ignoredSet branch, the divergence reporter's adminPaths walk, the takeover release pass — gates on a local:* claim existing. The defensive strip is an outlier; deleting it restores the model's internal consistency.

---

## Scope: any local:* release, not just Ignored fields

The decision applies to **any** release of a local:* claim, not only releases of fields in `spec.Ignored`. Rationale:

- The only reason a local:* manager owns a field is intentional override. Release = no more override = restore intent.
- Gating reclaim on `spec.Ignored` membership creates two divergent semantics for "release," which operators would have to mentally track ("did the cloud admin Ignore this yet? then release means X; otherwise release means Y").
- The single rule is robust to ordering races: local admin can release before, during, or after the cloud admin's resolution decision; the controller's reaction is the same.

The "cloud admin might be mid-decision when edge admin releases" race is a real concern in either model — cloud-admin's Accept already races local-admin's re-override today. Adding "Accept races edge-release" doesn't materially worsen the model.

---

## Trigger predicate semantics

The new ReclaimController predicate fires when:

> Field X had at least one local:* claim in the *old* managedFields slice, and zero local:* claims in the *new* managedFields slice.

This distinguishes:

- **Release** — no local:* manager left holding the field → reclaim.
- **Rotation** — local:admin released but local:bob claimed the field same-transaction → not a release; override survives under the new manager.
- **Claim** — local:* manager appears where none was → reporter's job, not reclaim's.
- **Modify** — local:* manager's claimed value changed → reporter's job, not reclaim's.

The existing `localManagersChanged` (`divergence_reporter_controller.go:225-247`) is too coarse — returns boolean "anything changed." Reclaim needs a richer diff: "for each field with a prior local:* claim, is there *any* local:* claim now?"

---

## What this changes in code

| Change | File | Effect |
|---|---|---|
| Delete defensive Ignored sweep | `consume.go:491-497` | `spec.Ignored` no longer affects fields with no local:* claim |
| Delete unused helper | `consume.go:567-595` `stripIgnoredField` | Garbage-collected |
| Invert the regression test | `consume_test.go:410-442` | New assertion: Ignored + no local:* claim → cb-controller claims with intent |
| Add ReclaimController | `internal/controller/reclaim_controller.go` (new) | Watches managedFields, replays manifest on release |
| Wire into manager | `cmd/main.go` | Symmetric with DivergenceReporter registration |
| Scrub stale Ignored at apply time | `consume.go:applyManifest` + `collectLocalClaimedKeys` / `filterActiveIgnored` helpers | `spec.Ignored` never carries entries without a matching local:* claim |

`TestOmitAdminOwnedFields_IgnoredAlwaysOmittedEvenOnValueMatch` (`consume_test.go:366`) is **unchanged** — the Ignore-plus-active-local-claim case still triggers unconditional bow-out.

---

## Edge cases

- **Field released, value already equals intent.** Replay's apply is a value no-op for that field; ForceOwnership claims it. Steady state restored. Harmless.
- **No `last-applied-spec` ConfigMap** (controller cold-started before any consume). Reclaim path logs and returns; next bundle import populates the ConfigMap. The release stands as released-with-no-action — value frozen, no owner. Recovers on next bundle.
- **Concurrent release and bundle import.** Both replay the same manifest. Idempotent. SSA serializes via resourceVersion.
- **Rotation: local:admin → local:bob in one transaction.** Predicate sees a local:* manager still on the field → does not fire. Override survives under local:bob.
- **Stale `spec.Ignored` entry after reclaim.** Scrubbed in `applyManifest` — see invariant below.

## Invariant: `spec.Ignored` only carries entries with an active local:* claim

Ignore is meaningless without an active override (see "Why the claim is sound" framing in [the upstream discussion captured in this ADR's context]). cb-controller enforces this invariant at every apply:

```go
// in applyManifest, after fetching cb to read managedFields:
claimed := collectLocalClaimedKeys(cb.ManagedFields)
spec.Ignored = filterActiveIgnored(spec.Ignored, claimed)
```

`collectLocalClaimedKeys` walks managedFields and produces a set of `<serverOrbId>|<field>` keys for every leaf owned by at least one `local:*` manager. `filterActiveIgnored` drops `IgnoredEntry` rows whose tuple has no matching claim. The filter runs on **every** apply — fresh bundle imports AND reclaim replays — so the CR's `spec.Ignored` never carries entries with no corresponding override.

**Why this is enforced at write time, not in the bundler.** The bundler emits `spec.Ignored` from orbital's resolution table. Orbital can lag in cleaning up rows after an edge handback (it may not even know the field was handed back until the next divergence report arrives). So the bundle as-built may carry stale entries. cb-controller is the last writer and has the live managedFields — it's the right place to enforce the invariant. The bundler stays simple; orbital's cleanup is best-effort.

**Consequence for orbital.** When orbital's divergence ingestion observes "divergence entry disappeared from a report" (resolved-by-disappearance), it should also clean up any associated `ignore` resolution row. Otherwise the row persists in orbital's DB while the bundle never re-imports it on the edge (cb-controller filters it). Tracking issue: orbital-side cleanup of orphan Ignore resolutions.

---

## Consequences

**Positive:**

- "Release" becomes semantically load-bearing — it means "I give back this field; restore intent."
- The orphan state (no claim, value ≠ intent) is no longer reachable.
- `spec.Ignored` entries that outlive their precondition cause no architectural damage — cb-controller now owns the field with intent; orbital can clean up the resolution row asynchronously.
- Operators perform handback via vanilla `kubectl apply --field-manager=local:admin --server-side` with the field omitted. No new endpoint, no annotation, no out-of-band signal.

**Negative:**

- Write amplification: one SSA Apply per release event (a full manifest replay). Acceptable — release is operator-initiated, not high-frequency. Debounce can be added if release-burst patterns emerge.
- Brief flicker observable in `kubectl get cb -w` between release and reclaim. SSA's resourceVersion serialization keeps state consistent; the flicker is cosmetic.
- Orbital's resolution table can accumulate dead Ignore rows (handed back at the edge). Cleanup is a separate concern; not blocking correctness.

**Neutral:**

- One new controller-runtime controller (ReclaimController), symmetric with DivergenceReporter.
- The replay reuses the full `applyManifest` pipeline including takeover. If `spec.Takeover` is non-empty when replay fires, the takeover pass re-runs idempotently.

---

---

## Related

- **ADR-006** — Takeover pipeline ordering. Reclaim reuses the apply pipeline but never originates takeover entries.
- **ADR-008** — Release stale ownership claims via SSA-as-manager. ADR-008 uses SSA release-on-omit *from controller perspective* (controller writes other-manager's body without the takeover target). ADR-009 uses SSA release-on-omit *from local admin's perspective* (local admin writes a body without the field) — symmetric inverse.
- `docs/plans/divergence-cb-controller-contract.md` — divergence pipeline that already watches managedFields changes; share the watcher infrastructure.
- `consume.go:491-497` — the defensive strip this ADR deletes.
