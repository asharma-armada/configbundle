# Runbook: BackupConfig CR exists but no CronJob was created

**Symptom:** `kubectl get backupconfigs` shows the CR, but
`kubectl -n kube-system get cronjobs` doesn't show the expected
`<bc-name>-etcd-backup`.

## Quick diagnosis

Check the BackupConfig's status first:

```bash
kubectl get backupconfig <name> -o yaml
```

Look at `.status.conditions[?(@.type=="Reconciled")]`:

- `status: "True"`, `reason: BackupApplied` → controller thinks it succeeded.
  The CronJob should exist. Continue with "controller says success but no CronJob".
- `status: "False"`, `reason: EtcdPatchFailed` → controller tried and failed.
  See "PATCH failed".
- Condition absent entirely → controller never reconciled the CR. See
  "no reconcile ever happened".

## No reconcile ever happened

bc-controller isn't running, isn't watching, or the CR doesn't match a
selector.

**Verify bc-controller is running:**

```bash
kubectl -n configbundle-system get pods -l app.kubernetes.io/name=backupconfig-controller
```

Pod should be `Running`, ready `1/1`. If not — deploy status:

```bash
kubectl -n configbundle-system get deploy configbundle-backupconfig-controller
kubectl -n configbundle-system describe pod -l app.kubernetes.io/name=backupconfig-controller | tail -30
```

Typical failure modes:

- `ImagePullBackOff` — check the image tag in the Deployment; `az acr login`
  and confirm the image was actually pushed
- Pod is `Pending` — resource quotas, node selector mismatch, or the
  `configbundle-system` namespace is missing something
- `CrashLoopBackOff` — grab logs: `kubectl -n configbundle-system logs -l app.kubernetes.io/name=backupconfig-controller --previous`

**Verify bc-controller can see BackupConfig CRs:**

```bash
kubectl -n configbundle-system logs -l app.kubernetes.io/name=backupconfig-controller --tail=50
```

Look for `Starting workers` on startup and `reconciling` messages when
BackupConfig CRs exist. If startup logs show scheme registration errors,
the CRD version doesn't match what the binary was built against — check
`kubectl get crd backupconfigs.armada.ai -o yaml` schema vs the version
of bc-controller you deployed.

## Controller says success but no CronJob

The status says `BackupApplied` but the CronJob isn't there. Two paths:

**Path A: Wrong namespace.** bc-controller writes CronJobs to
`ETCD_BACKUP_NAMESPACE` (default `kube-system`). Verify the env var:

```bash
kubectl -n configbundle-system get deploy configbundle-backupconfig-controller \
  -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="ETCD_BACKUP_NAMESPACE")].value}'
```

Then look in that namespace:

```bash
kubectl -n <namespace> get cronjobs | grep etcd-backup
```

**Path B: Name is different than you expected.** bc-controller uses
`<bc-name>-etcd-backup`. The `<bc-name>` is derived from
`cluster.backup.orbId` (via `orbIDToK8sName`), e.g.
`colo:cluster-001-backup` → `colo-cluster-001-backup` → CronJob name
`colo-cluster-001-backup-etcd-backup`. Confirm:

```bash
kubectl -n kube-system get cronjobs -o name
```

**Path C: Status is stale.** Reconciler wrote status then something else
deleted the CronJob out-of-band. Force a reconcile:

```bash
kubectl -n configbundle-system rollout restart deploy/configbundle-backupconfig-controller
```

Next reconcile re-applies the CronJob.

## PATCH failed

Status shows `reason: EtcdPatchFailed` or `VeleroPatchFailed` with a
message. Common causes and fixes:

**"container not found" / RBAC error.**
bc-controller lacks permission to create CronJobs in `kube-system` (or
Velero Schedules in `velero`). Check the ClusterRole:

```bash
kubectl get clusterrole configbundle-backupconfig-manager-role -o yaml | grep -A3 cronjob
```

Should include `create`, `update`, `patch`, `delete`. If missing, the
Deployment kustomize is stale — `make manifests` and redeploy.

**"unsupported scheme".**
Reconcile failed at URL parse. Look at the error message for the exact
scheme. bc-controller expects
`https://<account>.blob.core.windows.net/<container>/<prefix>`. Fix the
orbital `location` field.

**"spec.etcd.location is required".**
BackupConfig CR was created without a location. Check the CR:

```bash
kubectl get backupconfig <name> -o yaml | grep -A5 etcd:
```

If `location` is missing, orbital's cb-bundler didn't map it — check
`internal/bundler/handler.go:mapClusterBackup`.

## No BackupConfig CR exists at all

You applied a ConfigBundle but no BackupConfig was decomposed.

**Check cb-controller.** BackupConfig is created by cb-controller when
it decomposes the parent ConfigBundle.

```bash
kubectl -n configbundle-system logs -l app.kubernetes.io/name=configbundle-controller --tail=30
```

Look for `reconciling ConfigBundle {"kubernetesClusters": N}`. If N is 0,
the ConfigBundle spec has no `kubernetesClusters` — either the sample
manifest omits them or cb-bundler on orbital-side didn't emit them.

**Verify the ConfigBundle spec has kubernetesClusters:**

```bash
kubectl get configbundle <name> -o jsonpath='{.spec.kubernetesClusters}' | jq
```

If empty, root cause is upstream — cb-bundler didn't map, or orbital
graph doesn't have `KubernetesCluster` nodes attached to the DataCenter
in question. See
[`docs/playbooks/add-crd-field.md`](../playbooks/add-crd-field.md#4-if-the-field-is-actuated-at-the-edge-wire-the-controller)
for the bundler mapping path.

## The CronJob exists but backups aren't landing in blob storage

That's not "not reconciling" — bc-controller succeeded but the Job pod
that runs on schedule is failing. Different runbook (TODO). For now:

```bash
# Trigger the CronJob immediately (skip schedule wait)
kubectl -n kube-system create job \
  --from=cronjob/<bc-name>-etcd-backup \
  etcd-manual-$(date +%s)

# Watch the pod
kubectl -n kube-system get pods -l job-name -w

# Logs on failure
POD=$(kubectl -n kube-system get pods -l job-name --sort-by=.metadata.creationTimestamp -o jsonpath='{.items[-1].metadata.name}')
kubectl -n kube-system logs $POD -c snapshot-taker    # etcdctl step
kubectl -n kube-system logs $POD -c snapshot-writer   # az storage upload step
```

Common failure modes:

- Snapshot-writer failing on `az login` → check `az-storage-creds` Secret
  has valid credentials
- Snapshot-writer failing on `az storage blob upload` → target container
  doesn't exist. Create it: `az storage container create --account-name <account> --name <container>`
- Snapshot-taker failing on `etcdctl snapshot save` → PKI cert mount broken.
  Only happens if node's `/etc/kubernetes/pki` is inaccessible or the
  container isn't running on a control-plane node
