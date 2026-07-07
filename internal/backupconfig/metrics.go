package backupconfig

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	armadav1 "github.com/armada/configbundle/api/v1"
)

// Gauges expose the intended and observed value of every BackupConfig field so
// an external observer (Prometheus + Grafana) can detect drift without scraping
// CR status. Mirror of the serverconfig-controller metrics pattern.
//
// Labels:
//   - cluster : the BackupConfig CR name (one per Kubernetes cluster)
//   - kind    : "velero" or "etcd"
//   - field   : "enabled" | "schedule" | "location"
//
// Cardinality stays bounded: ~10 clusters per Galleon × 2 kinds × 3 fields = ~60
// series. Boolean fields (enabled) emit 0/1. String fields (schedule, location)
// emit 1 when set and absent when unset — operators visualize the string by
// scraping CR status, not the gauge value.
//
// Drift in PromQL:
//
//	configbundle_backup_field_intent != on(cluster, kind, field) configbundle_backup_field_observed
//	  unless configbundle_backup_field_ignored == 1
var (
	backupFieldIntent = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "configbundle_backup_field_intent",
		Help: "Intended value of a BackupConfig field. Boolean fields: 0=disabled, 1=enabled. String fields: 1 when set, absent when unset.",
	}, []string{"cluster", "kind", "field"})

	backupFieldObserved = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "configbundle_backup_field_observed",
		Help: "Confirmed value of a BackupConfig field from the controller's recordObserved ledger. Same encoding as intent.",
	}, []string{"cluster", "kind", "field"})

	// backupFieldIgnored is 1 when the parent ConfigBundle has an IgnoredEntry
	// for this {cluster, field}; absent otherwise. Mirror of the serverconfig
	// idracFieldIgnored gauge. Cluster-level ignore semantics are not yet wired
	// in spec.ignored[] — when divergence reporting for backup fields ships,
	// this gauge will be populated by ignoredFieldsForCluster below.
	backupFieldIgnored = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "configbundle_backup_field_ignored",
		Help: "1 when the parent ConfigBundle's spec.ignored[] lists this {cluster, kind, field}; absent otherwise. Used by alert rules to suppress drift alerts on admin-overridden fields.",
	}, []string{"cluster", "kind", "field"})

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

func init() {
	metrics.Registry.MustRegister(
		backupFieldIntent, backupFieldObserved, backupFieldIgnored,
		backupConfigReconcileTimestamp, backupConfigReconcileErrors,
		veleroSchedulePresent, etcdCronJobPresent,
	)
}

// recordPresence updates the resource-present gauges from a live snapshot.
// Nil block for a mechanism spec-asks-for = live-missing (0). Nil block for a
// mechanism spec-does-not-ask-for = delete the series entirely (mechanism
// not managed, absence is not "missing"). Called from the same live-read
// snapshot as the other observed metrics so all four surfaces stay coherent.
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

// recordReconcileSuccess marks a successful reconcile timestamp.
func recordReconcileSuccess(cluster string, now int64) {
	backupConfigReconcileTimestamp.With(prometheus.Labels{"cluster": cluster}).Set(float64(now))
}

// recordReconcileError increments the failure counter for the given reason.
// Reason strings should stay bounded — pick from a fixed enum, do not
// interpolate error messages.
func recordReconcileError(cluster, reason string) {
	backupConfigReconcileErrors.With(prometheus.Labels{"cluster": cluster, "reason": reason}).Inc()
}

func boolGauge(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// recordIntent updates the intent gauge for every field on the BackupConfig
// spec. Fields with no intent (nil pointer) have their series deleted so
// PromQL queries don't see stale values.
func recordIntent(bc *armadav1.BackupConfig) {
	if v := bc.Spec.Velero; v != nil {
		recordBlockIntent(bc.Name, "velero", v.Enabled, v.Schedule, v.Location)
	} else {
		recordBlockIntent(bc.Name, "velero", nil, nil, nil)
	}
	if e := bc.Spec.Etcd; e != nil {
		recordBlockIntent(bc.Name, "etcd", e.Enabled, e.Schedule, e.Location)
	} else {
		recordBlockIntent(bc.Name, "etcd", nil, nil, nil)
	}
}

// recordBlockIntent takes plain fields (not a typed *BackupBlock) so callers
// carrying either VeleroBackupSpec or EtcdBackupSpec can share this helper
// without wrapping in an interface.
func recordBlockIntent(cluster, kind string, enabled *bool, schedule, location *string) {
	enabledLabels := prometheus.Labels{"cluster": cluster, "kind": kind, "field": "enabled"}
	scheduleLabels := prometheus.Labels{"cluster": cluster, "kind": kind, "field": "schedule"}
	locationLabels := prometheus.Labels{"cluster": cluster, "kind": kind, "field": "location"}
	if enabled != nil {
		backupFieldIntent.With(enabledLabels).Set(boolGauge(*enabled))
	} else {
		backupFieldIntent.Delete(enabledLabels)
	}
	if schedule != nil {
		backupFieldIntent.With(scheduleLabels).Set(1)
	} else {
		backupFieldIntent.Delete(scheduleLabels)
	}
	if location != nil {
		backupFieldIntent.With(locationLabels).Set(1)
	} else {
		backupFieldIntent.Delete(locationLabels)
	}
}

// recordObservedMetric updates the observed gauges directly from a live-read
// snapshot. Deliberately takes the snapshot value (not `bc.Status`) so metrics
// are decoupled from status write success: if RetryOnConflict exhausts on the
// status Update, the Prom scrape still sees the honest values from THIS
// reconcile. Metrics feed Prom federation → cloud Grafana/Alertmanager —
// that pipeline is the primary observability surface and must not block on
// a K8s API race.
//
// A nil block (unmanaged mechanism or live resource missing) deletes the
// series so PromQL doesn't see stale values.
func recordObservedMetric(cluster string, observed armadav1.ObservedBackup) {
	if v := observed.Velero; v != nil {
		recordBlockObserved(cluster, "velero", v.Enabled, v.Schedule, v.Location)
	} else {
		recordBlockObserved(cluster, "velero", nil, nil, nil)
	}
	if e := observed.Etcd; e != nil {
		recordBlockObserved(cluster, "etcd", e.Enabled, e.Schedule, e.Location)
	} else {
		recordBlockObserved(cluster, "etcd", nil, nil, nil)
	}
}

func recordBlockObserved(cluster, kind string, enabled *bool, schedule, location *string) {
	enabledLabels := prometheus.Labels{"cluster": cluster, "kind": kind, "field": "enabled"}
	scheduleLabels := prometheus.Labels{"cluster": cluster, "kind": kind, "field": "schedule"}
	locationLabels := prometheus.Labels{"cluster": cluster, "kind": kind, "field": "location"}
	if enabled != nil {
		backupFieldObserved.With(enabledLabels).Set(boolGauge(*enabled))
	} else {
		backupFieldObserved.Delete(enabledLabels)
	}
	if schedule != nil {
		backupFieldObserved.With(scheduleLabels).Set(1)
	} else {
		backupFieldObserved.Delete(scheduleLabels)
	}
	if location != nil {
		backupFieldObserved.With(locationLabels).Set(1)
	} else {
		backupFieldObserved.Delete(locationLabels)
	}
}

// recordIgnored sets the ignored gauge to 1 for every {cluster, kind, field}
// in the ignoredFields set built from the parent ConfigBundle's spec.ignored[].
// Fields outside the set have their series deleted so the gauge doesn't go
// stale after the cloud admin reverses an Ignore decision.
//
// Currently a no-op because spec.ignored[] is server-scoped; cluster-level
// divergence reporting is a future spike. Kept here so the gauge surface is
// stable when that lands.
func recordIgnored(cluster string, ignored map[string]map[string]bool) {
	for _, kind := range []string{"velero", "etcd"} {
		for _, field := range []string{"enabled", "schedule", "location"} {
			labels := prometheus.Labels{"cluster": cluster, "kind": kind, "field": field}
			if ignored[kind][field] {
				backupFieldIgnored.With(labels).Set(1)
			} else {
				backupFieldIgnored.Delete(labels)
			}
		}
	}
}

// ignoredFieldsForCluster returns the set of {kind: {field: true}} entries
// that the parent ConfigBundle's spec.ignored[] lists for this BackupConfig.
//
// Best-effort: any failure to resolve the parent (no OwnerReference, CR not
// found, RBAC denied) returns an empty map + a debug log line. Currently
// returns an empty map regardless — IgnoredEntry today carries ServerOrbID
// (server-scoped); a cluster-scoped variant is a future spike.
func ignoredFieldsForCluster(ctx context.Context, c client.Client, bc *armadav1.BackupConfig, logger logr.Logger) map[string]map[string]bool {
	_ = ctx
	_ = c
	_ = bc
	_ = logger
	// Reserved for future cross-domain ignore semantics. Today: backup fields
	// are not addressable by IgnoredEntry (which is server-scoped). When the
	// schema gains cluster-scoped ignore entries, populate here and
	// recordIgnored will turn the gauge on. Until then, the gauge stays absent.
	return map[string]map[string]bool{}
}

// resolveParent loads the parent ConfigBundle by walking OwnerReferences.
// Returns nil when no parent is set or the lookup fails (logged as debug —
// the reconcile itself is not affected, only divergence-suppression labels).
func resolveParent(ctx context.Context, c client.Client, bc *armadav1.BackupConfig, logger logr.Logger) *armadav1.ConfigBundle {
	for _, ref := range bc.OwnerReferences {
		if ref.Kind != "ConfigBundle" {
			continue
		}
		var cb armadav1.ConfigBundle
		if err := c.Get(ctx, types.NamespacedName{Name: ref.Name}, &cb); err != nil {
			logger.V(1).Info("could not load parent ConfigBundle", "name", bc.Name, "owner", ref.Name, "err", err.Error())
			return nil
		}
		return &cb
	}
	return nil
}
