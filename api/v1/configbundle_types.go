/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// IdracSpec holds desired iDRAC configuration, sourced from Orbital IdracSettings.
// All fields are desired state — not observed state. The ServerConfig controller
// actuates these via Redfish PATCH calls to the OOB IP.
//
// All leaf fields are pointers with omitempty so SSA partial patches can omit
// admin-owned fields. See ADR-007.
type IdracSpec struct {
	// FirmwareVersion is the desired iDRAC firmware version (e.g. "7.20.10.05").
	// Controller reads current version via Redfish GET and upgrades/downgrades to match.
	// +optional
	FirmwareVersion *string `json:"firmwareVersion,omitempty"`

	// +optional
	SSHEnabled *bool `json:"sshEnabled,omitempty"`

	// +optional
	IPMIEnabled *bool `json:"ipmiEnabled,omitempty"`

	// +optional
	LockdownModeEnabled *bool `json:"lockdownModeEnabled,omitempty"`

	// +optional
	OsToIdracPassThroughEnabled *bool `json:"osToIdracPassThroughEnabled,omitempty"`

	// +optional
	UsbManagementPortEnabled *bool `json:"usbManagementPortEnabled,omitempty"`

	// +optional
	DHCPEnabled *bool `json:"dhcpEnabled,omitempty"`

	// +optional
	RacadmEnabled *bool `json:"racadmEnabled,omitempty"`
}

// ServerSpec describes one server's desired configuration within a ConfigBundle.
//
// Hostname and OobIP are pointers so SSA partial patches can omit admin-owned
// fields (see ADR-007). OrbID is the listMapKey — Orbital's immutable identifier
// for this server, stable across hardware swaps and renames.
// See docs/plans/server-identity-orbid.md.
type ServerSpec struct {
	// OrbID is the immutable Orbital identifier for this server (e.g. "colo:srv-001").
	// Used as the SSA listMapKey for spec.servers[]. Never changes for the same
	// physical server, even when serviceTag is re-stamped or hostname renamed.
	// +kubebuilder:validation:Required
	OrbID string `json:"orbId"`

	// ServiceTag is the Dell hardware service tag (e.g. "3RK3V64"). Mutable across
	// board swaps. Propagated to the child ServerConfig spec for operator visibility.
	// +kubebuilder:validation:Required
	ServiceTag string `json:"serviceTag"`

	// Hostname is the server's hostname. Mandatory — the bundler skips servers without one.
	// +kubebuilder:validation:Required
	Hostname *string `json:"hostname,omitempty"`

	// OobIP is the out-of-band management (iDRAC) IP address.
	// The ServerConfig controller sends Redfish calls here. Mandatory for actuation.
	// +kubebuilder:validation:Required
	OobIP *string `json:"oobIP,omitempty"`

	// Idrac holds desired iDRAC configuration. Idrac itself is a value type;
	// its leaf fields are pointers (see IdracSpec).
	// +optional
	Idrac IdracSpec `json:"idrac,omitempty"`
}

// TakeoverEntry represents a cloud admin's "force" resolution: reclaim ownership
// of a specific field from local:admin. The consume handler processes these by
// running a per-field SSA apply with ForceOwnership after the normal apply pass.
type TakeoverEntry struct {
	// ServerOrbID identifies which server entry the field belongs to (matches
	// ServerSpec.OrbID, the listMapKey for spec.servers[]).
	// +kubebuilder:validation:Required
	ServerOrbID string `json:"serverOrbId"`

	// OrbID is the Orbital ConfigItem identifier of the node that owns the field
	// (e.g. "colo:srv-001-idrac" for an iDRAC field). Informational — used for
	// audit and divergence-resolution correlation.
	OrbID string `json:"orbId"`

	// Field is the leaf field name to reclaim (e.g. "sshEnabled").
	// Must match the JSON tag name on IdracSpec (or ServerSpec for top-level fields).
	// +kubebuilder:validation:Required
	Field string `json:"field"`
}

// ConfigBundleSpec holds the full intended configuration for a datacenter.
// The ConfigBundle controller decomposes this into domain child CRs via SSA.
type ConfigBundleSpec struct {
	// OrbID is the immutable Orbital identifier for this datacenter (e.g. "colo:colo-galleon").
	// First-class cross-system correlation key; see docs/plans/server-identity-orbid.md.
	// +kubebuilder:validation:Required
	OrbID string `json:"orbId"`

	// Datacenter is the identifier of the target datacenter (matches Orbital namespace name).
	// +kubebuilder:validation:Required
	Datacenter string `json:"datacenter"`

	// Servers is the list of server configurations for this datacenter.
	// +optional
	// +listType=map
	// +listMapKey=orbId
	Servers []ServerSpec `json:"servers,omitempty"`

	// Takeover contains force-resolution directives from the cloud admin.
	// Each entry triggers a ForceOwnership SSA apply to reclaim the field from local:admin.
	// Entries persist until the next bundle replaces the spec (cb-bundler omits consumed entries).
	// +optional
	// +listType=atomic
	Takeover []TakeoverEntry `json:"takeover,omitempty"`
}

// ConfigBundlePhase represents the current lifecycle phase.
// +kubebuilder:validation:Enum=Pending;Applying;Applied;Failed
type ConfigBundlePhase string

const (
	ConfigBundlePhasePending  ConfigBundlePhase = "Pending"
	ConfigBundlePhaseApplying ConfigBundlePhase = "Applying"
	ConfigBundlePhaseApplied  ConfigBundlePhase = "Applied"
	ConfigBundlePhaseFailed   ConfigBundlePhase = "Failed"
)

// Condition type constants for ConfigBundleStatus.Conditions.
const (
	// ConditionReconciled is set by the Decomposition Reconciler when all child CRs are in sync.
	ConditionReconciled = "Reconciled"
)

// ConfigBundleStatus records the controller's observed state.
type ConfigBundleStatus struct {
	// ObservedGeneration is the .metadata.generation the controller has most
	// recently reconciled. When equal to .metadata.generation, the controller
	// has observed the latest spec. Used to distinguish spec-change reconciles
	// from drift / status-only reconciles in logging.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is the current lifecycle phase.
	// +optional
	Phase ConfigBundlePhase `json:"phase,omitempty"`

	// Conditions records detailed status conditions using the standard K8s convention.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastAppliedDigest is the artifact manifest digest (X-Orb-Digest) from the most
	// recent successful consume dispatch.
	// +optional
	LastAppliedDigest string `json:"lastAppliedDigest,omitempty"`

	// LastOrbImportID is the orb import UUID (X-Orb-Import-ID) from the most recent
	// successful consume dispatch. Used for correlation with orb's import history.
	// +optional
	LastOrbImportID string `json:"lastOrbImportID,omitempty"`

	// LastAppliedAt is the time the last successful apply completed.
	// +optional
	LastAppliedAt *metav1.Time `json:"lastAppliedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=cb
// +kubebuilder:printcolumn:name="Datacenter",type=string,JSONPath=`.spec.datacenter`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ConfigBundle is the top-level CR for a datacenter's intended configuration.
// The ConfigBundle controller decomposes its spec into domain child CRs (ServerConfig, etc.)
// using Server-Side Apply with field manager "configbundle-controller".
type ConfigBundle struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ConfigBundleSpec   `json:"spec,omitempty"`
	Status ConfigBundleStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ConfigBundleList contains a list of ConfigBundle.
type ConfigBundleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ConfigBundle `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ConfigBundle{}, &ConfigBundleList{})
}
