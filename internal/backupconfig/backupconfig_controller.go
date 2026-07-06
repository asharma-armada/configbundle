package backupconfig

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	armadav1 "github.com/armada/configbundle/api/v1"
)

// ConditionReconciled is the canonical condition type written by this
// controller. Type=True ⇒ live state matches intent (after PATCH, or already
// matching on read). Type=False ⇒ a step failed; Reason names which.
const ConditionReconciled = "Reconciled"

// fieldManager is the SSA field-owner string this controller uses when writing
// Velero Schedule CRDs and the etcd-backup CronJob. Matches the convention
// used by the sibling serverconfig-controller — single fixed owner per
// controller.
const fieldManager = "controller"

// recentPatchesLimit caps how many PATCH-history entries we keep in
// status.recentPatches. Mirrors the serverconfig-controller constant.
const recentPatchesLimit = 5

// BackupConfigReconciler watches BackupConfig CRs and reconciles spec.velero
// against a Velero Schedule CRD and spec.etcd against an etcd-backup CronJob,
// both in the same cluster as the controller.
type BackupConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// VeleroNamespace is where Velero Schedule CRDs are written
	// (conventionally "velero").
	VeleroNamespace string

	// EtcdBackupNamespace is where the etcd-backup CronJob is written
	// (conventionally "kube-system").
	EtcdBackupNamespace string

	// EtcdBackupImage is the container image the etcd-backup CronJob runs.
	// Placeholder until a dedicated snapshot image ships.
	EtcdBackupImage string

	// ObserveInterval is the cadence at which the reconciler re-polls Velero +
	// CronJob state even when nothing on the CR has changed. Drives drift
	// detection. Zero = no periodic poll (event-driven only).
	ObserveInterval time.Duration
}

func (r *BackupConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// GenerationChangedPredicate fires reconcile on Create + spec changes only.
	// Status updates and managedFields-only changes don't bump generation, so
	// they don't re-fire — keeps the log clean.
	return ctrl.NewControllerManagedBy(mgr).
		For(&armadav1.BackupConfig{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("backupconfig").
		Complete(r)
}

// +kubebuilder:rbac:groups=armada.ai,resources=backupconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=armada.ai,resources=backupconfigs/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=armada.ai,resources=configbundles,verbs=get;list;watch
// +kubebuilder:rbac:groups=velero.io,resources=schedules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;create;update;patch;delete

func (r *BackupConfigReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithName("backupconfig")

	var bc armadav1.BackupConfig
	if err := r.Get(ctx, req.NamespacedName, &bc); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("backupconfig deleted", "name", req.Name)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	// No reconcilable blocks — log and skip without status write.
	if bc.Spec.Velero == nil && bc.Spec.Etcd == nil {
		logger.Info("no velero or etcd block on backupconfig; skipping",
			"name", bc.Name, "orbId", bc.Spec.OrbID)
		recordIntent(&bc)
		return reconcile.Result{}, nil
	}

	// Refresh metrics before the reconcile so they stay in sync with the CR
	// even if a downstream PATCH fails. Ignored stays a no-op until cluster-scoped
	// IgnoredEntry support lands.
	recordIntent(&bc)
	recordIgnored(bc.Name, ignoredFieldsForCluster(ctx, r.Client, &bc, logger))

	patchMessages := []string{}

	if bc.Spec.Velero != nil {
		msg, err := r.reconcileVelero(ctx, &bc)
		if err != nil {
			logger.Error(err, "reconcile Velero Schedule", "name", bc.Name)
			r.setStatusFailed(ctx, &bc, "VeleroPatchFailed", err.Error())
			return reconcile.Result{}, fmt.Errorf("reconcile velero: %w", err)
		}
		if msg != "" {
			patchMessages = append(patchMessages, msg)
		}
	}

	if bc.Spec.Etcd != nil {
		msg, err := r.reconcileEtcd(ctx, &bc)
		if err != nil {
			logger.Error(err, "reconcile etcd CronJob", "name", bc.Name)
			r.setStatusFailed(ctx, &bc, "EtcdPatchFailed", err.Error())
			return reconcile.Result{}, fmt.Errorf("reconcile etcd: %w", err)
		}
		if msg != "" {
			patchMessages = append(patchMessages, msg)
		}
	}

	r.recordObserved(ctx, &bc)
	recordObservedMetric(&bc)

	if len(patchMessages) == 0 {
		logger.Info("reconciled (no PATCH needed)", "name", bc.Name)
		// Don't overwrite a prior "PATCHed ..." status message on every periodic
		// poll — that erases useful action history. Only refresh status here if
		// we're recovering from a non-Reconciled state.
		if !isCurrentlyReconciled(&bc) {
			r.setStatusApplied(ctx, &bc, "all backup settings already match intent")
		} else {
			r.bumpObservedGeneration(ctx, &bc)
		}
		return reconcile.Result{RequeueAfter: r.ObserveInterval}, nil
	}

	patchMsg := strings.Join(patchMessages, "; ")
	logger.Info("reconciled (PATCH applied)", "name", bc.Name, "actions", patchMsg)
	r.setStatusApplied(ctx, &bc, patchMsg)
	r.appendRecentPatch(ctx, &bc, patchMsg)
	return reconcile.Result{RequeueAfter: r.ObserveInterval}, nil
}

// recordObserved updates status.observed.{velero,etcd} from the spec intent
// for blocks the controller successfully patched (or confirmed already match
// on a no-op). Mirror of serverconfig-controller.recordObserved — trusts the
// PATCH 2xx as confirmation, no separate read-back.
//
// Read-modify-write with RetryOnConflict; skipped entirely when nothing would
// change so periodic polls in steady state are zero-cost.
func (r *BackupConfigReconciler) recordObserved(ctx context.Context, bc *armadav1.BackupConfig) {
	desired := buildObserved(bc.Spec)
	if observedEqual(bc.Status.Observed, desired) {
		return
	}
	logger := log.FromContext(ctx).WithName("backupconfig.status")
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh armadav1.BackupConfig
		if err := r.Get(ctx, client.ObjectKeyFromObject(bc), &fresh); err != nil {
			return err
		}
		d := buildObserved(fresh.Spec)
		if observedEqual(fresh.Status.Observed, d) {
			return nil
		}
		fresh.Status.Observed = d
		return r.Status().Update(ctx, &fresh)
	})
	if err != nil {
		logger.Info("observed status update failed (will retry next reconcile)", "name", bc.Name, "err", err.Error())
	}
}

// buildObserved snapshots the spec into the observed-ledger shape. Fields with
// no intent stay absent — observed only reflects what the controller manages.
func buildObserved(spec armadav1.BackupConfigSpec) armadav1.ObservedBackup {
	var out armadav1.ObservedBackup
	if spec.Velero != nil {
		out.Velero = armadav1.ObservedBackupBlock{
			Enabled:  spec.Velero.Enabled,
			Schedule: spec.Velero.Schedule,
			Location: spec.Velero.Location,
		}
	}
	if spec.Etcd != nil {
		out.Etcd = armadav1.ObservedBackupBlock{
			Enabled:  spec.Etcd.Enabled,
			Schedule: spec.Etcd.Schedule,
			Location: spec.Etcd.Location,
		}
	}
	return out
}

func observedEqual(a, b armadav1.ObservedBackup) bool {
	return observedBlockEqual(a.Velero, b.Velero) && observedBlockEqual(a.Etcd, b.Etcd)
}

func observedBlockEqual(a, b armadav1.ObservedBackupBlock) bool {
	return boolPtrEqual(a.Enabled, b.Enabled) &&
		stringPtrEqual(a.Schedule, b.Schedule) &&
		stringPtrEqual(a.Location, b.Location)
}

func boolPtrEqual(a, b *bool) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func stringPtrEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// setStatusApplied writes Phase=Applied + Reconciled=True. Best-effort.
func (r *BackupConfigReconciler) setStatusApplied(ctx context.Context, bc *armadav1.BackupConfig, msg string) {
	r.writeStatus(ctx, bc, armadav1.BackupConfigPhaseApplied, metav1.ConditionTrue, "BackupApplied", msg)
}

// setStatusFailed writes Phase=Diverged + Reconciled=False with a Reason that
// names which step failed (VeleroPatchFailed, EtcdPatchFailed).
func (r *BackupConfigReconciler) setStatusFailed(ctx context.Context, bc *armadav1.BackupConfig, reason, msg string) {
	r.writeStatus(ctx, bc, armadav1.BackupConfigPhaseDiverged, metav1.ConditionFalse, reason, msg)
}

func (r *BackupConfigReconciler) writeStatus(ctx context.Context, bc *armadav1.BackupConfig, phase armadav1.BackupConfigPhase, condStatus metav1.ConditionStatus, reason, msg string) {
	logger := log.FromContext(ctx).WithName("backupconfig.status")
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh armadav1.BackupConfig
		if err := r.Get(ctx, client.ObjectKeyFromObject(bc), &fresh); err != nil {
			return err
		}
		fresh.Status.Phase = phase
		fresh.Status.ObservedGeneration = fresh.Generation
		setCondition(&fresh.Status.Conditions, ConditionReconciled, condStatus, reason, msg)
		return r.Status().Update(ctx, &fresh)
	})
	if err != nil {
		logger.Info("status update failed (will retry next reconcile)", "name", bc.Name, "err", err.Error())
	}
}

// appendRecentPatch prepends a new PATCH-action entry to status.recentPatches
// and truncates the list to recentPatchesLimit. Called only on successful PATCH.
func (r *BackupConfigReconciler) appendRecentPatch(ctx context.Context, bc *armadav1.BackupConfig, message string) {
	entry := armadav1.RecentPatch{Time: metav1.Now(), Message: message}
	logger := log.FromContext(ctx).WithName("backupconfig.status")
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh armadav1.BackupConfig
		if err := r.Get(ctx, client.ObjectKeyFromObject(bc), &fresh); err != nil {
			return err
		}
		recent := make([]armadav1.RecentPatch, 0, recentPatchesLimit+1)
		recent = append(recent, entry)
		recent = append(recent, fresh.Status.RecentPatches...)
		if len(recent) > recentPatchesLimit {
			recent = recent[:recentPatchesLimit]
		}
		fresh.Status.RecentPatches = recent
		return r.Status().Update(ctx, &fresh)
	})
	if err != nil {
		logger.Info("recentPatches update failed (next PATCH will retry)", "name", bc.Name, "err", err.Error())
	}
}

func (r *BackupConfigReconciler) bumpObservedGeneration(ctx context.Context, bc *armadav1.BackupConfig) {
	if bc.Status.ObservedGeneration == bc.Generation {
		return
	}
	logger := log.FromContext(ctx).WithName("backupconfig.status")
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh armadav1.BackupConfig
		if err := r.Get(ctx, client.ObjectKeyFromObject(bc), &fresh); err != nil {
			return err
		}
		if fresh.Status.ObservedGeneration == fresh.Generation {
			return nil
		}
		fresh.Status.ObservedGeneration = fresh.Generation
		return r.Status().Update(ctx, &fresh)
	})
	if err != nil {
		logger.Info("observedGeneration update failed", "name", bc.Name, "err", err.Error())
	}
}

func isCurrentlyReconciled(bc *armadav1.BackupConfig) bool {
	for _, c := range bc.Status.Conditions {
		if c.Type == ConditionReconciled {
			return c.Status == metav1.ConditionTrue
		}
	}
	return false
}

func setCondition(conds *[]metav1.Condition, condType string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	for i := range *conds {
		c := &(*conds)[i]
		if c.Type != condType {
			continue
		}
		if c.Status != status {
			c.LastTransitionTime = now
		}
		c.Status = status
		c.Reason = reason
		c.Message = message
		return
	}
	*conds = append(*conds, metav1.Condition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            message,
	})
}

// formatBlockDeltas renders changed-field map as a stable, human-readable string
// for status messages: "schedule=0 2 * * *, location=s3://backups".
func formatBlockDeltas(prefix string, d map[string]string) string {
	if len(d) == 0 {
		return ""
	}
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, d[k]))
	}
	return fmt.Sprintf("%s: %s", prefix, strings.Join(parts, ", "))
}
