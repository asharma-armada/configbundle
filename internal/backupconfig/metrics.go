package backupconfig

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	armadav1 "github.com/armada/configbundle/api/v1"
)

// bc-controller metrics, per docs/reference/DOMAIN-CONTROLLER.md §6. Intent
// (.spec) is NOT a metric — the cloud/orbital already authored it, so there is
// no `_spec_`/intent gauge and no PromQL `spec != status` drift query (the
// controller already computes drift; that is reconciliation). We emit only what
// the cloud cannot otherwise know:
//   - reconcile OUTCOME: reconcile_success (per-object 0/1, absent = skip),
//     reconcile timestamp, error counter;
//   - OBSERVED state: resource-presence gauges + the etcd artifact metrics
//     (snapshot freshness/count/size — the reads-only outcome layer).
//
// One live read fans out to these gauges AND to CR status independently; metrics
// never block on a status write. Labels are identity only ({cluster}); never a
// value, timestamp, or unbounded id.
var (
	// backupConfigReconcileTimestamp is set to time.Now().Unix() after every
	// successful reconcile. Alerts on "bc-controller has stopped reconciling
	// this CR" fire when (time() - <this>) exceeds an expected reconcile
	// cadence. Populated only on success paths — failures leave the last
	// success timestamp alone so operators see how stale the current state is.
	backupConfigReconcileTimestamp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "configbundle_backupconfig_reconcile_timestamp_seconds",
		Help: "Unix timestamp of the last successful reconcile of this BackupConfig CR. Absent series = never reconciled.",
	}, []string{"cluster"})

	// backupConfigReconcileErrors counts reconcile failures by cause. Labels
	// stay bounded (fixed set of reason strings: VeleroPatchFailed,
	// EtcdPatchFailed). Rate() over this counter tells cloud dashboards
	// which failure mode is dominant.
	backupConfigReconcileErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "configbundle_backupconfig_reconciliation_errors_total",
		Help: "Cumulative reconcile failures per BackupConfig CR, labelled by failure reason (VeleroPatchFailed | EtcdPatchFailed).",
	}, []string{"cluster", "reason"})

	// backupConfigReconcileSuccess is the per-object "did the last reconcile
	// converge?" level: 1 = converged, 0 = failed. ABSENT for a deliberately
	// skipped BackupConfig (no velero/etcd block) or one never reconciled — so
	// `== 0` alerts fire only for genuinely-broken clusters. The load-bearing
	// "which cluster is failing?" signal. Set from recordReconcileSuccess/Error;
	// deleted by removeReconcileSuccess on skip and on CR delete.
	backupConfigReconcileSuccess = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "configbundle_backupconfig_reconcile_success",
		Help: "1 if this BackupConfig's last reconcile converged, 0 if it failed. Absent = deliberately skipped or never reconciled.",
	}, []string{"cluster"})

	// veleroSchedulePresent = 1 when the underlying Velero Schedule CR that
	// bc-controller manages for this cluster exists on-cluster, 0 when spec
	// asked for it but live is missing (deleted OOB, CRD absent, or apply
	// pending). Series is deleted entirely when spec.velero is nil, so
	// "no series" means "not managed" — distinct from "should exist but is
	// gone (0)". Same semantic for etcd below.
	veleroSchedulePresent = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "configbundle_backup_velero_schedule_present",
		Help: "1 when the live Velero Schedule for this BackupConfig exists on-cluster; 0 when spec.velero is set but the live resource is missing.",
	}, []string{"cluster"})

	etcdCronJobPresent = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "configbundle_backup_etcd_cronjob_present",
		Help: "1 when the live etcd CronJob for this BackupConfig exists on-cluster; 0 when spec.etcd is set but the live resource is missing.",
	}, []string{"cluster"})
)

// etcdArtifacts is the snapshot store the etcd artifact Collector renders each
// scrape. Fed by the reconciler from its live read of the backup store.
var etcdArtifacts = newEtcdArtifactStore()

// backupObservedCfg backs the observed-config info metrics (status_etcd_info /
// status_velero_info) — the producer config read live each reconcile.
var backupObservedCfg = newBackupObservedStore()

func init() {
	metrics.Registry.MustRegister(
		backupConfigReconcileTimestamp, backupConfigReconcileErrors, backupConfigReconcileSuccess,
		veleroSchedulePresent, etcdCronJobPresent,
		newEtcdArtifactCollector(etcdArtifacts),
		newBackupObservedCollector(backupObservedCfg),
	)
}

// recordPresence updates the resource-present gauges from a live snapshot.
// Nil block for a mechanism spec-asks-for = live-missing (0). Nil block for a
// mechanism spec-does-not-ask-for = delete the series entirely (mechanism
// not managed, absence is not "missing"). Called from the same live-read
// snapshot as the other observed metrics so all surfaces stay coherent.
func recordPresence(cluster string, bc *armadav1.BackupConfig, live armadav1.ObservedBackup) {
	labels := prometheus.Labels{"cluster": cluster}
	if bc.Spec.Velero != nil {
		if live.Velero != nil {
			veleroSchedulePresent.With(labels).Set(1)
		} else {
			veleroSchedulePresent.With(labels).Set(0)
		}
	} else {
		veleroSchedulePresent.Delete(labels)
	}
	if bc.Spec.Etcd != nil {
		if live.Etcd != nil {
			etcdCronJobPresent.With(labels).Set(1)
		} else {
			etcdCronJobPresent.With(labels).Set(0)
		}
	} else {
		etcdCronJobPresent.Delete(labels)
	}
}

// recordReconcileSuccess marks a successful reconcile: bumps the last-success
// timestamp and sets the reconcile_success level to 1.
func recordReconcileSuccess(cluster string, now int64) {
	backupConfigReconcileTimestamp.With(prometheus.Labels{"cluster": cluster}).Set(float64(now))
	backupConfigReconcileSuccess.With(prometheus.Labels{"cluster": cluster}).Set(1)
}

// recordReconcileError increments the failure counter for the given reason and
// drops the reconcile_success level to 0. Reason strings should stay bounded —
// pick from a fixed enum, do not interpolate error messages.
func recordReconcileError(cluster, reason string) {
	backupConfigReconcileErrors.With(prometheus.Labels{"cluster": cluster, "reason": reason}).Inc()
	backupConfigReconcileSuccess.With(prometheus.Labels{"cluster": cluster}).Set(0)
}

// removeReconcileSuccess deletes a cluster's reconcile_success series so it
// becomes absent — on skip (absent = skip, never 0) or CR delete.
func removeReconcileSuccess(cluster string) {
	backupConfigReconcileSuccess.DeleteLabelValues(cluster)
}

// -----------------------------------------------------------------------------
// etcd ARTIFACT metrics: the observed state of the actual snapshots in the
// backup store (not the CronJob config). bc is the SOLE source for etcd — no
// downstream owner emits these. Collector-over-snapshot, rebuilt each scrape,
// mirroring serverconfig's idracObservedCollector: a new snapshot replaces the
// series rather than leaving a stale one.
//
//	configbundle_backupconfig_status_etcd_last_snapshot_seconds{cluster}
//	configbundle_backupconfig_status_etcd_snapshot_count{cluster}
//	configbundle_backupconfig_status_etcd_latest_bytes{cluster}
//
// Do NOT add velero equivalents — Velero owns its backups; defer to velero_*.
// See docs/reference/BACKUP.md.
// -----------------------------------------------------------------------------

// etcdArtifactStore is a concurrency-safe latest-inventory map keyed by cluster
// (BackupConfig name). The reconciler writes from its live store read; the
// scrape goroutine reads. Holds only the current view. A read failure leaves
// the last entry in place (stale), so metrics show last-known while the
// reconcile-timestamp gap signals the staleness — same discipline as
// serverconfig.
type etcdArtifactStore struct {
	mu        sync.RWMutex
	byCluster map[string]etcdSnapshotInventory
}

func newEtcdArtifactStore() *etcdArtifactStore {
	return &etcdArtifactStore{byCluster: map[string]etcdSnapshotInventory{}}
}

func (s *etcdArtifactStore) set(cluster string, inv etcdSnapshotInventory) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byCluster[cluster] = inv
}

func (s *etcdArtifactStore) remove(cluster string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byCluster, cluster)
}

func (s *etcdArtifactStore) snapshot() map[string]etcdSnapshotInventory {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]etcdSnapshotInventory, len(s.byCluster))
	for k, v := range s.byCluster {
		out[k] = v
	}
	return out
}

type etcdArtifactCollector struct {
	store        *etcdArtifactStore
	lastSnapDesc *prometheus.Desc
	countDesc    *prometheus.Desc
	bytesDesc    *prometheus.Desc
}

func newEtcdArtifactCollector(store *etcdArtifactStore) *etcdArtifactCollector {
	return &etcdArtifactCollector{
		store: store,
		lastSnapDesc: prometheus.NewDesc(
			"configbundle_backupconfig_status_etcd_last_snapshot_seconds",
			"Unix time of the newest etcd snapshot in the backup store (bc is the sole source). Absent when no snapshot has been observed.",
			[]string{"cluster"}, nil),
		countDesc: prometheus.NewDesc(
			"configbundle_backupconfig_status_etcd_snapshot_count",
			"Number of etcd snapshot objects observed under the cluster's prefix (0 when the store is empty).",
			[]string{"cluster"}, nil),
		bytesDesc: prometheus.NewDesc(
			"configbundle_backupconfig_status_etcd_latest_bytes",
			"Size in bytes of the newest etcd snapshot object. Absent when no snapshot has been observed.",
			[]string{"cluster"}, nil),
	}
}

func (c *etcdArtifactCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.lastSnapDesc
	ch <- c.countDesc
	ch <- c.bytesDesc
}

func (c *etcdArtifactCollector) Collect(ch chan<- prometheus.Metric) {
	for cluster, inv := range c.store.snapshot() {
		ch <- prometheus.MustNewConstMetric(c.countDesc, prometheus.GaugeValue, float64(inv.Count), cluster)
		if inv.Count > 0 && !inv.LatestModified.IsZero() {
			ch <- prometheus.MustNewConstMetric(c.lastSnapDesc, prometheus.GaugeValue, float64(inv.LatestModified.Unix()), cluster)
			ch <- prometheus.MustNewConstMetric(c.bytesDesc, prometheus.GaugeValue, float64(inv.LatestBytes), cluster)
		}
	}
}

// recordEtcdArtifacts publishes a cluster's observed snapshot inventory to the
// artifact metrics. removeEtcdArtifacts drops it (BackupConfig deleted, or etcd
// no longer in spec).
func recordEtcdArtifacts(cluster string, inv etcdSnapshotInventory) { etcdArtifacts.set(cluster, inv) }
func removeEtcdArtifacts(cluster string)                            { etcdArtifacts.remove(cluster) }

// -----------------------------------------------------------------------------
// Observed-config info metrics: the producer config bc reads live each reconcile,
// surfaced as one info series per managed mechanism — the observed-config surface
// parallel to serverconfig's status_idracsettings_info. Per DOMAIN-CONTROLLER.md §6:
// observed managed config (bools+strings) → one status_<domain> info metric
// (value 1, config in labels); numeric truth → the gauges above.
//
//	configbundle_backupconfig_status_etcd_info{cluster,schedule,location,enabled}   1
//	configbundle_backupconfig_status_velero_info{cluster,schedule,location,enabled} 1
//
// Collector-over-snapshot (rebuilt each scrape) so a schedule/location change
// replaces the series rather than leaving a stale label combination.
// -----------------------------------------------------------------------------

// blockLabels is one producer block's observed config, pre-rendered as label
// strings (*bool → "true"/"false"/"unknown"; *string → value or "").
type blockLabels struct {
	schedule string
	location string
	enabled  string
}

type observedConfig struct {
	etcd   *blockLabels
	velero *blockLabels
}

type backupObservedStore struct {
	mu        sync.RWMutex
	byCluster map[string]observedConfig
}

func newBackupObservedStore() *backupObservedStore {
	return &backupObservedStore{byCluster: map[string]observedConfig{}}
}

func (s *backupObservedStore) set(cluster string, oc observedConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byCluster[cluster] = oc
}

func (s *backupObservedStore) remove(cluster string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byCluster, cluster)
}

func (s *backupObservedStore) snapshot() map[string]observedConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]observedConfig, len(s.byCluster))
	for k, v := range s.byCluster {
		out[k] = v
	}
	return out
}

type backupObservedCollector struct {
	store      *backupObservedStore
	etcdDesc   *prometheus.Desc
	veleroDesc *prometheus.Desc
}

func newBackupObservedCollector(store *backupObservedStore) *backupObservedCollector {
	labels := []string{"cluster", "schedule", "location", "enabled"}
	return &backupObservedCollector{
		store: store,
		etcdDesc: prometheus.NewDesc(
			"configbundle_backupconfig_status_etcd_info",
			"Observed etcd producer config (schedule/location/enabled) read live from the CronJob. Value always 1 (info metric); one series per managed BackupConfig.",
			labels, nil),
		veleroDesc: prometheus.NewDesc(
			"configbundle_backupconfig_status_velero_info",
			"Observed Velero producer config (schedule/location/enabled) read live from the Schedule. Value always 1 (info metric); one series per managed BackupConfig.",
			labels, nil),
	}
}

func (c *backupObservedCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.etcdDesc
	ch <- c.veleroDesc
}

func (c *backupObservedCollector) Collect(ch chan<- prometheus.Metric) {
	for cluster, oc := range c.store.snapshot() {
		if b := oc.etcd; b != nil {
			ch <- prometheus.MustNewConstMetric(c.etcdDesc, prometheus.GaugeValue, 1, cluster, b.schedule, b.location, b.enabled)
		}
		if b := oc.velero; b != nil {
			ch <- prometheus.MustNewConstMetric(c.veleroDesc, prometheus.GaugeValue, 1, cluster, b.schedule, b.location, b.enabled)
		}
	}
}

// recordObservedConfigInfo publishes a cluster's observed producer config to the
// status_etcd_info / status_velero_info info metrics, from the same live snapshot the
// other observed surfaces use. A nil block (mechanism not managed) omits that
// block's series.
func recordObservedConfigInfo(cluster string, live armadav1.ObservedBackup) {
	var oc observedConfig
	if e := live.Etcd; e != nil {
		oc.etcd = &blockLabels{schedule: strLabel(e.Schedule), location: strLabel(e.Location), enabled: boolLabel(e.Enabled)}
	}
	if v := live.Velero; v != nil {
		oc.velero = &blockLabels{schedule: strLabel(v.Schedule), location: strLabel(v.Location), enabled: boolLabel(v.Enabled)}
	}
	backupObservedCfg.set(cluster, oc)
}

// removeObservedConfigInfo drops a cluster's info series (CR deleted or skipped).
func removeObservedConfigInfo(cluster string) { backupObservedCfg.remove(cluster) }

func strLabel(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func boolLabel(b *bool) string {
	if b == nil {
		return "unknown"
	}
	if *b {
		return "true"
	}
	return "false"
}
