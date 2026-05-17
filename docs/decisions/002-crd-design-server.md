# CRD Design: ConfigBundle and ServerConfig

**Status:** In progress — iDRAC domain settled; clusters and other domains pending  
**Last updated:** 2026-05-26

---

## Context

The ConfigBundle CRD is the central artifact of this system. The ConfigBundle Controller decomposes it into domain child CRs via Server-Side Apply. This document captures the settled schema decisions for the ConfigBundle parent CR and the ServerConfig child CR, starting with the iDRAC domain.

The SDD §4.6 example was used as inspiration, not specification. The design here supersedes it.

---

## Settled Decisions

### Identity: `serviceTag`, not `hostname` or `name`

Server entries are keyed by `serviceTag` (Dell hardware service tag, e.g. `3RK3V64`). It is:
- Immutable for the life of the hardware
- Globally unique (Dell)
- Alphanumeric only — RFC 1123 safe when lowercased

`serviceTag` is the stable identity key for server entries in the ConfigBundle spec and is propagated into the ServerConfig child CR's `spec.serviceTag` field. It is **not** used as the K8s resource name.

The K8s resource name for the ServerConfig child CR is `strings.ToLower(hostname)`. This follows the industry convention established by Tinkerbell and Cluster API (e.g. `bmc-g1-cp5-09`) where the machine's hostname — not a hardware serial — is the resource identifier. Hostnames are human-readable, meaningful in context, and stable for the life of a server's role in the datacenter.

`hostname` is mandatory. Servers missing `hostname` are skipped (or errored) by the bundler at build time. The bundler must validate that the lowercased hostname is a valid RFC 1123 DNS label before including the server.

### `oobIP` is required for actuation

The ServerConfig controller issues Redfish calls to the iDRAC. Without the out-of-band management IP, the controller has no target. `oobIP` is sourced from `Server.oobIP.address` in the Orbital CMDB and must be present in the ConfigBundle spec and propagated to the ServerConfig child CR.

### iDRAC fields: all 8 from Orbital `IdracSettings` are desired-state spec fields

Orbital's `IdracSettings` type has 8 fields. All are desired-state config (the CMDB holds design intent, not observed hardware state). All 8 are included:

| Field | Redfish target |
|---|---|
| `firmwareVersion` | `UpdateService.SimpleUpdate` (upgrade/downgrade to match) |
| `sshEnabled` | `PATCH /Managers/iDRAC.Embedded.1` → `SSH.ProtocolEnabled` |
| `ipmiEnabled` | same → `IPMI.ProtocolEnabled` |
| `lockdownModeEnabled` | Attributes → `SysInfo.1.SystemLockDown` |
| `osToIdracPassThroughEnabled` | Attributes → `OS-BMC.1.AdminState` |
| `usbManagementPortEnabled` | Attributes → `USB.1.ManagementPortStatus` |
| `dhcpEnabled` | `EthernetInterfaces/NIC.1` → `DHCPv4.DHCPEnabled` |
| `racadmEnabled` | Attributes → `Racadm.1.Enable` |

**On `firmwareVersion`:** Orbital data is initially bootstrapped from Redfish scans, then maintained as authoritative desired configuration. `firmwareVersion` in Orbital means "we intend this version" — not a snapshot of current hardware state. The ServerConfig controller reads current firmware via Redfish GET and upgrades/downgrades to match spec. Downgrade policy is a controller design decision (TBD).

### `orbId` excluded

Orbital's internal CMDB key (`<namespace>:<serviceTag>`) is not included in spec. Once a ConfigBundle lands on a Galleon, Orbital is out of the picture (Key Decision 5, Invariant 3). Divergence reports can reconstruct the Orbital reference from `<datacenter>:<serviceTag>` if needed.

### Redfish port: assume 443

No explicit port field. All Redfish calls target HTTPS/443.

### Credentials: deferred

The ServerConfig controller requires iDRAC credentials to authenticate Redfish calls. Decision deferred. Interim: manually provisioned K8s Secret in the controller's namespace. Full credential model (per-server vs datacenter-wide, rotation strategy) must be designed before the ServerConfig controller ships. Tracked in ROADMAP prerequisites.

---

## ConfigBundle CR Spec — server section

```yaml
apiVersion: armada.ai/v1
kind: ConfigBundle
metadata:
  name: colo-galleon
  namespace: configbundle-system
spec:
  datacenter: colo
  servers:
    - serviceTag: 3RK3V64
      hostname: colo-r740-01
      oobIP: 10.10.1.45
      idrac:
        firmwareVersion: "7.20.10.05"
        sshEnabled: false
        ipmiEnabled: false
        lockdownModeEnabled: false
        osToIdracPassThroughEnabled: false
        usbManagementPortEnabled: true
        dhcpEnabled: false
        racadmEnabled: true
    - serviceTag: FQK3V64
      hostname: colo-r740-02
      oobIP: 10.10.1.46
      idrac:
        firmwareVersion: "7.20.10.05"
        sshEnabled: false
        ipmiEnabled: false
        lockdownModeEnabled: false
        osToIdracPassThroughEnabled: false
        usbManagementPortEnabled: true
        dhcpEnabled: false
        racadmEnabled: true
```

---

## ServerConfig Child CR

Created and updated by the ConfigBundle Controller via SSA (field manager: `configbundle-controller`). Named by `strings.ToLower(serviceTag)`. Owned by the parent ConfigBundle via `ownerReferences` — deletion cascades automatically.

```yaml
apiVersion: armada.ai/v1
kind: ServerConfig
metadata:
  name: colo-r740-01
  namespace: configbundle-system
  ownerReferences:
    - apiVersion: armada.ai/v1
      kind: ConfigBundle
      name: colo-galleon
      controller: true
      blockOwnerDeletion: true
spec:
  serviceTag: 3RK3V64
  hostname: colo-r740-01
  oobIP: 10.10.1.45
  idrac:
    firmwareVersion: "7.20.10.05"
    sshEnabled: false
    ipmiEnabled: false
    lockdownModeEnabled: false
    osToIdracPassThroughEnabled: false
    usbManagementPortEnabled: true
    dhcpEnabled: false
    racadmEnabled: true
```

### Why `serviceTag` is repeated in spec

The CR name is the lowercased hostname (e.g. `colo-r740-01`). The ServerConfig controller needs the original-case service tag for Redfish targeting and credential lookup. There is no reliable way to reconstruct the service tag from the CR name, so it is explicit in spec.

---

## Bundler behavior

When building the ConfigBundle manifest from Orbital GraphQL:

- Query: `Server.serviceTag`, `Server.hostname`, `Server.oobIP.address`, `Server.idracSettings.*`
- If `hostname` is null or empty: skip the server and log a warning (do not fail the entire bundle)
- If `oobIP` is null: skip the server and log a warning
- `firmwareVersion` null: include the server but omit the `idrac.firmwareVersion` field (controller skips firmware reconciliation for that server)

---

## Scaffolding with kubebuilder

kubebuilder v4 is the tool for this. It scaffolds the project structure and runs `controller-gen` via `make` targets. `controller-runtime` is the underlying library that runs the controllers.

### Why kubebuilder and not alternatives

- **Operator SDK** — built on top of kubebuilder. Only adds value for OLM/OperatorHub publishing. Not needed here.
- **controller-runtime directly** — valid, but kubebuilder generates the Makefile, kustomize config, and CRD YAML pipeline. No reason to hand-roll that.

### Multi-binary structure

kubebuilder assumes one operator binary. This repo has two (`bundler` + `controller`). The CRD types in `api/v1/` are shared. After `kubebuilder init`, the generated `cmd/main.go` is moved/split manually into `cmd/bundler/` and `cmd/controller/`. The Makefile build targets are adjusted accordingly.

### Scaffolding sequence (Spike 1 + Spike 4)

```bash
# Spike 1 — project scaffold
kubebuilder init --domain armada.ai --repo github.com/armada/configbundle

# Spike 4 — CRD types only (no controller stubs yet)
kubebuilder create api --version v1 --kind ConfigBundle --resource --controller=false
kubebuilder create api --version v1 --kind ServerConfig --resource --controller=false

# Edit api/v1/configbundle_types.go and api/v1/serverconfig_types.go
# to match the spec defined in this document

make generate   # controller-gen → zz_generated.deepcopy.go
make manifests  # controller-gen → config/crd/bases/*.yaml
```

`--controller=false` keeps this library-first. Controller stubs are added in Spike 5 (`kubebuilder create api --controller --resource=false`).

### Key make targets

| Target | What it does |
|---|---|
| `make generate` | Runs controller-gen to produce `zz_generated.deepcopy.go` |
| `make manifests` | Runs controller-gen to produce CRD YAML in `config/crd/bases/` |
| `make install` | Applies CRD YAML to the current kubeconfig cluster |
| `make run` | Runs the controller locally against current kubeconfig (Spike 5+) |

Never hand-edit `zz_generated.deepcopy.go` — it is overwritten by `make generate`.

---

## Open / Pending

| Item | Status |
|---|---|
| `spec.datacenter` — explicit field or redundant with CR name? | Open |
| `clusters` domain — EksaConfig schema, child CR design | Not started |
| `client.ForceOwnership` in reconciler | **Correct as implemented.** Local overrides are only possible at the ConfigBundle CR level (via the Puller's no-ForceOwnership apply). Child CRs (ServerConfig etc.) are derived state, not an override surface. The Decomposition Reconciler must always use ForceOwnership to ensure child CRs faithfully reflect the ConfigBundle CR. No change needed. |
| Downgrade policy for `firmwareVersion` | Deferred to controller design |
| iDRAC credentials model | Deferred — see ROADMAP prerequisites |
| Additional server domains (BIOS, storage) | Not started |
