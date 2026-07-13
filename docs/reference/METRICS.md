# Metrics Reference

Every Prometheus metric published by the configbundle domain controllers, in the
style of kube-state-metrics' per-resource metric docs. Only **serverconfig** and
**backupconfig** emit `configbundle_*` metrics today; both also expose the free
`controller_runtime_*` / `workqueue_*` / `rest_client_*` families from the
framework (generic loop health, not listed here).

Namespace is `configbundle_*`, taxonomy `configbundle_<kind>_<subject>_<measure>`.
For *why* each metric has the shape it does (info metric vs gauge, absent=skip,
where the freshness verdict lives), see [`DOMAIN-CONTROLLER.md`](DOMAIN-CONTROLLER.md) §6.

Metric families group into **reconcile outcome** (did it converge?) and
**observed state** (what's actually out there?).

---

## serverconfig (sc-controller)

Identity label: `server` = the ServerConfig CR name.

| Metric name | Type | Labels | Description |
| --- | --- | --- | --- |
| `configbundle_serverconfig_reconcile_success` | Gauge | `server` | `1` = last reconcile converged, `0` = failed. **Absent = deliberately skipped** (not on the OOB allowlist / no OOB IP) or never reconciled — so `== 0` alerts never fire for skips. |
| `configbundle_serverconfig_reconcile_timestamp_seconds` | Gauge | `server` | Unix time of the last *successful* reconcile. `time() - <this>` = how long since it last worked. |
| `configbundle_serverconfig_reconciliation_errors_total` | Counter | `server`, `reason` | Cumulative reconcile failures. `reason` ∈ `MissingCredentials`, `CredentialsLoadFailed`, `RedfishReadFailed`, `RedfishPatchFailed`. |
| `configbundle_serverconfig_status_idracsettings_info` | Gauge (info, value `1`) | `server`, `oob_ip`, `orb_id`, `firmware_version`, `ssh_enabled`, `ipmi_enabled`, `racadm_enabled` | Observed iDRAC state, carried in labels (bools are `true`/`false`/`unknown`). The observed-config surface. **Emitted only after a successful Redfish read** — absent while a server is failing or skipped. |

## backupconfig (bc-controller)

Identity label: `cluster` = the BackupConfig CR name (one per Kubernetes cluster).

| Metric name | Type | Labels | Description |
| --- | --- | --- | --- |
| `configbundle_backupconfig_reconcile_success` | Gauge | `cluster` | `1` converged / `0` failed / **absent = skipped** (no velero or etcd block) or never reconciled. |
| `configbundle_backupconfig_reconcile_timestamp_seconds` | Gauge | `cluster` | Unix time of the last *successful* reconcile. |
| `configbundle_backupconfig_reconciliation_errors_total` | Counter | `cluster`, `reason` | Cumulative reconcile failures. `reason` ∈ `VeleroPatchFailed`, `EtcdPatchFailed`. |
| `configbundle_backupconfig_status_etcd_info` | Gauge (info, value `1`) | `cluster`, `schedule`, `location`, `enabled` | Observed etcd producer config, read live from the CronJob. The observed-config surface (parallel to sc's `status_idracsettings_info`). |
| `configbundle_backupconfig_status_velero_info` | Gauge (info, value `1`) | `cluster`, `schedule`, `location`, `enabled` | Observed Velero producer config, read live from the Schedule. |
| `configbundle_backup_velero_schedule_present` | Gauge | `cluster` | `1` = live Velero Schedule exists on-cluster, `0` = spec asks for it but it's missing, **absent = not managed**. |
| `configbundle_backup_etcd_cronjob_present` | Gauge | `cluster` | `1` / `0` / absent — same semantics for the live etcd CronJob. |
| `configbundle_backupconfig_status_etcd_last_snapshot_seconds` | Gauge | `cluster` | Unix time of the newest etcd snapshot in the backup store. The freshness signal `EtcdSnapshotStale` alerts on. **Emitted only when artifact observation is enabled** (`BACKUP_OBSERVE_INTERVAL` set + `AZURE_*` creds). |
| `configbundle_backupconfig_status_etcd_snapshot_count` | Gauge | `cluster` | Number of etcd snapshot objects under the cluster's prefix (`0` = empty store). **Observation-on only.** |
| `configbundle_backupconfig_status_etcd_latest_bytes` | Gauge | `cluster` | Size in bytes of the newest etcd snapshot. **Observation-on only.** |

---

## Notes

- **Identity label differs by controller** — sc uses `server`, bc uses `cluster`
  (each is the CR name). Deliberate; cross-controller queries can't share the
  label.
- **Prefix quirk in bc** — the two producer-presence gauges are
  `configbundle_backup_*`, while everything else is `configbundle_backupconfig_*`.
  Pre-existing; a candidate to normalize to `configbundle_backupconfig_*`.
- **`_info`-style metrics carry state in labels, value always `1`** — filter and
  `count by (...)` them; never threshold. Numeric truth (counts, timestamps,
  sizes) is always a gauge *value*, never a label.
- **Alerting rules** over these metrics live in
  [`config/prometheus/rules/prometheus-rule.yaml`](../../config/prometheus/rules/prometheus-rule.yaml);
  a starter Grafana dashboard in
  [`config/prometheus/dashboards/`](../../config/prometheus/dashboards/).

*Regenerate the tables when metrics change — grep `Name: "configbundle` and
`NewDesc(` under `internal/*/metrics.go`.*
