# configbundle

A Go library and set of services that packages Orbital's datacenter export as a signed OCI artifact and delivers it to Galleon edge clusters.

## Problem

Galleon edge clusters need a consistent, verifiable snapshot of their intended configuration from the cloud CMDB (Orbital). Orbital produces the source of truth but has no delivery mechanism that works across disconnected or air-gapped edges.

configbundle solves this: it acts as an Orbital enricher, producing a signed OCI artifact that edge clusters pull and apply locally — without any cloud-initiated connection.

## Stack

- **Language:** Go (`github.com/armada/configbundle`)
- **Framework:** kubebuilder / controller-runtime
- **Key libraries:** `oras-project/oras-go`, `sigstore/cosign`, `k8s.io/client-go`
- **Registries:** ACR (cloud), Zot (edge mirror)
- **Deployment:** AKS (cloud components); Galleon Mgmt Cluster (edge agent, cb-controller)

## Getting started

```bash
# Install dependencies
brew install go kubebuilder
go mod download

# Start local Orbital stack (run in the orbital repo)
make up && make run-orbital

# Run bundler locally (listens on :8020)
go run ./cmd/bundler

# Regenerate CRD manifests after type changes
make generate && make manifests
```

For the full local end-to-end test flow, see [`configbundle-integration.md`](configbundle-integration.md#local-end-to-end-test-flow).

## Testing

### Unit / integration tests (envtest)

Runs the ConfigBundle controller against a real API server and etcd spun up in-process. No cluster required.

```bash
make test
```

Covers:
- ConfigBundle → ServerConfig decomposition (single server, multi-server)
- All iDRAC fields propagated correctly
- Desired state enforcement: out-of-band mutations on child CRs are restored
- ConfigBundle spec updates propagate to child CRs

### E2E tests — local (minikube)

Runs the ConfigBundle functional tests against a live controller. Requires minikube and the controller running locally.

**One-time setup:**

```bash
minikube start
kubectl config use-context minikube
make install        # installs CRDs into minikube
```

**In a separate terminal, keep the controller running:**

```bash
make run
```

**Run the e2e suite:**

```bash
make test-e2e-local
```

Covers (in addition to envtest):
- Cascade delete: deleting a ConfigBundle garbage-collects all child ServerConfig CRs via ownerReferences

### E2E tests — CI (Kind)

Builds the manager image, loads it into a Kind cluster, deploys the controller via kustomize, and runs the full suite. Requires Docker and Kind.

```bash
make test-e2e
```

## AI context

This project uses AI Context as Code. For architectural decisions, invariants, domain context, and working conventions, see [`CLAUDE.md`](CLAUDE.md).
