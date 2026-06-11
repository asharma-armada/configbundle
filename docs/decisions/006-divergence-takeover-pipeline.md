# ADR-006: Takeover is a second pass in the consume handler

**Date:** 2026-06-11
**Status:** accepted

---

## Context

When a cloud admin "forces" a divergence resolution, the next bundle includes `spec.takeover[]` entries. CB Controller must apply these with `--force-conflicts` to reclaim field ownership from `local:admin`. The design question: where in the consume pipeline does this happen, and what happens if the normal SSA apply fails?

---

## Decision

Takeover runs as a second pass after the normal SSA apply in the consume handler. It runs regardless of whether the normal apply succeeded or failed. Entries remain in `spec.takeover[]` until the next bundle naturally replaces them. Code lives in the consume handler, not the reconciler.

---

## Rationale

**Two-pass ordering — normal apply first, then takeover.**

The normal apply (without ForceOwnership) handles the common case: updating all non-contested fields. Takeover is exceptional — a cloud admin explicitly forcing ownership back on specific fields. Running normal apply first ensures the common path completes before the exceptional path runs.

**Takeover runs even if the normal apply fails.**

Consider: local admin has overridden field A (not in takeover list) and field B (in takeover list). If the normal apply hits a 409 on field A, and takeover is gated on normal apply success, the cloud admin's force decision on field B is blocked by an unrelated override. The cloud admin explicitly decided to reclaim field B — that should succeed independently. The two operations are logically independent.

**Entries stay in `spec.takeover[]` until the next bundle replaces them.**

The consume handler does not mutate `spec.takeover[]` after processing:
- The bundle manifest is the source of truth for the spec. Mutating it violates "cloud authors, edge enforces."
- cb-bundler already marks `DivergenceResolution.cb_consumed = true` after the bundle is pushed. The next bundle will not include consumed entries.
- Idempotency: if orb re-dispatches the same bundle, takeover runs again. `--force-conflicts` on a field the controller already owns is a no-op.

**Code lives in the consume handler, not the reconciler.**

Takeover is triggered by a new bundle arriving (inbound event), not by a CR change event. It changes field ownership on the ConfigBundle CR, which the reconciler then propagates to child CRs via decomposition. Putting takeover in the reconciler would mean the reconciler needs to distinguish "this is the first time I've seen this takeover entry" from "I've already processed it" — unnecessary state tracking.

**Alternatives rejected:**
- Takeover gated on normal apply success: creates coupling between unrelated overrides
- Consume handler removes processed entries from `spec.takeover[]`: violates source-of-truth principle, adds retry complexity for the removal itself
- Takeover in the reconciler: wrong trigger (reconciler reacts to CR changes, not inbound bundles), requires processed-entry tracking

---

## Consequences

**Positive:**
- Cloud admin's force decisions are never blocked by unrelated local overrides
- Consume handler remains the single owner of the apply pipeline (parse → inspect → apply → takeover → status)
- Idempotent: re-dispatching the same bundle is safe

**Negative:**
- If both normal apply and takeover fail, the error reporting must surface both failures (status condition + logs)
- `spec.takeover[]` contains "stale" entries between bundle N (where they appear) and bundle N+1 (where they disappear) — this is by design, not a bug

**Risks:**
- If cb-bundler fails to mark a resolution as consumed, takeover entries persist across multiple bundles. This is safe (idempotent) but wastes one `--force-conflicts` call per tick. Unlikely in practice.

---

## Notes

- Contract: `~/armada/orbital/docs/plans/divergence-cb-controller-contract.md` §"Resolution semantics — Force"
- Consume handler flow after this decision: parse → inspect managedFields → omit admin-owned → SSA apply (no ForceOwnership) → takeover pass (ForceOwnership per entry) → status update
- CRD addition: `Takeover []TakeoverEntry` on `ConfigBundleSpec`; `+listType=atomic` (controller is sole writer, always replaces full list)
