package backupconfig

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	armadav1 "github.com/armada/configbundle/api/v1"
)

const (
	testVeleroNs = "velero"
	testEtcdNs   = "kube-system"
	testImage    = "test/etcd-snapshot:test"
)

func newReconciler(t *testing.T, objs ...client.Object) (*BackupConfigReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	if err := armadav1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&armadav1.BackupConfig{}).
		Build()
	r := &BackupConfigReconciler{
		Client:              c,
		Scheme:              scheme,
		VeleroNamespace:     testVeleroNs,
		EtcdBackupNamespace: testEtcdNs,
		EtcdBackupImage:     testImage,
	}
	return r, c
}

func sampleBackupConfig() *armadav1.BackupConfig {
	return &armadav1.BackupConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "colo-cluster-001"},
		Spec: armadav1.BackupConfigSpec{
			OrbID: "colo:cluster-001",
			Velero: &armadav1.BackupBlock{
				OrbID:    "colo:cluster-001-velero",
				Enabled:  ptr.To(true),
				Schedule: ptr.To("0 2 * * *"),
				Location: ptr.To("default"),
			},
			Etcd: &armadav1.BackupBlock{
				OrbID:    "colo:cluster-001-etcd",
				Enabled:  ptr.To(true),
				Schedule: ptr.To("0 3 * * *"),
				Location: ptr.To("s3://backups/etcd"),
			},
		},
	}
}

func TestReconcile_CreatesVeleroScheduleAndEtcdCronJob(t *testing.T) {
	bc := sampleBackupConfig()
	r, c := newReconciler(t, bc)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: bc.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Velero Schedule created.
	sched := &unstructured.Unstructured{}
	sched.SetGroupVersionKind(veleroScheduleGVK)
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: testVeleroNs, Name: veleroScheduleName(bc),
	}, sched); err != nil {
		t.Fatalf("get velero schedule: %v", err)
	}
	if got, _, _ := unstructured.NestedString(sched.Object, "spec", "schedule"); got != "0 2 * * *" {
		t.Errorf("velero schedule: got %q", got)
	}
	if got, _, _ := unstructured.NestedString(sched.Object, "spec", "template", "storageLocation"); got != "default" {
		t.Errorf("velero storageLocation: got %q", got)
	}
	if paused, _, _ := unstructured.NestedBool(sched.Object, "spec", "paused"); paused {
		t.Errorf("velero paused: expected false (Enabled=true)")
	}

	// Etcd CronJob created.
	var cj batchv1.CronJob
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: testEtcdNs, Name: etcdCronJobName(bc),
	}, &cj); err != nil {
		t.Fatalf("get etcd cronjob: %v", err)
	}
	if cj.Spec.Schedule != "0 3 * * *" {
		t.Errorf("etcd schedule: got %q", cj.Spec.Schedule)
	}
	if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
		t.Errorf("etcd suspend: expected false (Enabled=true)")
	}
	containers := cj.Spec.JobTemplate.Spec.Template.Spec.Containers
	if len(containers) != 1 || containers[0].Image != testImage {
		t.Errorf("etcd container image: got %+v", containers)
	}
	if envValue(containers[0].Env, etcdBackupLocationEnv) != "s3://backups/etcd" {
		t.Errorf("etcd BACKUP_LOCATION: got %q", envValue(containers[0].Env, etcdBackupLocationEnv))
	}

	// Status reflects success.
	var got armadav1.BackupConfig
	if err := c.Get(context.Background(), types.NamespacedName{Name: bc.Name}, &got); err != nil {
		t.Fatalf("get bc: %v", err)
	}
	if got.Status.Phase != armadav1.BackupConfigPhaseApplied {
		t.Errorf("phase: got %q", got.Status.Phase)
	}
	if len(got.Status.RecentPatches) == 0 {
		t.Errorf("expected at least one RecentPatch entry")
	}
}

func TestReconcile_NoBlocksNoOp(t *testing.T) {
	bc := &armadav1.BackupConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "empty"},
		Spec:       armadav1.BackupConfigSpec{OrbID: "colo:cluster-empty"},
	}
	r, c := newReconciler(t, bc)

	res, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: bc.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("no-op reconcile should not requeue, got %v", res.RequeueAfter)
	}

	// No Schedule or CronJob created.
	sched := &unstructured.Unstructured{}
	sched.SetGroupVersionKind(veleroScheduleGVK)
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: testVeleroNs, Name: veleroScheduleName(bc),
	}, sched); err == nil {
		t.Errorf("velero schedule should not exist for empty backupconfig")
	}
}

func TestReconcile_PausedWhenEnabledFalse(t *testing.T) {
	bc := &armadav1.BackupConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "paused"},
		Spec: armadav1.BackupConfigSpec{
			OrbID: "colo:cluster-paused",
			Velero: &armadav1.BackupBlock{
				OrbID:    "colo:cluster-paused-velero",
				Enabled:  ptr.To(false),
				Schedule: ptr.To("0 4 * * *"),
			},
		},
	}
	r, c := newReconciler(t, bc)
	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: bc.Name},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	sched := &unstructured.Unstructured{}
	sched.SetGroupVersionKind(veleroScheduleGVK)
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: testVeleroNs, Name: veleroScheduleName(bc),
	}, sched); err != nil {
		t.Fatalf("get velero schedule: %v", err)
	}
	paused, _, _ := unstructured.NestedBool(sched.Object, "spec", "paused")
	if !paused {
		t.Errorf("expected paused=true when Enabled=false")
	}
}

func TestReconcile_Idempotent(t *testing.T) {
	bc := sampleBackupConfig()
	r, c := newReconciler(t, bc)

	for i := 0; i < 3; i++ {
		if _, err := r.Reconcile(context.Background(), reconcile.Request{
			NamespacedName: types.NamespacedName{Name: bc.Name},
		}); err != nil {
			t.Fatalf("Reconcile iter %d: %v", i, err)
		}
	}

	var got armadav1.BackupConfig
	if err := c.Get(context.Background(), types.NamespacedName{Name: bc.Name}, &got); err != nil {
		t.Fatalf("get bc: %v", err)
	}
	// RecentPatches grew only on the first reconcile (deltas existed). Subsequent
	// reconciles found no deltas and skipped the PATCH path → no new entries.
	if len(got.Status.RecentPatches) != 1 {
		t.Errorf("expected exactly one RecentPatch (first reconcile only), got %d", len(got.Status.RecentPatches))
	}
}

func TestVeleroDeltas_NotFound(t *testing.T) {
	r, c := newReconciler(t)
	block := &armadav1.BackupBlock{
		Schedule: ptr.To("0 2 * * *"),
		Location: ptr.To("default"),
		Enabled:  ptr.To(true),
	}
	d, err := veleroDeltas(context.Background(), c, r.VeleroNamespace, "missing-velero", block)
	if err != nil {
		t.Fatalf("veleroDeltas: %v", err)
	}
	if d["schedule"] != "0 2 * * *" || d["storageLocation"] != "default" || d["paused"] != "false" {
		t.Errorf("expected all-fields delta when missing, got %+v", d)
	}
}

func TestEtcdDeltas_NotFound(t *testing.T) {
	r, c := newReconciler(t)
	block := &armadav1.BackupBlock{
		Schedule: ptr.To("0 3 * * *"),
		Location: ptr.To("s3://backups/etcd"),
		Enabled:  ptr.To(true),
	}
	d, err := etcdDeltas(context.Background(), c, r.EtcdBackupNamespace, "missing-etcd", block, testImage)
	if err != nil {
		t.Fatalf("etcdDeltas: %v", err)
	}
	if d["schedule"] != "0 3 * * *" || d["location"] != "s3://backups/etcd" || d["suspend"] != "false" || d["image"] != testImage {
		t.Errorf("expected all-fields delta when missing, got %+v", d)
	}
}

func TestBuildObserved_PointersRoundTrip(t *testing.T) {
	spec := armadav1.BackupConfigSpec{
		Velero: &armadav1.BackupBlock{
			Enabled:  ptr.To(true),
			Schedule: ptr.To("0 2 * * *"),
		},
	}
	got := buildObserved(spec)
	if !boolPtrEqual(got.Velero.Enabled, ptr.To(true)) {
		t.Errorf("velero.enabled: got %v", got.Velero.Enabled)
	}
	if !stringPtrEqual(got.Velero.Schedule, ptr.To("0 2 * * *")) {
		t.Errorf("velero.schedule: got %v", got.Velero.Schedule)
	}
	if got.Etcd.Enabled != nil || got.Etcd.Schedule != nil || got.Etcd.Location != nil {
		t.Errorf("etcd block: expected all-nil for absent intent, got %+v", got.Etcd)
	}
}

func TestObservedEqual_DetectsAnyFieldChange(t *testing.T) {
	a := armadav1.ObservedBackup{
		Velero: armadav1.ObservedBackupBlock{Enabled: ptr.To(true), Schedule: ptr.To("a")},
	}
	b := armadav1.ObservedBackup{
		Velero: armadav1.ObservedBackupBlock{Enabled: ptr.To(true), Schedule: ptr.To("b")},
	}
	if observedEqual(a, b) {
		t.Errorf("expected schedule diff to surface as not-equal")
	}
	if !observedEqual(a, a) {
		t.Errorf("expected identical observed to be equal")
	}
}
