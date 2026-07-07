package backupconfig

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
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

// testLogger returns a no-op logr.Logger for tests that call methods
// requiring a logger but don't care about the output.
func testLogger() logr.Logger { return logr.Discard() }

// makeEtcdPodSpec builds a minimal PodTemplateSpec matching the shape
// buildEtcdCronJob writes: one container named etcdBackupContainerName with
// the location wired through BACKUP_LOCATION env. Used by observed-status
// tests that need a live CronJob to read. The container name and env-var
// name match what buildEtcdCronJob produces so observed / delta reads on the
// fake client find the fields they expect.
func makeEtcdPodSpec(container string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  etcdSnapshotWriterContainerName,
				Image: testUploadImage,
				Env: []corev1.EnvVar{
					{Name: "STORAGE_ACCOUNT", Value: testStorageAccount},
					{Name: "STORAGE_CONTAINER", Value: container},
				},
			}},
		},
	}
}

const (
	testVeleroNs       = "velero"
	testEtcdNs         = "kube-system"
	testEtcdctlImage   = "test/etcdctl:test"
	testUploadImage    = "test/azure-cli:test"
	testCredSecret     = "test-az-creds"
	testStorageAccount = "teststorage"
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
		EtcdctlImage:        testEtcdctlImage,
		UploadImage:         testUploadImage,
		CredentialSecret:    testCredSecret,
	}
	return r, c
}

func sampleBackupConfig() *armadav1.BackupConfig {
	return &armadav1.BackupConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "colo-cluster-001"},
		Spec: armadav1.BackupConfigSpec{
			OrbID:        "colo:cluster-001-backup",
			ClusterOrbID: "colo:cluster-001",
			Velero: &armadav1.VeleroBackupSpec{
				OrbID:    "colo:cluster-001-velero",
				Enabled:  ptr.To(true),
				Schedule: ptr.To("0 2 * * *"),
				Location: ptr.To("default"),
			},
			Etcd: &armadav1.EtcdBackupSpec{
				OrbID:    "colo:cluster-001-etcd",
				Enabled:  ptr.To(true),
				Schedule: ptr.To("0 3 * * *"),
				Location: ptr.To("https://teststorage.blob.core.windows.net/etcd-backups/colo/cluster-001"),
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
	podSpec := cj.Spec.JobTemplate.Spec.Template.Spec
	if len(podSpec.InitContainers) != 1 || podSpec.InitContainers[0].Image != testEtcdctlImage {
		t.Errorf("etcd initContainer (snapshot-taker): expected image %q, got %+v", testEtcdctlImage, podSpec.InitContainers)
	}
	if len(podSpec.Containers) != 1 || podSpec.Containers[0].Image != testUploadImage {
		t.Errorf("etcd main container (snapshot-writer): expected image %q, got %+v", testUploadImage, podSpec.Containers)
	}
	if envValue(podSpec.Containers[0].Env, "STORAGE_ACCOUNT") != "teststorage" {
		t.Errorf("etcd STORAGE_ACCOUNT: got %q", envValue(podSpec.Containers[0].Env, "STORAGE_ACCOUNT"))
	}
	if envValue(podSpec.Containers[0].Env, "STORAGE_CONTAINER") != "etcd-backups" {
		t.Errorf("etcd STORAGE_CONTAINER: got %q", envValue(podSpec.Containers[0].Env, "STORAGE_CONTAINER"))
	}
	if envValue(podSpec.Containers[0].Env, "BLOB_PREFIX") != "colo/cluster-001" {
		t.Errorf("etcd BLOB_PREFIX: got %q", envValue(podSpec.Containers[0].Env, "BLOB_PREFIX"))
	}
	if !podSpec.HostNetwork {
		t.Errorf("etcd CronJob should use hostNetwork (to reach local etcd)")
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
			Velero: &armadav1.VeleroBackupSpec{
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
	block := &armadav1.VeleroBackupSpec{
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
	block := &armadav1.EtcdBackupSpec{
		Schedule: ptr.To("0 3 * * *"),
		Location: ptr.To("https://teststorage.blob.core.windows.net/etcd-backups/colo/cluster-001"),
		Enabled:  ptr.To(true),
	}
	params := etcdCronJobParams{
		StorageAccount:   "teststorage",
		StorageContainer: "etcd-backups",
		BlobPrefix:       "colo/cluster-001",
	}
	d, err := etcdDeltas(context.Background(), c, r.EtcdBackupNamespace, "missing-etcd", block, params)
	if err != nil {
		t.Fatalf("etcdDeltas: %v", err)
	}
	if d["schedule"] != "0 3 * * *" || d["storageContainer"] != "etcd-backups" || d["storageAccount"] != "teststorage" || d["blobPrefix"] != "colo/cluster-001" || d["suspend"] != "false" {
		t.Errorf("expected all-fields delta when missing, got %+v", d)
	}
}

// TestReadLiveObserved covers the honest-observer contract: observed reflects
// what actually exists on the cluster, not what was intended. Every case
// pins one of the shapes readLiveObserved must produce.
func TestReadLiveObserved_LivesResourcesPresent(t *testing.T) {
	// Both a Velero Schedule and etcd CronJob exist. Observed should read
	// them and populate all four fields per mechanism from live state.
	sched := &unstructured.Unstructured{}
	sched.SetGroupVersionKind(veleroScheduleGVK)
	sched.SetNamespace(testVeleroNs)
	sched.SetName("colo-cluster-001-velero")
	_ = unstructured.SetNestedField(sched.Object, "0 2 * * *", "spec", "schedule")
	_ = unstructured.SetNestedField(sched.Object, "default", "spec", "template", "storageLocation")
	_ = unstructured.SetNestedField(sched.Object, false, "spec", "paused")

	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "colo-cluster-001-etcd-backup", Namespace: testEtcdNs},
		Spec: batchv1.CronJobSpec{
			Schedule: "0 3 * * *",
			Suspend:  ptr.To(false),
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{Template: makeEtcdPodSpec("s3://backups/etcd")},
			},
		},
	}
	r, _ := newReconciler(t, sched, cj)
	bc := sampleBackupConfig()

	got := r.readLiveObserved(context.Background(), bc, testLogger())

	if got.Velero == nil {
		t.Fatal("velero: expected non-nil for present live Schedule")
	}
	if !boolPtrEqual(got.Velero.Enabled, ptr.To(true)) {
		t.Errorf("velero.enabled: got %v want true (live paused=false)", got.Velero.Enabled)
	}
	if !stringPtrEqual(got.Velero.Schedule, ptr.To("0 2 * * *")) {
		t.Errorf("velero.schedule: got %v", got.Velero.Schedule)
	}
	if !stringPtrEqual(got.Velero.Location, ptr.To("default")) {
		t.Errorf("velero.location: got %v", got.Velero.Location)
	}
	if got.Etcd == nil {
		t.Fatal("etcd: expected non-nil for present live CronJob")
	}
	if !boolPtrEqual(got.Etcd.Enabled, ptr.To(true)) {
		t.Errorf("etcd.enabled: got %v want true (live suspend=false)", got.Etcd.Enabled)
	}
	if !stringPtrEqual(got.Etcd.Schedule, ptr.To("0 3 * * *")) {
		t.Errorf("etcd.schedule: got %v", got.Etcd.Schedule)
	}
	if !stringPtrEqual(got.Etcd.Location, ptr.To("s3://backups/etcd")) {
		t.Errorf("etcd.location: got %v", got.Etcd.Location)
	}
}

func TestReadLiveObserved_ResourcesAbsent(t *testing.T) {
	// Spec asks for velero + etcd but neither live resource exists. Observed
	// reports both as nil — the "honest" answer instead of copying spec.
	r, _ := newReconciler(t)
	bc := sampleBackupConfig()

	got := r.readLiveObserved(context.Background(), bc, testLogger())

	if got.Velero != nil {
		t.Errorf("velero: expected nil when live Schedule absent, got %+v", got.Velero)
	}
	if got.Etcd != nil {
		t.Errorf("etcd: expected nil when live CronJob absent, got %+v", got.Etcd)
	}
}

func TestReadLiveObserved_LiveDriftedFromSpec(t *testing.T) {
	// Live Schedule has paused=true even though spec says enabled=true.
	// Observed must report the LIVE value (enabled=false), surfacing the
	// drift in status. This is the core payoff of live-read observed.
	sched := &unstructured.Unstructured{}
	sched.SetGroupVersionKind(veleroScheduleGVK)
	sched.SetNamespace(testVeleroNs)
	sched.SetName("colo-cluster-001-velero")
	_ = unstructured.SetNestedField(sched.Object, "0 2 * * *", "spec", "schedule")
	_ = unstructured.SetNestedField(sched.Object, true, "spec", "paused") // ← drifted
	r, _ := newReconciler(t, sched)
	bc := sampleBackupConfig()

	got := r.readLiveObserved(context.Background(), bc, testLogger())

	if got.Velero == nil {
		t.Fatal("velero: expected non-nil (live Schedule present)")
	}
	if !boolPtrEqual(got.Velero.Enabled, ptr.To(false)) {
		t.Errorf("velero.enabled: expected false (live paused=true drifted from spec enabled=true), got %v", got.Velero.Enabled)
	}
}

func TestReadLiveObserved_UnmanagedMechanismStayNil(t *testing.T) {
	// Spec only manages Velero. Live CronJob happens to exist under our
	// deterministic name (stray from a prior deploy). readLiveObserved must
	// still report etcd as nil — we don't observe mechanisms the CR does
	// not ask us to manage.
	strayCj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "colo-cluster-001-etcd-backup", Namespace: testEtcdNs},
		Spec:       batchv1.CronJobSpec{Schedule: "0 3 * * *"},
	}
	r, _ := newReconciler(t, strayCj)
	bc := &armadav1.BackupConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "colo-cluster-001"},
		Spec: armadav1.BackupConfigSpec{
			OrbID:  "colo:cluster-001",
			Velero: &armadav1.VeleroBackupSpec{OrbID: "colo:cluster-001-velero"},
			// Etcd intentionally nil — spec does not manage etcd.
		},
	}

	got := r.readLiveObserved(context.Background(), bc, testLogger())

	if got.Etcd != nil {
		t.Errorf("etcd: expected nil for unmanaged mechanism, got %+v", got.Etcd)
	}
}

func TestObservedEqual_DetectsAnyFieldChange(t *testing.T) {
	a := armadav1.ObservedBackup{
		Velero: &armadav1.ObservedVeleroStatus{Enabled: ptr.To(true), Schedule: ptr.To("a")},
	}
	b := armadav1.ObservedBackup{
		Velero: &armadav1.ObservedVeleroStatus{Enabled: ptr.To(true), Schedule: ptr.To("b")},
	}
	if observedEqual(a, b) {
		t.Errorf("expected schedule diff to surface as not-equal")
	}
	if !observedEqual(a, a) {
		t.Errorf("expected identical observed to be equal")
	}
}
