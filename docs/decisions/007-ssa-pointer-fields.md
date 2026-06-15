# ADR-007: SSA partial-apply uses pointer fields with omitempty

**Date:** 2026-06-13
**Status:** accepted

---

## Context

cb-controller does Server-Side Apply (SSA) on the `ConfigBundle` CR with field manager `configbundle-controller`. Local admins can override individual leaf fields via SSA with field manager `local:admin`. After an admin override, controller's subsequent SSA must omit the admin-owned leaves from its patch — otherwise SSA returns 409 Conflict on those specific fields. This is the per-leaf granularity decided in [ADR-006](006-divergence-takeover-pipeline.md) and [SSA single-owner model](../../CLAUDE.md).

The technical question: how does a typed Go struct produce a partial SSA patch that omits specific leaf fields?

Go primitives serialize their zero value (`bool` → `false`, `string` → `""`) by default. Without intervention, the controller's apply always claims every field on `IdracSpec`, including the ones admin owns — causing 409s. We need a mechanism to mark specific leaf fields as "not part of this apply."

## Options considered

### A. Unstructured `map[string]any` at the apply boundary

Marshal the typed spec to `map[string]any`, walk admin's `FieldsV1` and delete owned paths from the map, then apply via `unstructured.Unstructured`.

- ✅ Localized — typed structs everywhere except this one apply site
- ✅ No CRD schema changes
- ❌ Loses type safety at the apply boundary
- ❌ ~80 lines of bespoke `FieldsV1` walking (`f:`, `k:{...}`, listMapKey restoration)
- ❌ Field renames break silently at runtime, not at compile time
- ❌ Test fixtures construct nested `map[string]any` literals instead of typed values
- ❌ Not idiomatic — Kubernetes itself does not do this for typed CRDs

### B. Pointer fields with `omitempty` on the CRD types

Change every leaf primitive in overridable types to a pointer (`*bool`, `*string`) with `,omitempty`. A nil pointer serializes as absent; controller selectively nils admin-owned fields before applying as a typed object.

- ✅ Idiomatic — matches Kubernetes' own `ApplyConfiguration` pattern used throughout `k8s.io/client-go/applyconfigurations/*`
- ✅ Type-safe at the apply boundary
- ✅ Field renames caught at compile time
- ✅ The omission logic is the same FieldsV1 walk but produces a typed result via JSON round-trip; the apply itself is typed
- ✅ Tests construct typed values with `ptr.To(true)` helpers
- ❌ Every CRD field becomes a pointer — slight ergonomic cost in unrelated reads (`*bool` vs `bool`)
- ❌ Consumers must dereference safely (treat `nil` as "field unset")
- ❌ One-time refactor cost across the codebase

### C. Generated `ApplyConfiguration` types

Run `applyconfiguration-gen` against our API types. Generates a parallel set of `*ApplyConfiguration` structs (`IdracSpecApplyConfiguration`, etc.) with pointer fields. Controller applies using the generated types; the underlying CRD types stay non-pointer.

- ✅ Most-canonical Kubernetes pattern (exact same generator used for core types)
- ✅ Type-safe; original CRD types unchanged
- ❌ Adds a codegen pipeline to the build
- ❌ Two parallel type hierarchies to maintain
- ❌ Overkill for our scale (~10 leaf fields)

## Decision

**Adopt Option B: pointer fields with `omitempty` on overridable leaf fields of `IdracSpec` and `ServerSpec`.**

`ServiceTag` (the listMapKey on `servers[]`) stays as a non-pointer `string` — it identifies the entry and must always be present.

## Rationale

The choice is between a 5-line apply call backed by ~80 lines of FieldsV1 walking against unstructured maps (Option A), versus a ~10-line idiomatic typed apply backed by a JSON round-trip and selective nilification (Option B). The walking logic is the same in both — what differs is whether the final apply is `*unstructured.Unstructured` or `*armadav1.ConfigBundle`.

The evidence that Option B is the convention:

- `k8s.io/client-go/applyconfigurations/apps/v1/deploymentspec.go` declares `Replicas *int32`, `Paused *bool`, `MinReadySeconds *int32`, etc. — every leaf is a pointer.
- The `applyconfiguration-gen` tool generates the same shape for any API type. Its existence is the official acknowledgement that "to do partial SSA from Go, use pointer fields."
- Cluster API's `ClusterClass` overrides (architecturally analogous to our local admin overrides) use pointer fields throughout.
- We could not find any serious controller that uses `unstructured.Unstructured` for SSA against its own typed CRD. The unstructured client is for *generic* tooling that doesn't know the type, not for typed CRD controllers.

Option C would be more canonical still but introduces codegen complexity disproportionate to a ~10-field CRD. Option B captures the same benefit (typed apply + selective omission) with no codegen pipeline.

## Consequences

**API surface:**

- `IdracSpec` booleans become `*bool` with `,omitempty`.
- `IdracSpec.FirmwareVersion` already had `,omitempty` on a `string`; promoted to `*string,omitempty` for consistency.
- `ServerSpec.Hostname` and `ServerSpec.OobIP` become `*string,omitempty`.
- `ServerSpec.ServiceTag` stays `string` (listMapKey, always required).
- The `+kubebuilder:validation:Required` markers still apply — they validate the *final merged CR*, not individual patches. Admin's apply may omit Required fields if another manager owns them.

**Caller obligations:**

- Bundler must allocate pointers for every leaf it emits (orbital is the source, so values are always known — no nil from this side).
- Divergence reporter and child reconciler must dereference safely (`if p != nil { ... }`).
- A nil leaf in a CR means "no manager has set this field" — treated as unset, not as zero value.

**Test ergonomics:**

- Test fixtures use `k8s.io/utils/ptr` helpers: `Idrac: armadav1.IdracSpec{SSHEnabled: ptr.To(true)}`.
- Existing tests that construct `IdracSpec{SSHEnabled: true}` won't compile; mechanical refactor.

**What this does NOT change:**

- SSA semantics (still single-owner, no shared management).
- Conflict resolution (admin still uses `--force-conflicts` for initial takeover).
- ADR-006 takeover pipeline (unchanged; ForceOwnership on specific paths).
- Divergence reporting shape (the override JSON payload remains the same — pointer/non-pointer is an internal Go concern).

**Migration:**

Existing ConfigBundle CRs in clusters are wire-compatible. The JSON shape on disk doesn't change — `sshEnabled: false` decodes to `*bool` pointing at `false`. No data migration required. The controller is the only producer of these CRs in normal operation; once cb-bundler is redeployed to emit pointer fields, all new CRs are correct.

## Related

- [ADR-006](006-divergence-takeover-pipeline.md) — takeover pipeline; defines per-leaf granularity requirement
- [docs/claude/crd-context.md](../claude/crd-context.md) — CRD type conventions
- Kubernetes `ApplyConfiguration` reference: `k8s.io/client-go/applyconfigurations/apps/v1/deploymentspec.go`
- `applyconfiguration-gen`: `k8s.io/code-generator/cmd/applyconfiguration-gen`
- Cluster API ClusterClass pointer-field pattern: `cluster.x-k8s.io/v1beta1/ClusterClass`
