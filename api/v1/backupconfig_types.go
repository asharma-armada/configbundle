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

// BackupBlock holds desired backup configuration for one mechanism (velero or
// etcd), sourced from Orbital VeleroBackup / EtcdBackup nodes. Shared shape
// because the two mechanisms expose identical knobs today (enabled, schedule,
// location); when they diverge in the future, split into VeleroBackupSpec and
// EtcdBackupSpec.
//
// All admin-overridable fields are pointers with omitempty so SSA partial
// patches can omit admin-owned fields (matches the IdracSpec / ADR-007 pattern).
// OrbID is identity metadata set by the bundler — required, never overridable.
type BackupBlock struct {
	// OrbID is the immutable Orbital identifier for the backup-node this block
	// represents (e.g. "colo:cluster-001-velero"). Set by the bundler.
	// Identity-only; admin overrides never touch it.
	// +kubebuilder:validation:Required
	OrbID string `json:"orbId"`

	// Enabled toggles the backup mechanism on or off.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Schedule is a cron expression for when the backup runs (e.g. "0 2 * * *").
	// +optional
	Schedule *string `json:"schedule,omitempty"`

	// Location is the storage location the backup writes to (e.g. an S3 URL or a
	// Velero BackupStorageLocation name). Free-form; mechanism-specific.
	// +optional
	Location *string `json:"location,omitempty"`
}

// VeleroTypeName / EtcdTypeName are the orbital GraphQL type names for the
// backup-block nodes. Used in TakeoverEntry.Type / IgnoredEntry.Type when the
// divergence reporter emits an entry for a local override on a backup field.
// One constant per nested struct that has its own orbital identity; when adding
// a new nested type (e.g. S3Sync), add a sibling constant.
const (
	VeleroTypeName = "VeleroBackup"
	EtcdTypeName   = "EtcdBackup"
)

// BackupConfigSpec describes one Kubernetes cluster's desired backup configuration.
// Created and updated by the ConfigBundle Controller via SSA. BackupConfig is
// derived state — admin overrides happen on the parent ConfigBundle CR only.
type BackupConfigSpec struct {
	// OrbID is the immutable Orbital identifier for this cluster
	// (mirrors ConfigBundle.spec.clusters[].orbId). Carried on the child so
	// cross-system grep, audit logs, and downstream telemetry can correlate
	// without a parent round-trip.
	// +kubebuilder:validation:Required
	OrbID string `json:"orbId"`

	// Velero holds desired Velero backup configuration. Absent = the controller
	// does not manage Velero for this cluster.
	// +optional
	Velero *BackupBlock `json:"velero,omitempty"`

	// Etcd holds desired etcd backup configuration. Absent = the controller does
	// not manage etcd backups for this cluster.
	// +optional
	Etcd *BackupBlock `json:"etcd,omitempty"`
}

// BackupConfigPhase represents the current lifecycle phase.
// +kubebuilder:validation:Enum=Pending;Applied;Diverged
type BackupConfigPhase string

const (
	BackupConfigPhasePending  BackupConfigPhase = "Pending"
	BackupConfigPhaseApplied  BackupConfigPhase = "Applied"
	BackupConfigPhaseDiverged BackupConfigPhase = "Diverged"
)

// BackupConfigStatus records the controller's observed state. Mirrors the
// ServerConfigStatus shape so operators can reason about both child kinds the
// same way (Phase, ObservedGeneration, Conditions, per-field Observed ledger,
// bounded RecentPatches action history).
type BackupConfigStatus struct {
	// Phase is the current lifecycle phase.
	// +optional
	Phase BackupConfigPhase `json:"phase,omitempty"`

	// ObservedGeneration is the spec.generation the controller last successfully
	// reconciled. K8s-standard "are we converged?" signal.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions records detailed status conditions.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Observed holds the controller's per-field confirmed values — values the
	// controller has successfully landed on the target (Velero Schedule PATCH
	// success, or etcd CronJob PATCH success), or confirmed already match on a
	// no-op reconcile. Absence of a field means the controller has never
	// confirmed it.
	// +optional
	Observed ObservedBackup `json:"observed,omitempty"`

	// RecentPatches is a bounded list of the last few PATCH actions, newest
	// first. Capped at 5 entries to keep the status object small.
	// +optional
	// +listType=atomic
	RecentPatches []RecentPatch `json:"recentPatches,omitempty"`
}

// ObservedBackup contains per-mechanism observed-state ledgers.
type ObservedBackup struct {
	// Velero holds controller-confirmed Velero backup field values.
	// +optional
	Velero ObservedBackupBlock `json:"velero,omitempty"`

	// Etcd holds controller-confirmed etcd backup field values.
	// +optional
	Etcd ObservedBackupBlock `json:"etcd,omitempty"`
}

// ObservedBackupBlock mirrors the controller-managed subset of BackupBlock.
// Pointer types so absence means "never confirmed" (vs. "confirmed and false").
type ObservedBackupBlock struct {
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
	// +optional
	Schedule *string `json:"schedule,omitempty"`
	// +optional
	Location *string `json:"location,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=bc
// +kubebuilder:printcolumn:name="OrbID",type=string,JSONPath=`.spec.orbId`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// BackupConfig is a domain child CR owned by a ConfigBundle.
// Created and updated by the ConfigBundle Controller via SSA (field manager:
// "configbundle-controller"). The BackupConfig Controller (separate repo)
// actuates the spec by SSA-patching Velero Schedule CRDs and an etcd backup
// CronJob in the same cluster.
type BackupConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BackupConfigSpec   `json:"spec,omitempty"`
	Status BackupConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BackupConfigList contains a list of BackupConfig.
type BackupConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BackupConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BackupConfig{}, &BackupConfigList{})
}
