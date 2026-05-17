# Bundle Reference

> **When to load this file:** Read this before working on OCI artifact structure, layer definitions, media types, signing, or tag conventions.

---

## Overview

A configbundle OCI artifact is produced by Orbital's publish pipeline (with configbundle as an enricher). It contains three layers identified by media type. The artifact is signed once by Orbital using cosign and pushed once to ACR. Downstream consumers (orb, cb-controller, edge agent) pull from ACR or the local Zot mirror and identify their layer by media type — unknown layers are ignored.

---

## Key decisions

- **Orbital produces the artifact** — configbundle's bundler returns bytes to Orbital; it never pushes to ACR. No downstream system needs OCI registry write credentials.
- **Monotonic int tags** — OCI tags are `v1`, `v2`, `v42`. After push, all references use the digest for immutability. Do not use datacenter/schema/sequence compound tags.
- **cosign signing** — signature stored as an OCI referrer artifact on the bundle digest. Galleons hold only the public key. Verification is fully air-gapped (no ACR reachability required).
- **Enrichment is all-or-nothing** — if the bundler fails, Orbital pushes nothing. Partial artifacts with some layers missing are never produced.

---

## OCI layer reference

| Layer | Media type | Producer | Consumer |
|---|---|---|---|
| ConfigBundle manifest | `application/vnd.armada.configbundle.manifest.v1+yaml` | configbundle bundler | cb-controller |
| DGraph export subgraph | `application/vnd.orbital.subgraph.data.v1+gzip` | Orbital | orb (`dgraph live` import) |
| DGraph schema | `application/vnd.orbital.subgraph.schema.v1+gzip` | Orbital | orb (schema version check) |

The first layer is produced only when the publish request includes the bundler enricher URL. The orbital layers are always present.

---

## Media type constants

These constants belong in the `bundle/` package and are the single source of truth. Import them everywhere — do not hardcode strings.

```go
const (
    MediaTypeManifest = "application/vnd.armada.configbundle.manifest.v1+yaml"
    MediaTypeData     = "application/vnd.orbital.subgraph.data.v1+gzip"
    MediaTypeSchema   = "application/vnd.orbital.subgraph.schema.v1+gzip"
)
```

---

## Tag and digest conventions

- Tags are monotonic integers: `v1`, `v2`, `v42`. Orbital increments per datacenter per publish.
- After an artifact is pushed, reference it by digest (`sha256:abc123...`) for immutability.
- The edge agent and edge registry track artifacts by digest, not tag, after initial pull.

---

## Signing

- Orbital signs with cosign after assembling all layers — one signature per artifact push.
- The signature is stored as an OCI referrer artifact on the bundle's digest (not a separate tag).
- Galleons hold only the cosign public key. Verification happens locally from the Zot mirror — no network call to ACR.
- The edge agent must verify the cosign signature before importing or writing the ConfigBundle CR. Skip verification = reject.

---

## Gotchas

- **Unknown layers are safe to ignore** — consumers identify their layer by media type and skip the rest. Do not write code that fails on unexpected layers.
- **`enriched: true` is set by Orbital** — the bundler does not set this. If you see it on the `RegistryArtifact` row in Orbital's DB, it means the bundler ran and succeeded.
- **Empty array `[]` from enricher = no configbundle layer** — Orbital will push only the orbital layers. This is valid behavior, not a failure.

---

## External references

- [Enricher integration design](../../configbundle-integration.md)
- [OCI artifact layer reference table](../../configbundle-integration.md#oci-artifact-layer-reference)

---

## Domain file maintenance

Update this file when:
- A new OCI layer is added or a media type changes
- The tag convention changes
- The signing mechanism or verification approach changes

Updates must be in the same PR as the code change that prompted them.
