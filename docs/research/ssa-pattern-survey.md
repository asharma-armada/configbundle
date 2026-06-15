# SSA pattern survey: who else does what we do?

**Status:** Draft (2026-06-14) — research notes, not a decision. Revise as needed.
**Author:** session-captured; based on a Web search through K8s ecosystem projects
**Question being answered:** Is the configbundle SSA pattern (per-leaf admin overrides + controller respects + upstream divergence reporting + takeover loop) conventional, or are we doing something unusual?

---

## Summary

Pieces of our pattern exist throughout the K8s ecosystem. The combination — specifically the **closed-loop human-disposition workflow** described in [What we do that's uncommon](#what-we-do-thats-uncommon-in-the-k8s-ecosystem) — is unusual.

Every component primitive we use — per-leaf SSA tracking, force-conflicts for takeover, listType=map with listMapKey, managedFields walking, field-manager identity tracking — appears in production K8s projects exactly as we use it. The unconventional part is the workflow we build on top: an upstream system that receives structured override data and emits a structured response signal through the same delivery path.

---

## Closest match: EKS Managed Add-ons

This is the **closest analog by intent**. Amazon documents the same posture: controller owns its fields, customer owns the rest.

From AWS's docs:

> Amazon EKS uses the Kubernetes server-side apply feature to enable management of an add-on by Amazon EKS **without overwriting your configuration for settings that aren't managed by Amazon EKS**. To achieve this, Amazon EKS manages a minimum set of fields for every add-on that it installs. You can modify all fields that aren't managed by Amazon EKS, or another Kubernetes control plane process such as `kube-controller-manager`, without issue.

Their `managedFields` documentation distinguishes:

- **"Fully managed"** (`f:field: {}`) — entire field owned
- **"Partially managed"** (`f:field: { f:specificKey }`) — specific sub-keys owned

This is exactly our per-leaf model (e.g. `f:idrac: { f:sshEnabled: {} }`). EKS uses a single fixed field manager string `eks`.

**Where they differ from us:**

- No per-person attribution — just `eks` vs anything-else
- No upstream reporting of customer overrides
- No takeover loop — "Modifying a field managed by Amazon EKS prevents Amazon EKS from managing the add-on and may result in your changes being overwritten when an add-on is updated." That's a loose posture (just hope), not a deliberate workflow.

---

## Closest mechanic: ArgoCD `managedFieldsManagers`

ArgoCD added the **exact same mechanic** we use, in reverse direction. Their `ignoreDifferences.managedFieldsManagers` config (since ArgoCD 2.5):

```yaml
ignoreDifferences:
  - group: apps
    kind: Deployment
    managedFieldsManagers:
      - kube-controller-manager
      - istio-sidecar-injector
```

ArgoCD walks `managedFields`, finds paths owned by those managers, and **excludes them from drift comparison**.

> "ArgoCD can leverage this metadata to automatically ignore fields owned by other managers, eliminating the need to manually list every field path you want to skip."

Our `omitAdminOwnedFields` function is structurally identical — walk `managedFields`, find paths owned by `local:*`, exclude. ArgoCD does it for diff; we do it for apply. Same machinery.

**Where they differ from us:**

- ArgoCD lists individual trusted manager names; we use a prefix convention (`local:*`)
- ArgoCD's flow stops at "ignore"; we capture and report upstream
- ArgoCD's actual conflict resolution is force-override-everything per their proposal:
  > "The first version should use the force flag and override even if there are conflicts."

So ArgoCD treats SSA as "controller wins, but tolerate other managers in diffing." We treat it as "controller respects local overrides at apply time, then reports them upstream for human disposition."

---

## Closest architecture: Cluster API ClusterClass

The CAPI proposal **explicitly cites SSA co-authorship** as a design choice:

> "the topology controller uses Server Side Apply to write/patch topology owned objects; using SSA allows other controllers to co-author the generated objects."

They built in awareness of co-owned lists:

> "this requires providers to pay attention on lists that are co-owned by multiple controller... it is required to ensure the proper annotation exists on the CRD type definitions, like +MapType or +MapTypeKey."

But ClusterClass **constrains human overrides to defined variables**, not arbitrary fields. The proposal explicitly does NOT define a general "override any topology-managed field" mechanism. Their per-field admin override surface is structured (via variable definitions), not free-form.

---

## Honorable mention: Flux's per-resource `ssa: Merge`

Flux's default is "controller wins, revert drift" (field manager: `kustomize-controller`). Their `kustomize.toolkit.fluxcd.io/ssa: Merge` annotation flips that per-resource to "preserve non-overlapping fields owned by others." Same posture as EKS, opt-in.

> "Flux will detect the drift and revert to the desired replica count on its next reconciliation cycle." (default)

Pattern documented for coexistence: **omit fields from Git manifests that you want other tools to manage** (e.g. remove `replicas` so HPA can adjust it).

---

## What we do that's uncommon in the K8s ecosystem

**One sentence:** we put a human decision point inside the controller's reconciliation loop, mediated by an upstream source-of-truth rather than by Git or a UI.

This is **one composed workflow**, not a set of unrelated novel mechanics. Every individual piece uses standard K8s primitives (SSA, managedFields, force-conflicts, OCI artifact distribution). The shape is unusual because of **how they're connected**.

### The workflow

```
   ┌───────────────────────────────────────────────────────────────────────┐
   │                                                                       │
   │   1.  source-of-truth   ─── bundle ───►   controller                  │
   │       (orbital)             (oci push)    (cb-controller)             │
   │                                                                       │
   │   2.  human operator SSAs the CR with --field-manager=local:<id>      │
   │                                                                       │
   │   3.  controller continues to reconcile — per-leaf SSA omits          │
   │       admin-owned leaves from its apply (preserves the override)      │
   │                                                                       │
   │   4.  controller   ─── /api/v1/divergence ───►   source-of-truth      │
   │       (reporter)       (HTTP POST,                (orbital)           │
   │                         structured payload)                           │
   │                                                                       │
   │       payload = { orbId, field, intended, override, who, when }       │
   │                                                                       │
   │   5.  human admin at source-of-truth decides per-override:            │
   │                                                                       │
   │       ACCEPT  ───────────────► mutate orbital data; next bundle       │
   │                                 carries the override AS the intent.   │
   │                                 Override becomes source of truth.     │
   │                                                                       │
   │       TAKEOVER ─── bundle ────► next bundle includes spec.takeover[]. │
   │                    (oci push)   Controller force-conflicts the field  │
   │                                 back to its bundle value. Admin's     │
   │                                 override is reverted, with intent.    │
   │                                                                       │
   └───────────────────────────────────────────────────────────────────────┘
```

### Two wire-level interfaces make this distinctive

Most K8s controllers have a **one-way** primary write loop (intent flows in, controller writes to cluster). Ours has **two wires** in addition:

| Direction | Wire | Purpose | Conventional in K8s? |
|---|---|---|---|
| Down (intent) | OCI bundle push (orbital → orb → controller) | source-of-truth → cluster | ✅ Yes — every GitOps tool does this |
| Up (report) | HTTP POST `/api/v1/divergence` (reporter → orbital) | cluster → source-of-truth, structured | ✗ Rare — drift detection exists but typically lives in a UI/dashboard, not as a structured controller-to-source feedback channel |
| Down (response) | `spec.takeover[]` in next bundle | source-of-truth response → cluster | ✗ Rare — auto-correction is the K8s norm; explicit per-field reclamation from upstream is unusual |

The upstream wire and the structured-takeover-as-bundle-payload are the two pieces I could not find in any K8s-native project.

### What other projects do at each workflow step

| Step | EKS Add-ons | ArgoCD | Flux | ClusterClass | **Us** |
|---|---|---|---|---|---|
| 1. Push intent | manual install + auto-update | git → cluster | git → cluster | topology → cluster | bundle → cluster |
| 2. Admin override | direct field write | direct field write | direct field write | only via defined variables | per-leaf SSA with `local:*` |
| 3. Controller respects override | passive (just doesn't manage that field) | `managedFieldsManagers` excludes from diff | `ssa: Merge` annotation, per-resource | constrained surface, no arbitrary override | `omitAdminOwnedFields` excludes from apply |
| 4. Report upstream | ✗ nothing | drift visible in UI only | drift logged | ✗ | **structured HTTP POST with full override payload** |
| 5. Upstream decides | ✗ hope | manual git edit OR auto-revert (self-heal) | auto-revert OR config map allowlist | n/a | **accept (data mutation) or takeover (control signal back through bundle)** |

The novel composition is rows 4 and 5: an upstream system that **receives structured override data** and **emits a structured response signal** through the same delivery path that carries the original intent.

### Why this composition is uncommon in K8s

The K8s ecosystem assumes one of two postures:

- **Source-of-truth wins** (Flux default, ArgoCD self-heal, Pulumi refresh): controller auto-corrects drift. No human decision needed inside the control loop.
- **Human-in-the-middle via GitOps PR**: drift becomes a Git PR. Human reviews and merges. Decision happens **outside** the controller, in Git tooling.

We're doing a third thing: **human decision inside the loop, mediated by the source-of-truth system itself.** This makes sense for our use case (CMDB-backed compliance, audit trail per server, central admin approval workflow tied to inventory) but isn't an assumption K8s tooling is built around.

The closest non-K8s analog is **traditional ITSM/CMDB drift management** — ServiceNow drift workflows, Ansible Tower with approval gates, network automation platforms like Cisco NSO and Juniper Apstra. Those domains routinely have human approval gates inside the config-management loop. We're applying that pattern in a K8s controller, where it's uncommon.

### Where we extend the closest K8s analog

EKS Managed Add-ons is the closest K8s precedent. We add two things they don't:

- **Per-person attribution** via the `local:<id>` prefix convention (EKS has one fixed `eks` manager — no per-actor identity)
- **Explicit upstream reporting and takeover loop** (EKS just hopes manual customizations survive add-on updates — and warns the operator they might not)

We're not inventing new mechanics. We're combining existing mechanics into a workflow that K8s tooling doesn't pre-can.

---

## Field manager identity conventions across projects

| Project | Field manager string(s) | Notes |
|---|---|---|
| EKS Managed Add-ons | `eks` | single fixed string |
| ArgoCD | `argocd-controller` (proposed) | single fixed string per proposal |
| Flux | `kustomize-controller` | single fixed string |
| HPA | `kube-controller-manager` | k8s core controller |
| Istio sidecar injector | `istio-sidecar-injector` | per-component named |
| cert-manager | `cert-manager-certificates-issuing` | per-subsystem named |
| external-dns | `external-dns` | single fixed string |
| **configbundle (us)** | `configbundle-controller` + `local:<id>` | controller + per-person prefix |

No other project I found uses a prefix convention with arbitrary suffix for per-actor attribution. Our `local:daniel`, `local:alice`, etc. is a deliberate but non-conventional choice — see the discussion in [configbundle/docs/runbooks/divergence-e2e-local.md](../../../orbital/docs/runbooks/divergence-e2e-local.md) (orbital repo) about how that convention has already bitten us once (hard-coded `local:admin` filter dropped `local:daniel` overrides silently).

---

## Implications for our roadmap

Things that follow from this survey:

1. **The CLI wrapper recommendation gets stronger.** SSA's primary audience is controllers, not humans. Every K8s analog we found exposes admin override through a wrapper or a structured "variables" surface, not raw SSA. Our `cb-override` CLI proposal aligns with how the ecosystem actually does this.

2. **The `local:*` prefix is a real deviation worth documenting prominently.** Other projects use fixed identifiers. Our prefix convention buys per-actor attribution at the cost of being non-standard.

3. **The takeover loop is the genuine differentiator.** Nobody else does explicit upstream-driven reclamation. If this becomes a problem (operators don't understand it, orbital UI gets confused), it's where we're farthest from precedent. Worth keeping a sharp eye on.

4. **EKS-style "no upstream reporting, just hope" is a viable simpler model.** If the divergence reporter ever becomes a maintenance burden, that's a fallback — accept that overrides happen, don't report them, let next bundle from orbital overwrite (or not). Simpler workflow, less audit fidelity.

---

## Sources

Research conducted via web search through K8s ecosystem documentation:

- [Amazon EKS — Determine fields you can customize for add-ons](https://docs.aws.amazon.com/eks/latest/userguide/kubernetes-field-management.html)
- [ArgoCD Server-Side Apply proposal](https://argo-cd.readthedocs.io/en/latest/proposals/server-side-apply/)
- [ArgoCD managedFieldsManagers diff customization](https://oneuptime.com/blog/post/2026-02-26-argocd-managedfields-manager-diff/view)
- [ArgoCD ignoreDifferences with managedFieldsManagers (GitHub issue #7926)](https://github.com/argoproj/argo-cd/issues/7926)
- [Cluster API ClusterClass proposal](https://github.com/kubernetes-sigs/cluster-api/blob/main/docs/proposals/20210526-cluster-class-and-managed-topologies.md)
- [Cluster API: Changing a ClusterClass](https://cluster-api.sigs.k8s.io/tasks/experimental-features/cluster-class/change-clusterclass)
- [Flux CD field ownership](https://oneuptime.com/blog/post/2026-03-05-flux-cd-resource-ownership/view)
- [Kubernetes Server-Side Apply reference](https://www.kubernetes.io/docs/reference/using-api/server-side-apply/)
- [controller-runtime partial SSA behavior](https://dev.to/suin/controller-runtime-what-happens-when-you-do-partial-server-side-apply-1oi0)
- [Anthos Config Management multi-source configuration](https://cloud.google.com/anthos-config-management/docs/how-to/multiple-repositories)
- [Pulumi Kubernetes drift detection](https://www.pulumi.com/docs/iac/operations/stack-management/drift/)

---

## Notes for revision

Things this draft does not address but should once revised:

- [ ] Capacitor / Telco network function projects (5G CNFs often have similar "central config + local override" patterns) — didn't search deeply enough
- [ ] Knative serving SSA usage
- [ ] Pulumi-Kubernetes operator pattern (their `ServerSideApply` mode)
- [ ] Whether OpenShift's `cluster-config-operator` does anything analogous
- [ ] Concrete code references in EKS controller / ArgoCD source for the managedFields walking implementation (so we can compare our code directly)
- [ ] How divergence acceptance UIs (if any) work in adjacent systems — does anything else surface "drift detected, admin acks or rejects" as a workflow?
