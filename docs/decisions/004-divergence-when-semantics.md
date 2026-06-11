# ADR-004: Divergence `when` field uses managedFields time for MVP

**Date:** 2026-06-11
**Status:** accepted

---

## Context

The divergence report payload includes a `when` field per override entry — "when was this field first overridden by local:admin?" The ideal source is `managedFields[].time` on the ConfigBundle CR, but K8s updates this timestamp on every re-apply by the same manager, not just the first time. So if `local:admin` re-applies the same override, `when` drifts.

An alternative is annotation-based first-seen tracking: cb-controller writes an annotation like `configbundle.armada.ai/override-first-seen/<encoded-path>: <timestamp>` the first time it detects a field owned by `local:admin`, and reads from the annotation thereafter.

---

## Decision

Use `managedFields[].time` directly for the `when` field. No annotation tracking for MVP.

---

## Rationale

**The drift is less meaningful than it appears.** The common case (admin overrides once, never re-applies) has zero drift. The re-apply case is a conscious act — the updated timestamp is arguably more accurate because the admin is actively maintaining the override.

**CB Controller's normal apply does not cause drift.** The `omitAdminOwnedServers` logic means CB Controller does not touch admin-owned fields during normal reconciliation. Only `local:admin` itself updates its own `managedFields[].time`.

**Orbital's `DivergenceEntry.first_seen_at` is the real first-seen.** It is set on first ingest and never updated. The cloud admin sees Orbital's stable first-seen in the UI, not cb-controller's raw `when`. Even if `when` drifts client-side, the displayed value is stable.

**Annotation costs outweigh benefits:**
- Key length limits (253-char DNS prefix + 63-char name) make deeply nested field paths problematic
- Write/delete lifecycle on every consume cycle adds a failure surface
- CR recreation loses annotations — the same failure mode annotations were supposed to prevent
- Additional code complexity for marginal value

**Alternatives rejected:**
- Annotation-based tracking: too complex for MVP, marginal benefit given Orbital's `first_seen_at`
- ConfigMap-based tracking: stale state across pod restarts, harder to correlate with CR lifecycle
- In-memory tracking: lost on pod restart, unsuitable for production

---

## Consequences

**Positive:**
- Divergence reporter is simple — reads `managedFields` directly, no annotation management
- No additional write load on the K8s API per reporting tick

**Negative:**
- `when` reflects "last modified by local:admin" not "first overridden" — documented behavior, not a bug
- If Orbital ever needs client-side first-seen (without its own `first_seen_at`), annotation tracking must be added

**Risks:**
- If a use case emerges where Orbital's `first_seen_at` is insufficient (e.g. Orbital data loss, new consumer), revisit annotation tracking. The intake payload schema (`when` field) does not change — only the source of the value.

---

## Notes

- Contract: `~/armada/orbital/docs/plans/divergence-cb-controller-contract.md` open item #3
- Orbital stores `first_seen_at` in `DivergenceEntry` (set once, never updated) — see `divergence-orbital-ingestion.md`
