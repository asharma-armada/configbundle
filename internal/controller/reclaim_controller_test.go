package controller

import (
	"encoding/json"
	"sort"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// mfEntry constructs a ManagedFieldsEntry with a given manager and FieldsV1 tree.
func mfEntry(t *testing.T, manager string, fields map[string]any) metav1.ManagedFieldsEntry {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal fields: %v", err)
	}
	return metav1.ManagedFieldsEntry{
		Manager:  manager,
		FieldsV1: &metav1.FieldsV1{Raw: raw},
	}
}

// claim is shorthand for a single-leaf claim under f:spec.f:servers.k:{orbId}.f:idrac.f:<field>.
func claim(field string) map[string]any {
	return map[string]any{
		"f:spec": map[string]any{
			"f:servers": map[string]any{
				`k:{"orbId":"colo:srv-1"}`: map[string]any{
					"f:idracSettings": map[string]any{
						"f:" + field: map[string]any{},
					},
				},
			},
		},
	}
}

// multiClaim builds an idrac claim covering multiple leaves under the same server.
func multiClaim(fields ...string) map[string]any {
	idrac := map[string]any{}
	for _, f := range fields {
		idrac["f:"+f] = map[string]any{}
	}
	return map[string]any{
		"f:spec": map[string]any{
			"f:servers": map[string]any{
				`k:{"orbId":"colo:srv-1"}`: map[string]any{
					"f:idracSettings": idrac,
				},
			},
		},
	}
}

func TestLocalReleasedFieldPaths(t *testing.T) {
	cases := []struct {
		name     string
		old      []metav1.ManagedFieldsEntry
		new      []metav1.ManagedFieldsEntry
		released int // count of released paths (set membership; counts only)
	}{
		{
			name: "simple release: local:admin lets go of racadmEnabled",
			old: []metav1.ManagedFieldsEntry{
				mfEntry(t, "local:admin", claim("racadmEnabled")),
			},
			new:      []metav1.ManagedFieldsEntry{},
			released: 1,
		},
		{
			name: "rotation: local:admin releases, local:bob picks up same field — not a release",
			old: []metav1.ManagedFieldsEntry{
				mfEntry(t, "local:admin", claim("racadmEnabled")),
			},
			new: []metav1.ManagedFieldsEntry{
				mfEntry(t, "local:bob", claim("racadmEnabled")),
			},
			released: 0,
		},
		{
			name: "partial release: local:admin keeps racadmEnabled, drops sshEnabled",
			old: []metav1.ManagedFieldsEntry{
				mfEntry(t, "local:admin", multiClaim("racadmEnabled", "sshEnabled")),
			},
			new: []metav1.ManagedFieldsEntry{
				mfEntry(t, "local:admin", claim("racadmEnabled")),
			},
			released: 1,
		},
		{
			name: "pure claim: empty old, claim added — not a release",
			old:  []metav1.ManagedFieldsEntry{},
			new: []metav1.ManagedFieldsEntry{
				mfEntry(t, "local:admin", claim("racadmEnabled")),
			},
			released: 0,
		},
		{
			name: "additional local manager: local:admin stays, local:bob joins — not a release",
			old: []metav1.ManagedFieldsEntry{
				mfEntry(t, "local:admin", claim("racadmEnabled")),
			},
			new: []metav1.ManagedFieldsEntry{
				mfEntry(t, "local:admin", claim("racadmEnabled")),
				mfEntry(t, "local:bob", claim("racadmEnabled")),
			},
			released: 0,
		},
		{
			name: "non-local manager change: controller's claim mutates — not a release",
			old: []metav1.ManagedFieldsEntry{
				mfEntry(t, "local:admin", claim("racadmEnabled")),
				mfEntry(t, "configbundle-controller", claim("sshEnabled")),
			},
			new: []metav1.ManagedFieldsEntry{
				mfEntry(t, "local:admin", claim("racadmEnabled")),
			},
			released: 0,
		},
		{
			name: "multi-field release: A, B, C → A",
			old: []metav1.ManagedFieldsEntry{
				mfEntry(t, "local:admin", multiClaim("racadmEnabled", "sshEnabled", "ipmiEnabled")),
			},
			new: []metav1.ManagedFieldsEntry{
				mfEntry(t, "local:admin", claim("racadmEnabled")),
			},
			released: 2,
		},
		{
			name:     "no managedFields changes at all",
			old:      []metav1.ManagedFieldsEntry{mfEntry(t, "local:admin", claim("racadmEnabled"))},
			new:      []metav1.ManagedFieldsEntry{mfEntry(t, "local:admin", claim("racadmEnabled"))},
			released: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := localReleasedFieldPaths(tc.old, tc.new)
			if len(got) != tc.released {
				sort.Strings(got)
				t.Errorf("expected %d released paths, got %d: %v", tc.released, len(got), got)
			}
		})
	}
}

func TestCollectLocalLeafPaths_IgnoresNonLocalManagers(t *testing.T) {
	fields := []metav1.ManagedFieldsEntry{
		mfEntry(t, "configbundle-controller", claim("racadmEnabled")),
		mfEntry(t, "kube-controller-manager", claim("sshEnabled")),
	}
	paths := collectLocalLeafPaths(fields)
	if len(paths) != 0 {
		t.Errorf("expected zero paths for non-local managers, got %v", paths)
	}
}

func TestCollectLocalLeafPaths_MultipleLocalManagersMerged(t *testing.T) {
	fields := []metav1.ManagedFieldsEntry{
		mfEntry(t, "local:admin", claim("racadmEnabled")),
		mfEntry(t, "local:bob", claim("sshEnabled")),
	}
	paths := collectLocalLeafPaths(fields)
	if len(paths) != 2 {
		t.Errorf("expected two distinct paths from two local managers, got %d: %v", len(paths), paths)
	}
}
