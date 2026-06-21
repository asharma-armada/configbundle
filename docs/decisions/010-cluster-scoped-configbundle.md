# ADR-010: ConfigBundle is cluster-scoped; children are namespaced

**Date:** 2026-06-19
**Status:** accepted

---

## Context

ConfigBundle today is namespaced (`kubebuilder:resource:scope=Namespaced`). The prototype has been operating with the single `colo-galleon` CR in `default`, and ConsumeServer's `cfg.Namespace` overload meant the same env var named both "where the parent lives" and "where children land."

Two pressures push against this model:

**1. Extensibility.** Other teams' operators will eventually want to subscribe to ConfigBundle and produce sub-resources of their own (e.g., a `BIOSPolicy` controller, a `FirmwareDriftReporter`, multiple `ServerConfig` actuators per team). With both parent and child namespaced, K8s forbids cross-namespace `ownerReferences`. Anyone outside the parent's namespace either loses native GC (forcing custom finalizer / scan-based cleanup) or has to live in the same namespace as the parent — which couples team boundaries to namespace boundaries unnecessarily.

**2. Semantic correctness.** A ConfigBundle represents the cloud-CMDB-shipped intent for a *datacenter*. It's a single artifact per cluster, not "one per namespace." Namespacing it suggests a multiplicity that doesn't exist in the domain model.

K8s has a sanctioned pattern for exactly this shape: **cluster-scoped parent, namespaced children**. Cluster-scoped owners can be referenced from `ownerReferences` in any namespace, and K8s GC cascades natively. Real-world precedents: `cert-manager.io/ClusterIssuer` → `Certificate`, `gateway.networking.k8s.io/GatewayClass` → `Gateway`, most CNI policy resources. ADR-009's clarification confirmed the rule.

---

## Decision

**Both ConfigBundle and ServerConfig (and any future sibling sub-resources that describe datacenter physical-config state) are cluster-scoped.** Per-bundle operator-state ConfigMaps (mapping, last-applied-spec) and the iDRAC credentials Secret remain namespaced — they're scoped to a controller's deployment, not to the datacenter.

cb-controller writes the per-bundle ConfigMaps to a configurable target namespace via env var `CHILD_NAMESPACE` (default: `configbundle-system`). ServerConfig CRs themselves are cluster-scoped — no namespace context. Each CR carries an `ownerReferences[0]` pointing to the cluster-scoped ConfigBundle; native K8s GC cascades on bundle deletion.

---

## Why this is the right model

**1. Native K8s GC.** Cluster-scoped owner → cluster-scoped child supports native GC cascade. No custom finalizer, no scan-based cleanup, no "stuck `Terminating`" failure modes.

**2. Domain match.** ServerConfig describes a *physical server's intended config*. Servers are global to a cluster — they don't belong to a tenant or a namespace. Two ServerConfigs for the same server in two different namespaces would be a contradiction, not a feature. Closer to `Node` (cluster-scoped) than `Certificate` (per-tenant namespaced).

**3. Consistent mental model.** Everything describing datacenter physical state is cluster-scoped. Things that are operator-implementation-state (ConfigMaps, Secrets) stay namespaced because they're scoped to a controller's deployment, not to the datacenter itself.

**4. No `CHILD_NAMESPACE` for SC consumers.** Consumers (serverconfig-controller and future siblings) don't need any namespace configuration to find the CRs — they just watch cluster-wide. Controllers themselves can live in any namespace for deployment isolation, but the CRs they touch are cluster-scoped.

**5. Established K8s idiom for physical-infra CRDs.** `Node`, `PersistentVolume`, `StorageClass`, `PriorityClass`, `ClusterIssuer` — cluster-scoped because they represent global facts.

---

## What changes

| Surface | Before | After |
|---|---|---|
| **CRD scope** | `scope=Namespaced` | `scope=Cluster` |
| **Parent CR location** | `default` (or any ns) | no namespace — cluster-scoped |
| **Child ServerConfig location** | same ns as parent | `CHILD_NAMESPACE` (default `configbundle-system`) |
| **Mapping + last-applied-spec ConfigMaps** | same ns as parent | `CHILD_NAMESPACE` |
| **OwnerReference on children** | namespaced parent (constrained to same ns) | cluster-scoped parent (any ns legal) |
| **K8s GC** | native cascade | native cascade (still works, now across ns) |
| **cb-controller env vars** | `NAMESPACE=default` | `CHILD_NAMESPACE=configbundle-system` |
| **cb-controller RBAC** | namespaced Role | ClusterRole on `configbundles` + Role on `serverconfigs`/`configmaps` in CHILD_NAMESPACE |
| **`kubectl get cb`** | `kubectl get cb -n default` | `kubectl get cb` (cluster-wide) |
| **Orb `POST /dispatch`** | unchanged contract | unchanged contract |

---

## What stays the same

- The ConfigBundleSpec fields (datacenter, servers, takeover, ignored).
- All field-level negotiation semantics: takeover (ADR-006), managedFields release (ADR-008), edge handback + stale-Ignored scrub (ADR-009).
- ServerConfig is still namespaced — no change to its scope or fields.
- Orb's `POST /dispatch` API contract — orb doesn't model namespaces; the dispatch payload carries no namespace context.
- The bundle's OCI layout, media types, signing, etc.
- The divergence reporter pipeline and contract with orb.

This is a **topology change, not a semantics change.** The core controller logic doesn't move.

---

## Cluster-wide name uniqueness

Cluster-scoped resources require globally unique names. ConfigBundle names today are `spec.Datacenter` (e.g., `colo-galleon`), and the design already assumes one datacenter per cluster — so collision-free. Document as invariant: **one ConfigBundle per datacenter, per cluster.**

If a future use case needs multiple datacenters per cluster (e.g., a test cluster running both `colo-galleon` and `staging-galleon` bundles), the existing naming already accommodates it — both names are distinct.

---

## Migration (one-shot, clean break)

K8s does not allow changing CRD scope in place. The migration is a delete-and-recreate:

```bash
# 1. Note the existing CR (informational; not used for restore)
kubectl get cb colo-galleon -o yaml > /tmp/cb-backup.yaml

# 2. Delete the existing CR (cascades to ServerConfig children via current ownerRefs)
kubectl delete cb colo-galleon -n default

# 3. Uninstall the old (Namespaced) CRD
make uninstall   # or: kubectl delete crd configbundles.armada.ai

# 4. Pull this branch / regen manifests
make manifests

# 5. Install the new (Cluster-scoped) CRD
make install

# 6. Restart cb-controller with CHILD_NAMESPACE=configbundle-system
make run-controller   # or redeploy via kustomize

# 7. Re-import via orb (orb pushes a fresh bundle; cb-controller creates
#    the cluster-scoped ConfigBundle, decomposes into configbundle-system)
```

After migration:

```
kubectl get cb                                  # cluster-scoped, no -n flag
kubectl get sc -n configbundle-system           # 49 server children, in CHILD_NAMESPACE
kubectl get cm -n configbundle-system           # mapping + last-applied-spec CMs
```

No data is preserved across the migration; orb is the source of truth and will repopulate. The local-admin `managedFields` claims on the previous CR are gone — testers should re-apply any overrides post-migration if they want to continue exercising the takeover/ignore/handback flows.

---

## Consequences

**Positive:**

- Native K8s GC works for cross-namespace fanout. No custom finalizer.
- Multiple teams can build their own sub-resource operators in their own namespaces, all consuming the same ConfigBundle.
- RBAC separation by responsibility: cluster-level for the contract, namespaced for actuation. Easier to scope down per-component.
- Mental model matches domain: one datacenter, one bundle, one cluster — not "one bundle per ns."
- `kubectl get cb` is simpler (no `-n` needed).

**Negative:**

- Breaking change for the one existing CR (`colo-galleon` in `default`). Documented migration recipe required.
- cb-controller needs `ClusterRole` for ConfigBundle ops — a strictly broader RBAC grant than before. Auditors will see cluster-level writes; acceptable because the controller IS the contract owner.
- All envtest/unit-test fixtures referencing namespaced ConfigBundle need updates.
- Documentation churn: ADRs and CLAUDE.md notes that reference "the bundle's namespace" need updates.

**Neutral:**

- `kubectl describe cb colo-galleon` works identically; the only operator-facing difference is the absent `Namespace:` line.
- Field-level semantics (takeover/ignore/handback) are unaffected. Tests verify this.

---

## Related

- ADR-006 (takeover pipeline) — unaffected.
- ADR-008 (managedfields release) — unaffected; the release-on-omit protocol operates on a single CR regardless of scope.
- ADR-009 (edge handback) — unaffected; reclaim controller watches a cluster-scoped CR now but the logic is identical.
- K8s docs — [Owners and dependents: Cross-namespace owner references](https://kubernetes.io/docs/concepts/overview/working-with-objects/owners-dependents/#cross-namespace-owner-references)

---

## Out of scope (follow-ups)

- **Per-ConfigBundle child-namespace override** (e.g., annotation `armada.ai/child-namespace`). Useful if multiple teams want to fan out to different namespaces from one bundle. Defer until a concrete tenant needs it.
- **Multi-datacenter naming policy.** Today's "one CB per datacenter per cluster" invariant is documented above but not enforced by validation. If/when that constraint is at risk, add admission validation.
- **CRD version bump.** Kept at `v1` for prototype simplicity; this migration is a clean break, not a versioned conversion. Production usage would warrant a `v1alpha2` or `v2` with conversion webhooks.
