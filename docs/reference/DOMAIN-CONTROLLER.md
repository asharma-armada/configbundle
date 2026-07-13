# Domain Controller Template

A standard for building the controllers that manage one class of resource on
behalf of the cloud. `serverconfig` (iDRAC) and `backupconfig` (etcd + Velero
backups) are the first two; this doc is the shape every future one should follow.

Individual controllers may deviate — the differences are real and sanctioned
(see [Sanctioned deviations](#sanctioned-deviations)). But they all present the
same approach.

---

## The approach, in one line

> A **domain controller** owns one class of resource that lives **outside**
> Kubernetes. It continuously drives that resource toward the intent declared in
> `.spec`, and reflects the resource's observed reality back into `.status` and
> the metrics pipeline. **Intent flows down; observed truth flows up** — so the
> gap between desired and actual stays visible in both Kubernetes and the cloud.

Two-sentence version, for a slide:

> A domain controller is a two-way bridge for one class of out-of-cluster
> resource: it reconciles the resource toward the intent in `.spec`, and from a
> single continuous observation it reports the resource's actual state into both
> `.status` (the Kubernetes-native view) and the metrics pipeline (the cloud
> view). Intent flows down, observed truth flows up — the cloud sees edge reality
> without ever reaching into the cluster.

Everything below is the mechanics of that one idea.

---

## 1. `.spec` is intent; `.status` is observed

The `spec`/`status` split **is** the desired-vs-observed split — that is the
label. Do not add an `observed:` (or `atProvider:`-style) wrapper under status;
`status` already means "observed." (Core Kubernetes never wraps: `Pod.status.podIP`,
`Deployment.status.replicas` sit flat.)

- **Mirror every *observable* spec field under the same status path.**
  `spec.fooSettings.fieldA` (desired) ↔ `status.fooSettings.fieldA` (observed).
  Same shape → drift is a generic field diff (`diff(spec.foo, status.foo)`), so
  one reflection-based differ serves every controller. This is the single
  biggest reason the shape is standardized: **shared machinery.**
- **Status is a superset, not a bijection.** It also carries observation-only
  fields with no spec twin — freshness, health, counts, timestamps. These are
  often the *most valuable* signals. (`Deployment.status.availableReplicas` has
  no `spec` counterpart; neither does an etcd `lastSnapshotTime`.)
- **A spec field that isn't observable has no twin** — write-only, one-shot, or
  no read-back. Don't fabricate an empty mirror.

```yaml
apiVersion: armada.ai/v1
kind: FooConfig
spec:
  fooSettings:
    fieldA: <desired>          # observable → mirrored into status
    fieldB: <desired>
    # a write-only / one-shot field would have NO status twin
status:
  observedGeneration: 5
  conditions:                  # see §4
    - type: Reconciled
      status: "True"
  lastAppliedAt: <ts>          # cross-cutting observation-only → top level
  fooSettings:
    fieldA: <observed_last>    # 1:1 mirror of spec.fooSettings.fieldA
    fieldB: <observed_last>
    lastObservedAt: <ts>       # domain observation-only → in the block, no spec twin
    health: <...>
```

## 2. Observation is direct; one read feeds two surfaces

Every domain controller **directly observes** its external system — it polls the
system and reports what it sees. There is no lesser, second-hand form of
observation; `backupconfig` reading Azure blob state is exactly as direct as
`serverconfig` reading Redfish attributes.

**One live read per reconcile fans out to `.status` AND metrics, independently.**

```
              ┌── .status.fooSettings   (Kubernetes-native view: kubectl, GitOps)
observe() ────┤
              └── configbundle_foo_*     (cloud view: metrics pipeline → Prometheus)
```

- **Metrics MUST NOT be derived from `.status`.** Both come from the same read.
  (Deriving metrics from status couples them to a status write that can lose a
  `RetryOnConflict` race — silently dropping gauges. Keep them independent.)
- `.status` is the K8s surface; metrics are the cloud surface. Neither is the
  other's source of truth — the *observation* is.

## 3. Classify each external system: writes-and-reads vs reads-only

A controller may touch more than one external system. Classify each:

| Relationship | Meaning | Status role |
|---|---|---|
| **writes-and-reads** | the controller both actuates and observes it | **config fidelity** → mirror the spec fields (`spec.X` ↔ `status.X`) |
| **reads-only** | the controller observes it but something else writes it | **outcome / monitoring** → observation-only fields, no spec twin |

> **The number of independent health signals (conditions and/or Prometheus
> alerts) equals the number of independently-failing observed layers** — not a
> fixed count.

- A controller that only *writes-and-reads* one system needs **one** signal:
  observed == desired, done. (Its config *is* the outcome.)
- A controller that also *reads-only* a downstream outcome needs a **second**
  signal for that outcome, because config-fidelity and outcome can diverge
  (config perfect, downstream actor failed). Where that second verdict lives —
  a CR condition vs. a PrometheusRule on the raw metric — is a per-controller
  choice; the raw observed fact belongs on `.status` either way.

## 4. Reconcile behavior

- **Watch in-band, poll out-of-band.** Kubernetes-native inputs (the CR, owned
  sub-objects) are *watchable* → use `For`/`Owns`, react event-driven and
  instantly. External systems (iDRAC, blob storage) are *out-of-band* — K8s has
  no visibility, nothing pushes events → **poll** on an interval. The interval
  is the switch for out-of-band observation; when it's unset the controller is
  purely watch-driven. (Do not add a redundant on/off flag beside the interval.)
- **Always SSA-apply; do not gate the apply on a pre-diff.** SSA is idempotent.
  Use the computed delta for the status summary / events, never to skip the
  apply (skipping silently drops metadata reconciliation — owner refs, labels).
- **Reconcile drift, then back off.** On observing drift, attempt to converge.
  On failure, return the error so controller-runtime applies exponential backoff
  and retries — same as any well-behaved controller.
- **Surface errors; never swallow them.** A managed resource that is unreachable
  or misconfigured is a fault the operator must see (`Reconciled=False` + a
  Warning event). Distinguish it from *deliberately out of scope* (see Unknown,
  below).

## 5. Status conventions

**Conditions** — via `meta.SetStatusCondition` (the apimachinery-canonical
upsert). Tri-state, PascalCase types, per-condition `observedGeneration`.

| `status` | Meaning |
|---|---|
| `True` | managed and converged |
| `False` | managed and **determined not converged** — a real fault, alert on this |
| `Unknown` | not being determined — deliberately skipped / out of scope |

- **`Skipped` → `Unknown`, never `False`.** A CR the controller intentionally
  doesn't manage (not allow-listed, missing prerequisite) isn't broken. Keeping
  it out of `False` means an operator alerting on `Reconciled=False` pages only
  for genuinely-broken resources. (Same reasoning as `NodeReady=Unknown`.)
- **`LastTransitionTime` moves only on a status *flip*** (K8s norm). It lies for
  "still True, but the controller just did more work." So carry a separate
  **`lastAppliedAt`** timestamp — the truthful "is the controller still working?"
  signal — bumped on every successful reconcile.
- **Condition `message` describes state** ("all managed settings match intent"),
  not the last action. **Per-action history goes to Kubernetes Events**, not the
  condition — a `Normal` event on apply, a `Warning` event on failure. Events
  aggregate/dedup, so emitting on every failing reconcile yields one aggregated
  event, not a flood.

**Phases** (`.status.phase`) are a coarse human-readable rollup
(`Pending`/`Applied`/`Diverged`/`Skipped`). Conditions are the machine signal;
phase is the at-a-glance one.

## 6. Metrics conventions

Namespace `configbundle_*`, taxonomy `configbundle_<kind>_<subject>_<measure>`.
Never `armada_*`. One live read fans out to status AND metrics (§2) — metrics are
never derived from status.

### Emit only what the cloud can't already know

**Intent (`.spec`) is NOT a metric.** It is declarative input the cloud authored
and pushed down (for us, the orbital artifact) — echoing it back as an edge metric
is redundant, since the metric's consumer already has it. There is no
`configbundle_<kind>_spec_*` and no PromQL "`spec != status`" drift query; the
controller already computes drift (that's reconciliation). Emit only the two
things the cloud can't know unless the edge reports them:

1. **Reconcile outcome** — did reconciliation occur, and did it succeed?
2. **Observed state** — the current live `.status` values.

(This is exactly Crossplane's first-party choice: reconcile/latency *outcome*
metrics, no spec mirror.)

### Family 1 — reconcile outcome

- **Loop liveness (per-controller, free):** the controller-runtime metrics
  (`controller_runtime_reconcile_total`, `_reconcile_errors_total`,
  `_reconcile_time_seconds`) answer "is the loop running / erroring."
- **Per-object success:** `configbundle_<kind>_reconcile_success{name} 0/1` —
  `1` = converged, `0` = failed, **absent = deliberately skipped** (so skips
  never page — no `Unknown` state needed in the metric). This is the load-bearing
  new signal: it tells the cloud *which* resource is broken.
- **Failure reason is NOT in the metric.** A boolean has no room for it — reason
  lives in the CR's `Reconciled` condition + Events (`kubectl describe`). The
  metric says "this one's wrong"; the CR says why. **Do NOT emit conditions as
  state-set metrics** — the rich tri-state condition stays on the CR.

### Family 2 — observed state

Surface the current observed values for fleet inventory/dashboards, in one of two
shapes depending on what the value *is*:

- **Observed managed *config*** — the fields you set and read back (booleans and
  strings: iDRAC settings, a backup schedule/location/enabled) → **one info
  metric per domain**, `configbundle_<kind>_status_<domain>_info`, value always `1`,
  the fields carried in labels. This is the inventory/display surface — you
  filter it and `count by (…)`, you don't threshold it. Bundle bools *and*
  strings into the one info series (do not split bools into separate gauges —
  convergence alerting is Family 1's job, not this metric's).
  Examples: serverconfig's `status_idracsettings_info`; backupconfig's `status_etcd_info`
  and `status_velero_info`.
- **Observed *numeric truth*** you alert or graph on (counts, timestamps, sizes —
  an etcd snapshot's age, count, bytes) → **per-field gauges**,
  `configbundle_<kind>_status_<domain>_<measure>`, the number in the value. A
  number MUST be a gauge value, never an info label — you cannot do
  `time() - <label>` or `<label> == 0` in PromQL. Examples: backupconfig's
  `status_etcd_last_snapshot_seconds` / `_snapshot_count` / `_latest_bytes`.

Emit info metrics via a Collector-over-snapshot so a changed field replaces the
series rather than leaving a stale label combination. Observed state overlaps
intent when converged; it earns its keep for dashboards and for the **diverged**
case, where it shows what reality actually is.

> The common core holds across controllers: **every domain controller exposes its
> observed config as `configbundle_<kind>_status_<domain>_info` and its numeric truth
> as `_<measure>` gauges.** A controller only *skips* a surface it genuinely has
> no data for (e.g. serverconfig has no numeric truth) — never because a field is
> "low value"; uniformity of the surface is the template's whole point.

### Cardinality: per-object is safe at edge scale — keep it bounded

Per-object metrics (a `name` label) are what tell the cloud *which* resource is
affected — worth the cost, and safe because:
- **Labels are identity only** (`name`, `namespace`, `cluster`). NEVER a value,
  timestamp, or unbounded id in a label — that is the one true cardinality lever.
- **Don't multiply dimensions.** One series per object per metric is *linear* in
  object count. (Dropping `_spec_` and not fanning out per-field already keeps it
  linear, not multiplicative.)
- Objects are **bounded by physical inventory** (servers per cluster; ~1
  BackupConfig per cluster) — tens to low thousands even federated, orders of
  magnitude below a single Prometheus's comfort zone. Crossplane defers per-object
  to KSM only because it manages *tens of thousands of unbounded* objects; that
  scale doesn't bind us.
- **Escape hatch:** if an object type ever gets huge, that is exactly when to
  offload to KSM-CRS (moving cardinality out of the controller's scrape/memory) or
  drop to aggregate-only. We are nowhere near it.

### Self-emit vs kube-state-metrics: a genuine tradeoff

Once reconcile-success is a plain `0/1` (absent = skip) and observed values are
gauges/info, there is no tri-state left for KSM Custom-Resource-State (CRS) to
flatten — so both are viable, and a shared template should present both rather
than mandate one:
- **Self-emit** (configbundle's default): the metric surface lives in-repo with
  the CRD, fed by the same live read (§2); full control of taxonomy.
- **KSM-CRS:** zero code — declarative YAML in the monitoring stack reads the CRD
  status; but the surface then lives in kube-prometheus-stack values, off the
  "controllers own their pipeline metrics" line.

### Stay in your lane

Emit only pipeline-domain metrics. Downstream operational metrics belong to their
owners (`velero_*`, `kube_*` via KSM) — do not re-emit them.

## 7. Sanctioned deviations

The common approach above holds for all domain controllers. These are the axes
on which they legitimately differ — name them, don't hide them:

- **Number of external systems** touched (one vs several), and the
  writes-and-reads / reads-only classification of each (§3).
- **Number of health signals** — falls out of §3, not chosen arbitrarily.
- **Where an outcome verdict lives** — a CR condition vs a PrometheusRule.
- **How much of `.spec` is observable** — a fully-observable resource mirrors
  nearly all of spec; an indirectly-produced one mirrors config-fidelity and
  carries the outcome as observation-only fields.

## 8. Reference implementations

- **`serverconfig` (iDRAC)** — the simplest shape. One external system, which it
  **writes-and-reads** (PATCHes an attribute, then reads it back). `.spec` mirrors
  almost entirely into `.status`; one condition (`Reconciled`); polls Redfish
  (out-of-band) on the observe interval.
- **`backupconfig` (etcd + Velero)** — a hybrid. **Writes-and-reads** the producer
  (the etcd `CronJob` / Velero `Schedule` — watched in-band, config mirrored into
  status) *and* **reads-only** the artifact store (Azure blob — polled
  out-of-band, surfaced as observation-only fields like `lastSnapshotTime`). The
  second, reads-only layer is why it carries an outcome signal beyond config
  fidelity. See [`BACKUP.md`](BACKUP.md).

> **Conformance note:** sc and bc currently still nest observed fields under
> `.status.observed.<domain>`; they are being reshaped to the flat
> `.status.<domain>` form prescribed in §1. Until that lands, read
> `status.observed.etcd` for `status.etcd`, etc.

## 9. Worked example — serverconfig metrics

Illustrates §6 on the serverconfig reference implementation. Three servers:
`node-a` healthy, `node-b` broken (iDRAC unreachable), `node-c` skipped (not on
the allowlist). (`reconcile_success` lands with the metrics reshape; the rest is
emitted today.)

Exposition (`/metrics`):

```
# reconcile outcome
configbundle_serverconfig_reconcile_success{server="node-a"} 1
configbundle_serverconfig_reconcile_success{server="node-b"} 0
# node-c skipped → no series emitted
configbundle_serverconfig_reconcile_timestamp_seconds{server="node-a"} 1783900800
configbundle_serverconfig_reconciliation_errors_total{server="node-b",reason="RedfishReadFailed"} 7
# observed state — info metric, value always 1
configbundle_serverconfig_status_idracsettings_info{server="node-a",oob_ip="10.0.0.11",orb_id="orb-aaa",firmware_version="7.10.30.00",ssh_enabled="false",ipmi_enabled="true",racadm_enabled="true"} 1
configbundle_serverconfig_status_idracsettings_info{server="node-b",oob_ip="10.0.0.12",orb_id="orb-bbb",firmware_version="7.00.00.00",ssh_enabled="true",ipmi_enabled="true",racadm_enabled="true"} 1
```

Queries (paste into Grafana):

| Question | PromQL | Returns |
|---|---|---|
| Which servers are failing *now*? | `configbundle_serverconfig_reconcile_success == 0` | `node-b` (`node-c` skipped → absent → never shows) |
| Alert on *sustained* failure | `configbundle_serverconfig_reconcile_success == 0`, `for: 10m` | one alert per broken server; a transient clears before 10m; skips can't fire |
| How long has it been broken? | `time() - configbundle_serverconfig_reconcile_timestamp_seconds` | seconds since last success (the timestamp only bumps on success) |
| How flaky is it? | `increase(configbundle_serverconfig_reconciliation_errors_total[1h])` | failure count — catches flapping even after `success` flips back to `1` |
| Observed iDRAC inventory | `configbundle_serverconfig_status_idracsettings_info` | one row/server; Grafana Table + *Labels to fields*, hide the Value column |
| Who has SSH on? | `count(configbundle_serverconfig_status_idracsettings_info{ssh_enabled="true"})` | count (filter/count *by* label — never threshold a label) |
| Firmware spread | `count by (firmware_version) (configbundle_serverconfig_status_idracsettings_info)` | count per version |

**Where's "drift"?** There is no `spec != status` query. *Converged?* → `reconcile_success`. *Actual value?* → the observed info metric. *Desired?* → the CR `.spec` / orbital, which already has it. Drift is reported (a boolean the controller computed), never re-derived in PromQL.

---

## Settled Decisions

- **`.spec` = intent, `.status` = observed — the prefix is the label.** No
  `observed:`/`atProvider:` wrapper under status; mirror observable spec fields
  at matching paths, and let status be a superset for observation-only truth.
- **Observation is always direct; one read fans out to `.status` and metrics
  independently.** Metrics are never derived from status.
- **Classify each external system writes-and-reads vs reads-only; health signals
  = independently-failing observed layers.** Not a fixed condition count.
- **Watch in-band (K8s objects), poll out-of-band (external systems).** The
  observe interval is the single switch for out-of-band observation.
- **`Skipped` is `Unknown`, not `False`.** False = managed and broken; Unknown =
  deliberately not managed.
- **Condition message = state; per-action history = Kubernetes Events.**
  `lastAppliedAt` (not `LastTransitionTime`) is the "still working" signal.
- **Intent (`.spec`) is not a metric.** It's declarative input the cloud already
  has (the orbital artifact); echoing it back is redundant. Metrics carry only
  what the cloud can't otherwise know: reconcile outcome + observed state. No
  `_spec_` metrics, no PromQL `spec != status` drift (the controller already
  computes drift — that's reconciliation).
- **Reconcile outcome = per-object `reconcile_success{name} 0/1` (absent = skip)
  + the free controller-runtime loop metrics.** Failure *reason* stays on the
  CR's condition + Events, never in the metric; do NOT emit conditions as
  state-set metrics.
- **Observed managed config (bools + strings) → ONE `status_<domain>_info` info
  metric (value 1, config in labels); numeric truth → per-field `_<measure>`
  gauges.** Never a number in a label (you can't `time() - <label>` or
  `<label> == 0`). The info-metric surface holds across controllers for
  uniformity — skip it only when there's genuinely no config to show, never for
  "low value." Labels are identity only otherwise (name/cluster) — the one
  cardinality lever; with it, per-object is safe at bounded edge scale.
- **Self-emit vs KSM-CRS is a genuine tradeoff** (no tri-state to lose once
  success is a boolean). A shared template presents both; configbundle's default
  is self-emit (in-repo, fed by the one live read).
