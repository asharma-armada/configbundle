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
//	armada_backup_field_intent != on(cluster, kind, field) armada_backup_field_observed
//	  unless armada_backup_field_ignored == 1
var (
	backupFieldIntent = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "armada_backup_field_intent",
		Help: "Intended value of a BackupConfig field. Boolean fields: 0=disabled, 1=enabled. String fields: 1 when set, absent when unset.",
	}, []string{"cluster", "kind", "field"})

	backupFieldObserved = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "armada_backup_field_observed",
		Help: "Confirmed value of a BackupConfig field from the controller's recordObserved ledger. Same encoding as intent.",
	}, []string{"cluster", "kind", "field"})

	// backupFieldIgnored is 1 when the parent ConfigBundle has an IgnoredEntry
	// for this {cluster, field}; absent otherwise. Mirror of the serverconfig
	// idracFieldIgnored gauge. Cluster-level ignore semantics are not yet wired
	// in spec.ignored[] — when divergence reporting for backup fields ships,
	// this gauge will be populated by ignoredFieldsForCluster below.
	backupFieldIgnored = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "armada_backup_field_ignored",
		Help: "1 when the parent ConfigBundle's spec.ignored[] lists this {cluster, kind, field}; absent otherwise. Used by alert rules to suppress drift alerts on admin-overridden fields.",
	}, []string{"cluster", "kind", "field"})
)

func init() {
	metrics.Registry.MustRegister(backupFieldIntent, backupFieldObserved, backupFieldIgnored)
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
	recordBlockIntent(bc.Name, "velero", bc.Spec.Velero)
	recordBlockIntent(bc.Name, "etcd", bc.Spec.Etcd)
}

func recordBlockIntent(cluster, kind string, block *armadav1.BackupBlock) {
	enabledLabels := prometheus.Labels{"cluster": cluster, "kind": kind, "field": "enabled"}
	scheduleLabels := prometheus.Labels{"cluster": cluster, "kind": kind, "field": "schedule"}
	locationLabels := prometheus.Labels{"cluster": cluster, "kind": kind, "field": "location"}
	if block == nil {
		backupFieldIntent.Delete(enabledLabels)
		backupFieldIntent.Delete(scheduleLabels)
		backupFieldIntent.Delete(locationLabels)
		return
	}
	if block.Enabled != nil {
		backupFieldIntent.With(enabledLabels).Set(boolGauge(*block.Enabled))
	} else {
		backupFieldIntent.Delete(enabledLabels)
	}
	if block.Schedule != nil {
		backupFieldIntent.With(scheduleLabels).Set(1)
	} else {
		backupFieldIntent.Delete(scheduleLabels)
	}
	if block.Location != nil {
		backupFieldIntent.With(locationLabels).Set(1)
	} else {
		backupFieldIntent.Delete(locationLabels)
	}
}

// recordObservedMetric updates the observed gauge from the status ledger.
// Mirror of recordIntent but reading from status.observed.{velero,etcd}.
func recordObservedMetric(bc *armadav1.BackupConfig) {
	recordBlockObserved(bc.Name, "velero", bc.Status.Observed.Velero)
	recordBlockObserved(bc.Name, "etcd", bc.Status.Observed.Etcd)
}

func recordBlockObserved(cluster, kind string, block armadav1.ObservedBackupBlock) {
	enabledLabels := prometheus.Labels{"cluster": cluster, "kind": kind, "field": "enabled"}
	scheduleLabels := prometheus.Labels{"cluster": cluster, "kind": kind, "field": "schedule"}
	locationLabels := prometheus.Labels{"cluster": cluster, "kind": kind, "field": "location"}
	if block.Enabled != nil {
		backupFieldObserved.With(enabledLabels).Set(boolGauge(*block.Enabled))
	} else {
		backupFieldObserved.Delete(enabledLabels)
	}
	if block.Schedule != nil {
		backupFieldObserved.With(scheduleLabels).Set(1)
	} else {
		backupFieldObserved.Delete(scheduleLabels)
	}
	if block.Location != nil {
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
