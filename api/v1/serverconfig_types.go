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

// ServerConfigSpec mirrors the ServerSpec from ConfigBundle.
// The ConfigBundle controller creates and updates this CR via SSA. ServerConfig
// is derived state — admin overrides happen on the parent ConfigBundle CR only.
//
// Field types match ServerSpec (pointer leaves, see ADR-007) so the parent→child
// copy is a direct assignment.
type ServerConfigSpec struct {
	// OrbID is the immutable Orbital identifier for this server
	// (mirrors ConfigBundle.spec.servers[].orbId). Carried on the child so
	// cross-system grep, audit logs, and downstream telemetry can correlate
	// without a parent round-trip. See docs/plans/server-identity-orbid.md.
	// +kubebuilder:validation:Required
	OrbID string `json:"orbId"`

	// ServiceTag is the original-case Dell service tag (e.g. "3RK3V64").
	// Repeated here (vs. deriving from CR name) so the controller has it without string manipulation.
	// +kubebuilder:validation:Required
	ServiceTag string `json:"serviceTag"`

	// Hostname is the server's hostname for display and logging.
	// +kubebuilder:validation:Required
	Hostname *string `json:"hostname,omitempty"`

	// OobIP is the iDRAC management IP. The ServerConfig controller targets Redfish here.
	// +kubebuilder:validation:Required
	OobIP *string `json:"oobIP,omitempty"`

	// Idrac holds desired iDRAC configuration.
	// +optional
	Idrac IdracSpec `json:"idrac,omitempty"`
}

// ServerConfigPhase represents the current lifecycle phase.
// +kubebuilder:validation:Enum=Pending;Applied;Diverged
type ServerConfigPhase string

const (
	ServerConfigPhasePending  ServerConfigPhase = "Pending"
	ServerConfigPhaseApplied  ServerConfigPhase = "Applied"
	ServerConfigPhaseDiverged ServerConfigPhase = "Diverged"
)

// ServerConfigStatus records the controller's observed state.
type ServerConfigStatus struct {
	// Phase is the current lifecycle phase.
	// +optional
	Phase ServerConfigPhase `json:"phase,omitempty"`

	// ObservedGeneration is the spec.generation the controller last successfully
	// reconciled. Tooling compares this to metadata.generation to know "has the
	// controller caught up to my spec change yet?" — the K8s-standard
	// "are we converged?" signal. Bumped on every successful reconcile, even
	// when no PATCH was needed; gated by "only write if it would change" so
	// periodic polls don't churn the apiserver.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions records detailed status conditions.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedFirmwareVersion is the firmware version read from Redfish at last reconcile.
	// +optional
	ObservedFirmwareVersion string `json:"observedFirmwareVersion,omitempty"`

	// Observed holds the controller's per-field confirmed values — values the
	// controller has successfully landed on the device (PATCH success), or
	// confirmed already match on a no-op reconcile. Survives multi-step
	// reconciles where the condition message can only hold the most recent
	// action. Absence of a field means the controller has never confirmed it.
	// +optional
	Observed ObservedStatus `json:"observed,omitempty"`

	// RecentPatches is a bounded list of the last few PATCH actions, newest
	// first. Lets operators see all PATCHes from a multi-step reconcile (e.g.
	// a takeover roundtrip that produces multiple sc-controller reconciles)
	// instead of just the most recent one — the condition `message` field
	// can only hold one action and gets overwritten across reconciles.
	// Capped at 5 entries to keep the status object small.
	// +optional
	// +listType=atomic
	RecentPatches []RecentPatch `json:"recentPatches,omitempty"`
}

// RecentPatch is one entry in ServerConfigStatus.RecentPatches.
type RecentPatch struct {
	// Time is when the PATCH succeeded against the iDRAC.
	Time metav1.Time `json:"time"`

	// Message describes which fields were PATCHed, e.g.
	// "PATCHed 2 attribute(s): IPMILan.1.Enable=Disabled, SSH.1.Enable=Disabled".
	// Same format as the Reconciled condition's message.
	Message string `json:"message"`
}

// ObservedStatus contains per-domain observed-state ledgers.
type ObservedStatus struct {
	// Idrac holds the controller-confirmed iDRAC field values.
	// +optional
	Idrac ObservedIdracStatus `json:"idrac,omitempty"`
}

// ObservedIdracStatus mirrors the controller-managed subset of IdracSpec.
// Pointer types so absence means "never confirmed" (vs. "confirmed and false").
type ObservedIdracStatus struct {
	// +optional
	SSHEnabled *bool `json:"sshEnabled,omitempty"`
	// +optional
	IPMIEnabled *bool `json:"ipmiEnabled,omitempty"`
	// +optional
	RacadmEnabled *bool `json:"racadmEnabled,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=sc
// +kubebuilder:printcolumn:name="ServiceTag",type=string,JSONPath=`.spec.serviceTag`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:printcolumn:name="OrbID",type=string,priority=1,JSONPath=`.spec.orbId`
// +kubebuilder:printcolumn:name="Hostname",type=string,priority=1,JSONPath=`.spec.hostname`
// +kubebuilder:printcolumn:name="OOB IP",type=string,priority=1,JSONPath=`.spec.oobIP`

// ServerConfig is a domain child CR owned by a ConfigBundle.
// Created and updated by the ConfigBundle Controller via SSA (field manager: "configbundle-controller").
// The ServerConfig Controller (separate, out of scope for v1) actuates the spec via Redfish.
type ServerConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ServerConfigSpec   `json:"spec,omitempty"`
	Status ServerConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ServerConfigList contains a list of ServerConfig.
type ServerConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ServerConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ServerConfig{}, &ServerConfigList{})
}
