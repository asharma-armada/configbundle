# ADR-011: Saturate orbIds on the ConfigBundle CR; eliminate the mapping layer

**Date:** 2026-06-20
**Status:** accepted
**Supersedes (in part):** ADR-005 (D2 — mapping as separate OCI layer) and the partial-Option-C transition documented in `docs/plans/server-identity-orbid.md`.

---

## Context

The CRD's relationship to orbital identifiers has shifted twice already:

1. **ADR-005 (Jun 11)** chose D2: ship the path→orbId mapping as a separate OCI layer; keep the CRD orbId-free. Rationale at the time: *"The CRD should describe desired cluster state, not CMDB metadata."*

2. **`docs/plans/server-identity-orbid.md` (Jun 13)** partially walked that back: added `spec.orbId` and `spec.servers[].orbId` to the CRD because using `serviceTag` as the SSA listMapKey orphaned `ServerConfig` children whenever Dell rebadged a server. orbId was promoted to first-class because Orbital's `@id` directive makes it API-immutable; serviceTag and hostname are both mutable.

3. **Today (this ADR)** finishes the transition: every level orbital identifies as its own ConfigItem carries its own `OrbID` field. The mapping layer is deleted.

The half-state between (2) and (3) — *some* levels carry orbIds on the CR, the rest are derived via mapping rules — produced ongoing friction:

| Friction | Source |
|---|---|
| **Recurring 409 on mapping dispatch** | Manifest and mapping arrive as **two separate** HTTP requests from orb to cb-controller. The mapping handler queries for a CB matching the digest, but `Status.LastAppliedDigest` is written at the END of `applyManifest`. If mapping arrives before the digest is written → no match → 409 → orb retries. Race window persists indefinitely under load. |
| **Suffix convention coupling** | `<parent-orbId>-idrac` is a configbundle↔orbital convention encoded redundantly in the bundler's rule generation and the controller's resolver. Renaming or changing the suffix requires synchronized change in both repos. |
| **Mental model split** | "Datacenter and server orbIds live in `spec.*.orbId`, but nested-type orbIds are derived from a wire-shipped rule" is a half-rule that's hard to teach. |
| **Wire round-trip for static data** | Today's mapping payload contains one rule that is identical in every bundle. The bundler hardcodes the rule, ships it, the controller parses it. Zero per-bundle variation, full ceremony. |
| **Cross-repo contract complexity** | `divergence-cb-controller-contract.md` devotes 80+ lines to specifying the mapping wire shape, dispatch routing, storage convention, and translation algorithm — all of which exists because of the half-state. |

The user's framing — *"having some but not all is confusing, do all or nothing"* — names the right principle. "Nothing" is structurally blocked because every alternative to orbId-as-listMapKey (serviceTag, hostname, oobIP, hardware UUID-not-yet-exposed-by-orbital) either is mutable or requires non-trivial orbital schema work. "All" is reachable from where we are.

---

## Decision

**Saturate orbIds on the ConfigBundle CR.** Every level that orbital identifies as a ConfigItem carries its own `OrbID` field directly on the CRD type. Eliminate the mapping OCI layer, the `<cb-name>-mapping` ConfigMap's `mapping.json` key, the `MediaTypeMapping` constant, and the entire `bundle.MappingPayload` / `bundle.MappingRule` runtime abstraction.

Concrete CRD change:

```go
type IdracSpec struct {
    // OrbID is the immutable Orbital identifier for this IdracSettings node
    // (e.g. "colo:srv-001-idrac"). Set by the bundler from orbital's GraphQL;
    // identity metadata only — never overridable by local:admin.
    // +kubebuilder:validation:Required
    OrbID string `json:"orbId"`

    // (existing fields — sshEnabled, racadmEnabled, ipmiEnabled, etc.)
}
```

The CRD now declares the rule **"every level that has independent orbital identity carries its own OrbID"** as a structural invariant. Future nested types (BIOS, NIC, NetworkConfig, FirmwareImage, ...) follow the same pattern: add the field on the struct, the bundler queries orbital and populates it, the controller reads it directly.

---

## Why this is the right model now

**1. Mental model becomes a single sentence.** *"The CR is the cross-system identity manifest for this datacenter."* Every node has its orbId stored where it's used. No half-rules, no suffix conventions, no "depends on which level you're at" exceptions.

**2. Eliminates the 409 race entirely.** The mapping dispatch is the source of the 409. With no second HTTP request from orb (mapping no longer ships), the race window closes by deletion. The `RetryOnConflict`-around-409 mitigation we added earlier becomes unnecessary.

**3. Removes the suffix-convention coupling.** The bundler queries orbital for the iDRAC node's `orbId` directly and stores it. orbital may name iDRAC nodes "X-idrac", "X-bmc", UUID, or anything else in the future — configbundle stores whatever orbital returns. Zero shared convention to keep synchronized.

**4. Scales cleanly to new nested types.** Adding `BiosSpec` with its own orbId is: one struct field on the CRD, one query field on the bundler's GraphQL, one entry in the divergence-reporter's "which nested struct is this?" lookup. No rule entry to add, no on-wire payload to extend, no suffix to mint.

**5. Validation gives a real signal.** `+kubebuilder:validation:Required` on `IdracSpec.OrbID` means a malformed bundle (bundler bug — forgot to set orbId on an idrac block) fails at apply time with a clear 4xx, not silently at divergence-reporting time several minutes later.

**6. Matches established K8s patterns for cross-system identity.** `Node.spec.providerID`, `PersistentVolume.spec.csi.volumeHandle`, `Endpoints` subsets — K8s objects routinely carry external-system identifiers as first-class fields where they are needed. The configbundle model is now consistent with those precedents.

---

## What this changes

### CRD schema

```go
// api/v1/configbundle_types.go
type IdracSpec struct {
    // +kubebuilder:validation:Required
    OrbID                       string  `json:"orbId"`            // NEW
    SSHEnabled                  *bool   `json:"sshEnabled,omitempty"`
    RacadmEnabled               *bool   `json:"racadmEnabled,omitempty"`
    IPMIEnabled                 *bool   `json:"ipmiEnabled,omitempty"`
    // ... (existing pointer-leaf fields unchanged)
}

// IdracTypeName is the orbital GraphQL type name for IdracSpec nodes.
// Surfaces in OverrideEntry.Type. One const per nested struct that has
// its own orbital identity.
const IdracTypeName = "IdracSettings"
```

### Deletions

| File | What goes |
|---|---|
| `bundle/mapping.go` | **entire file** — `MappingPayload`, `MappingRule`, `ParseMappingPayload`, `Resolve`, `ResolveByOrbID` |
| `bundle/mediatype.go` | `MediaTypeMapping` constant |
| `internal/bundler/handler.go` | `buildMapping` function (~20 lines); the second OCI layer in `bundleResponse`; refactor `buildTakeover`/`buildIgnored` to walk `spec.servers[].idrac.orbId` directly |
| `internal/controller/consume.go` | `MediaTypeMapping` case in dispatch switch; `handleMappingBody` (~50 lines) |
| `internal/controller/mapping.go` | `ParseMapping`, `writeMappingConfigMap`, `readMappingConfigMap` (~50 lines); KEEP `writeLastAppliedSpec` and `readLastAppliedSpec` |
| `internal/controller/divergence_reporter.go` | `extractOverrides`'s `mapping *bundle.MappingPayload` parameter; replaced with direct spec walk |

### What stays

- The `<cb-name>-mapping` ConfigMap exists, but now carries only `last-applied-spec.yaml` (used by handback and divergence reporter cold-start). Renaming to `-state` is post-MVP polish.
- All of takeover (ADR-006), managedFields release (ADR-008), edge handback (ADR-009) — unaffected. They operate on `spec.takeover[]` and `spec.ignored[]` and on managedFields; never on the mapping rules.
- `OperatorNamespace` env var (controls where the last-applied-spec CM lives) — still relevant for the surviving CM.
- The divergence intake contract from cb-controller to orb (`POST /api/v1/divergence` payload shape) is unchanged. The reporter still produces orbital-native entries; it just sources the orbId from `cb.Spec.Servers[].Idrac.OrbID` instead of `mapping.Resolve(path)`.

### Cross-repo impact

- **Orb's dispatcher** no longer needs to handle `MediaTypeMapping`. orb-side cleanup is non-blocking — if orb still recognizes the media type but no bundler ships it, nothing happens. Worth pruning eventually.
- **Orbital's contract doc** (`divergence-cb-controller-contract.md`, mirrored in both repos) needs the mapping-layer section rewritten. The wire shape, dispatch routing, and translation algorithm sections all delete.

---

## Migration

CRD change requires existing CRs to be deleted and re-imported (the `OrbID` field on `IdracSpec` is required; existing bundles don't have it).

```bash
# 1. Delete the existing CR (cascades to ServerConfig children + state CM via GC)
kubectl delete cb colo-galleon

# 2. Uninstall the old CRDs
make uninstall

# 3. Pull this branch, regenerate
make manifests

# 4. Install the new CRDs (now with required IdracSpec.OrbID)
make install

# 5. Run the new bundler (locally or restart deployed)
make run-bundler

# 6. Run the new controller
make run-controller

# 7. Trigger a fresh import via orb (orb pulls + dispatches; bundler ships the
#    new shape; controller accepts; no mapping CM is written).
```

No data is preserved; orbital is the source of truth and repopulates. The `last-applied-spec.yaml` snapshot is regenerated on first successful import.

---

## Consequences

**Positive:**

- 409 race on mapping dispatch is gone (deleted, not mitigated).
- One mental model for cross-system identity (CR carries it everywhere).
- One fewer OCI media type, one fewer dispatch route, one fewer state-shape on the controller.
- New nested types follow a simple pattern: add struct field + type const.
- Bundler is more straightforward (queries orbital, fills in the spec, ships one layer).
- Validation catches malformed bundles at apply time, not at divergence-report time.

**Negative:**

- The CR is no longer "pure K8s desired state" — it carries orbital identifiers as first-class fields. ADR-005's purity argument is fully abandoned. This is the conceptual cost worth naming honestly. The trade is: pragmatic identity stability (rebadge resilience) wins over architectural purity.
- One required field added to `IdracSpec` — any tooling that constructs IdracSpec objects manually (tests, scripts, demos) must include `OrbID`.
- Migration requires CR deletion + re-import — cannot be in-place. Same constraint as ADR-010's cluster-scope migration.

**Neutral:**

- The divergence intake contract from cb-controller to orb is unchanged in shape; only the internal source of `orbId` shifts.
- Orb can still recognize `MediaTypeMapping` for a release or two during the transition; it just won't see any bundles using it.

---

## Related

- ADR-005 — Original D2 decision. Superseded in spirit (the mapping layer it defined is now deleted) but its rejected-Option-C critique remains historically informative.
- `docs/plans/server-identity-orbid.md` — The intermediate step that put orbIds on datacenter and server. This ADR finishes that work at the nested-type level.
- ADR-006 (takeover), ADR-008 (managedFields release), ADR-009 (handback) — unaffected.
- ADR-010 (cluster-scoped CRDs) — orthogonal but co-located in time; both shift the CR toward "the canonical artifact for this datacenter's intended state."
