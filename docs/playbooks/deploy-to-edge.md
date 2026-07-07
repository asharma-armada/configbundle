# Edge deploy: cb-controller + serverconfig-controller

Both controllers run in `configbundle-system` on the Galleon edge cluster.

## Endpoints (after deploy)

| URL | Purpose |
|---|---|
| `http://configbundle-controller.configbundle-system:8095/dispatch` | orb POSTs OCI layer bodies here |
| `http://serverconfig-controller-metrics.configbundle-system:8093/metrics` | Prometheus scrape target |

## Prereqs

```bash
# Namespace (neither kustomize creates it)
kubectl create namespace configbundle-system

# iDRAC credentials Secret (NEVER commit the password)
kubectl create secret generic idrac-credentials \
  -n configbundle-system \
  --from-literal=username=root \
  --from-literal=password='<actual-password>'
```

Also required (assumed already in place):

- **orb** Service reachable as `orb:8010` from inside the namespace.
- **ACR pull secret** in `configbundle-system` (site-specific Deployment patch).

## Tune the allowlist (one-time, per site)

Edit `~/armada/serverconfig-controller/config/manager/manager.yaml` ConfigMap:

```yaml
oobIPs: "10.20.21.44,..."           # iDRAC IPs the controller may PATCH
fields: "sshEnabled,racadmEnabled,ipmiEnabled"
```

`oobIPs` is the blast-radius control — CRs targeting other IPs are silently
skipped.

## Set image versions

Each repo pins its image tag in `config/default/kustomization.yaml`:

```yaml
images:
- name: controller
  newName: armadaeksatest.azurecr.io/<repo>-controller
  newTag: vX.Y.Z
```

Bump `newTag` here before deploy.

## Deploy

```bash
# cb-controller first (ships the CRDs)
kubectl apply -k ~/armada/configbundle/config/default
kubectl apply -k ~/armada/serverconfig-controller/config/default
```

Idempotent — same command upgrades.

## Verify

```bash
kubectl -n configbundle-system get pods,svc
kubectl get crd | grep armada.ai

# Drift-detection log line
kubectl -n configbundle-system logs deploy/serverconfig-controller --tail=20 | grep drift

# Metrics scrape
kubectl -n configbundle-system port-forward svc/serverconfig-controller-metrics 8093 &
curl -s http://localhost:8093/metrics | grep armada_idrac_field
```

## Teardown

```bash
kubectl delete -k ~/armada/serverconfig-controller/config/default
kubectl delete -k ~/armada/configbundle/config/default
# Optional: kubectl delete namespace configbundle-system
```
