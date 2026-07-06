package backupconfig

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	armadav1 "github.com/armada/configbundle/api/v1"
)

// etcdBackupContainerName is the well-known container name in the CronJob we
// manage. The controller owns this single container; it does not try to write
// other containers a cluster admin may add for sidecars.
const etcdBackupContainerName = "etcd-snapshot"

// etcdBackupLocationEnv is the env var the placeholder etcd-snapshot image
// reads to determine where to write the snapshot. Will be honored by the real
// image once it ships; until then it's metadata only.
const etcdBackupLocationEnv = "BACKUP_LOCATION"

// etcdCronJobName builds the deterministic CronJob name for a BackupConfig.
// Convention: "<bc-name>-etcd-backup".
func etcdCronJobName(bc *armadav1.BackupConfig) string {
	return bc.Name + "-etcd-backup"
}

// reconcileEtcd applies the desired etcd-backup CronJob from bc.Spec.Etcd.
// Returns a human-readable summary of the PATCH (empty string = no PATCH needed)
// or an error if the apply failed.
//
// "Enabled = false" maps to spec.suspend = true on the CronJob — K8s' native
// pause toggle. The CronJob stays in place when disabled, so re-enabling is
// a one-field flip.
func (r *BackupConfigReconciler) reconcileEtcd(ctx context.Context, bc *armadav1.BackupConfig) (string, error) {
	logger := log.FromContext(ctx).WithName("backupconfig.etcd")
	block := bc.Spec.Etcd
	name := etcdCronJobName(bc)

	deltas, err := etcdDeltas(ctx, r.Client, r.EtcdBackupNamespace, name, block, r.EtcdBackupImage)
	if err != nil {
		return "", err
	}
	if len(deltas) == 0 {
		logger.V(1).Info("etcd cronjob already matches intent", "name", name)
		return "", nil
	}

	cj := buildEtcdCronJob(name, r.EtcdBackupNamespace, r.EtcdBackupImage, block)
	if err := r.Patch(ctx, cj, client.Apply,
		client.FieldOwner(fieldManager),
		client.ForceOwnership,
	); err != nil {
		return "", fmt.Errorf("ssa patch etcd cronjob %s/%s: %w", r.EtcdBackupNamespace, name, err)
	}

	return formatBlockDeltas(fmt.Sprintf("etcd/%s", name), deltas), nil
}

// buildEtcdCronJob constructs the desired CronJob from a BackupBlock. Only the
// fields the controller owns are set — the kubelet, K8s defaulter, and any
// cluster-admin-added sidecars get to write everything else without conflict.
//
// Image is the placeholder default until the real etcd-snapshot image ships.
// BACKUP_LOCATION env carries the location string; honored by the real image
// once it lands.
func buildEtcdCronJob(name, namespace, image string, block *armadav1.BackupBlock) *batchv1.CronJob {
	schedule := ""
	if block.Schedule != nil {
		schedule = *block.Schedule
	}

	container := corev1.Container{
		Name:  etcdBackupContainerName,
		Image: image,
	}
	if block.Location != nil {
		container.Env = []corev1.EnvVar{{Name: etcdBackupLocationEnv, Value: *block.Location}}
	}

	cj := &batchv1.CronJob{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "batch/v1",
			Kind:       "CronJob",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: batchv1.CronJobSpec{
			Schedule: schedule,
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyOnFailure,
							Containers:    []corev1.Container{container},
						},
					},
				},
			},
		},
	}
	if block.Enabled != nil {
		suspend := !*block.Enabled
		cj.Spec.Suspend = &suspend
	}
	return cj
}

// etcdDeltas returns the set of fields that differ between the live CronJob and
// the intent. NotFound means all intent fields are deltas (create-on-first-apply).
func etcdDeltas(ctx context.Context, c client.Client, namespace, name string, block *armadav1.BackupBlock, image string) (map[string]string, error) {
	out := map[string]string{}

	var live batchv1.CronJob
	err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &live)
	switch {
	case apierrors.IsNotFound(err):
		if block.Schedule != nil {
			out["schedule"] = *block.Schedule
		}
		if block.Location != nil {
			out["location"] = *block.Location
		}
		if block.Enabled != nil {
			out["suspend"] = fmt.Sprintf("%t", !*block.Enabled)
		}
		out["image"] = image
		return out, nil
	case err != nil:
		return nil, fmt.Errorf("get etcd cronjob: %w", err)
	}

	if block.Schedule != nil && live.Spec.Schedule != *block.Schedule {
		out["schedule"] = *block.Schedule
	}
	if block.Enabled != nil {
		desiredSuspend := !*block.Enabled
		liveSuspend := false
		if live.Spec.Suspend != nil {
			liveSuspend = *live.Spec.Suspend
		}
		if liveSuspend != desiredSuspend {
			out["suspend"] = fmt.Sprintf("%t", desiredSuspend)
		}
	}

	liveContainer := findContainer(live.Spec.JobTemplate.Spec.Template.Spec.Containers, etcdBackupContainerName)
	if liveContainer == nil {
		out["image"] = image
		if block.Location != nil {
			out["location"] = *block.Location
		}
		return out, nil
	}
	if liveContainer.Image != image {
		out["image"] = image
	}
	if block.Location != nil {
		liveLoc := envValue(liveContainer.Env, etcdBackupLocationEnv)
		if liveLoc != *block.Location {
			out["location"] = *block.Location
		}
	}
	return out, nil
}

func findContainer(cs []corev1.Container, name string) *corev1.Container {
	for i := range cs {
		if cs[i].Name == name {
			return &cs[i]
		}
	}
	return nil
}

func envValue(env []corev1.EnvVar, name string) string {
	for _, e := range env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}
