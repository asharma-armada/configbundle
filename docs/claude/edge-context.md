# Edge Reference

> **When to load this file:** Read this before working on the ConfigBundle Controller's OCI pipeline (poll, verify, write CR), cosign verification, divergence reporting, or the edge registry (Zot).

---

## Overview

There is no separate edge agent binary. The ConfigBundle Controller owns the full edge pipeline: it polls the local Zot registry, cosign-verifies each artifact, writes the `ConfigBundle` CR, and decomposes it into domain child CRs. Orb owns its own Dgraph import — configbundle never calls orb's import API. The ConfigBundle CR written to etcd is the handoff artifact; orb reacts to it independently.

---

## Key decisions

- **No separate edge agent** — OCI polling, cosign verification, and ConfigBundle CR writing are part of the ConfigBundle Controller, not a separate sidecar binary. Do not create an `edge-agent` binary.
- **Orb owns Dgraph import** — configbundle never calls orb's `/import` endpoint. The ConfigBundle CR is the signal; orb is responsible for reacting to it and importing its own graph data.
- **Edge always pulls** — the controller polls Zot on a configurable interval. No push, no webhook, no cloud-initiated connection.
- **cosign verify before writing CR** — verification uses the Galleon's local public key (no ACR reachability required). A bundle that fails verification is rejected; no CR is written.
- **Idempotent on digest** — if the artifact at the current tag has the same digest as `status.lastAppliedDigest`, skip all processing. Do not re-verify, re-apply, or re-decompose.
- **Single field manager** — `configbundle-controller` owns all fields it writes on both the ConfigBundle CR and child CRs. Local admin overrides use `local:<admin-id>` — but ONLY on the ConfigBundle CR, never on child CRs.
- **Local overrides are at ConfigBundle CR level only** — child CRs are derived state, not an override surface. The Puller applies the ConfigBundle CR spec WITHOUT `ForceOwnership` so SSA preserves locally-owned fields. The Decomposition Reconciler applies child CR specs WITH `ForceOwnership` because child CRs always faithfully reflect the ConfigBundle CR (including any local overrides already merged into it).
- **Divergence is data, not an error** — a disconnected Galleon that hasn't received a new artifact is in a valid (diverged) state. Do not block or error on lack of convergence.

---

## ConfigBundle Controller — full responsibility list

The controller is a single binary (Mgmt Cluster) with three goroutines managed by controller-runtime:

### Puller (`ctrl.Runnable`) — time-driven, not event-driven
1. **Poll Zot** on `POLL_INTERVAL` for the datacenter's OCI tag
2. **Compare digest** against `status.lastAppliedDigest` on the ConfigBundle CR — skip if unchanged
3. **cosign verify** using local public key at `COSIGN_PUBLIC_KEY_PATH` — reject and write no CR if verification fails
4. **Extract** the `application/vnd.armada.configbundle.manifest.v1+yaml` layer
5. **Apply ConfigBundle CR spec** via SSA WITHOUT `ForceOwnership` — locally-owned fields (from `local:<admin-id>`) are preserved. **⚠ Open design question (Spike 5):** SSA has no partial apply — if the full manifest includes any locally-owned field, the entire apply fails (409), including legitimate cloud-intent changes on uncontested fields. The Puller must either inspect `managedFields` first and omit locally-owned fields, or accept that it uses ForceOwnership (wiping overrides). See crd-context.md § SSA conflict resolution.
6. **Update ConfigBundle CR status** (status subresource): `lastAppliedDigest`, `lastAppliedAt`, `ArtifactFetched` condition, `SignatureVerified` condition

### Decomposition Reconciler (`ctrl.Reconciler`) — event-driven, triggered by ConfigBundle CR changes
7. **Decompose ConfigBundle CR** into domain child CRs via SSA WITH `ForceOwnership` — child CRs faithfully reflect the ConfigBundle CR (including any local overrides already merged into it)
8. **Set ownerReferences** on child CRs so deletion cascades when ConfigBundle is deleted
9. **Update ConfigBundle CR status**: `phase`, `Reconciled` condition

### Divergence Reporter (`ctrl.Runnable`) — scheduled
10. **Inspect `managedFields`** on the ConfigBundle CR — fields owned by `local:<admin-id>` are local overrides
11. **Publish divergence report** to `DIVERGENCE_REPORT_DEST`: field path, CR, override owner, since when
12. **Compare against OCI artifact content** to produce field-level divergence (cloud intent vs current ConfigBundle CR state)

---

## Environment variables (ConfigBundle Controller)

| Variable | Default | Description |
|---|---|---|
| `EDGE_REGISTRY_URL` | `http://localhost:5000` | Zot OCI registry URL |
| `COSIGN_PUBLIC_KEY_PATH` | `/etc/configbundle/cosign.pub` | Path to cosign public key |
| `POLL_INTERVAL` | `60s` | How often to check for new artifacts |
| `DIVERGENCE_REPORT_DEST` | — | S3/NFS path for divergence reports (required) |

---

## Divergence tracking

- The Divergence Reporter inspects `managedFields` on the **ConfigBundle CR only** — not child CRs
- Fields owned by `local:<admin-id>` on the ConfigBundle CR are local overrides
- Divergence report contains: field path, CR name, override owner, since when, delta vs OCI artifact
- Reports published to `DIVERGENCE_REPORT_DEST` on schedule and on demand
- A Galleon with no new artifact (disconnected) still publishes divergence reports — time since last apply is tracked
- **Prerequisite for implementation:** `servers[]` in `ConfigBundleSpec` needs `+listType=map +listMapKey=serviceTag` so SSA tracks field ownership within individual server entries, not just the entire array

---

## Gotchas

- **cosign verify is mandatory** — do not add a flag to skip it. The air-gap trust guarantee depends on the controller being the only entity that can introduce new state.
- **Zot is the only OCI source** — the controller never pulls from ACR directly. Always from local Zot.
- **Do not call orb** — no HTTP calls to orb from the controller. The ConfigBundle CR is the complete interface. If orb needs to react to a new bundle, that is orb's concern.
- **Local overrides are at ConfigBundle CR level only** — do not implement or support `local:<admin-id>` field managers on child CRs (ServerConfig, ClusterConfig, etc.). Child CRs are derived state. Overrides belong on the ConfigBundle CR where they are visible and tracked.
- **Puller must NOT use ForceOwnership on ConfigBundle CR** — this is what allows local overrides to persist across bundle cycles. SSA conflict detection handles the rest.
- **Decomposition Reconciler MUST use ForceOwnership on child CRs** — child CRs always reflect the ConfigBundle CR faithfully. There is no case where a child CR field should diverge from what the ConfigBundle CR says.
- **Divergence tracking is on ConfigBundle CR managedFields only** — do not inspect child CR managedFields for divergence. The ConfigBundle CR is the single source of divergence truth.
- **Decomposition must be idempotent** — applying the same ConfigBundle manifest twice must produce the same child CRs with no side effects. SSA guarantees this if field managers are used correctly.

---

## External references

- [SDD §3.2 — Edge Architecture diagram](../../SDD%20DCIM%20%26%20CMBD%20for%20Galleon%20Digital%20Twin%20in%20Atlas%20%283%29.pdf)
- [OCI artifact layer reference](bundle-context.md)
- [ConfigBundle CR structure](crd-context.md)
- [Local override / divergence model](orbital-context.md)

---

## Domain file maintenance

Update this file when:
- The controller's OCI polling mechanism changes
- The cosign verification approach changes
- The divergence report format or transport is finalized
- Environment variables are added or renamed

Updates must be in the same PR as the code change that prompted them.
