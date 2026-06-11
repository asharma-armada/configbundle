# ADR-005: K8s-path to orbId translation via separate OCI mapping layer (D2)

**Date:** 2026-06-11
**Status:** accepted

---

## Context

The divergence reporter sends K8s field paths (e.g. `spec.servers[serviceTag=3RK3V64].idrac.sshEnabled`) to orb. Orbital needs orbId + field name to display divergence entries in the UI. Something must translate between the two identifier systems.

Three options were considered:
- **Option A:** CB Controller does the translation (needs orbIds in the CR or a separate lookup)
- **Option C:** orbIds are inlined on the CR itself (every ConfigItem carries its orbId)
- **Option D2:** cb-bundler produces a `mapping.json` OCI layer; orb stores it by bundle digest and translates at intake time

---

## Decision

D2: separate `mapping.json` OCI layer. CB Controller sends raw K8s paths. Orb hosts the mapping and translates.

---

## Rationale

**CB Controller stays in pure K8s terms.** It reads `managedFields`, produces K8s field paths, and POSTs them to orb. It does not know about orbIds, Orbital's identifier scheme, or DGraph. This keeps the controller small and decoupled from Orbital's data model.

**The mapping format is domain-agnostic.** A flat list of `{path, orbId}` entries means orb does string prefix matching — no knowledge of CRD list key names, no code changes when new domains are added (clusters, applications, etc.). cb-bundler emits the right entries; orb's parser stays generic.

**cb-bundler already has the data.** It queries Orbital GraphQL when building the bundle and already knows each server's serviceTag, hostname, etc. Adding `orbId` to the query is one field. Producing the mapping layer is a serialization step alongside the manifest layer.

**Orb is the right location for translation.** The mapping is edge-local (stored by bundle digest). Translation happens at intake time (`POST /api/v1/divergence`), keeping the logic in one place. If the mapping format evolves, only orb changes — cb-controller's release cycle is independent.

**Alternatives rejected:**
- Option A (CB Controller translates): requires cb-controller to know orbIds, adding coupling to Orbital's data model. The controller would need either the mapping file or inline CRD fields — both leak Orbital concerns.
- Option C (inline orbIds on CRD): pollutes the ConfigBundle CR spec with Orbital-internal identifiers. The CRD should describe desired cluster state, not CMDB metadata. Also forces CRD schema changes whenever Orbital's identifier scheme evolves.

**Note:** The contract's open items section (Q2) references "option C" but the contract body describes D2 throughout. The Q2 reference is stale. Both the contract body and the companion orbital ingestion doc agree on D2.

---

## Consequences

**Positive:**
- CB Controller has no orbId dependency — simpler, faster, fewer failure modes
- New CRD domains (clusters, networks) require only new mapping entries from cb-bundler — orb code unchanged
- Mapping is versioned per bundle digest — no stale-translation risk

**Negative:**
- Bundler returns two layers instead of one (manifest + mapping) — minimal complexity increase
- Orb must implement mapping storage and the prefix-match translation algorithm
- If the mapping layer is missing from a bundle (bundler bug), orb returns 422 on divergence intake — cb-controller retries next tick

**Risks:**
- If the flat-list format proves insufficient for complex nested domains, the mapping schema may need extension (e.g. per-leaf name overrides). Current format handles all known domains.

---

## Notes

- Contract: `~/armada/orbital/docs/plans/divergence-cb-controller-contract.md` §"How K8s field paths get translated"
- Mapping media type: `application/vnd.armada.configbundle.mapping.v1+json`
- Mapping format: `{"bundleDigest": "sha256:...", "items": [{"path": "...", "orbId": "..."}]}`
- Orb prerequisite: recognize mapping layer media type, store under `DataDir/mappings/<digest>.json`
