package controller

import (
	"encoding/json"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	armadav1 "github.com/armada/configbundle/api/v1"
)

func TestExtractAdminPaths_NoAdminEntries(t *testing.T) {
	fields := []metav1.ManagedFieldsEntry{
		{Manager: "configbundle-controller", FieldsV1: &metav1.FieldsV1{Raw: []byte(`{}`)}},
	}
	paths := extractAdminPaths(fields)
	if len(paths) != 0 {
		t.Errorf("expected 0 paths, got %d", len(paths))
	}
}

func TestExtractAdminPaths_SimpleField(t *testing.T) {
	now := metav1.Now()
	fieldsJSON := `{
		"f:spec": {
			"f:datacenter": {}
		}
	}`
	fields := []metav1.ManagedFieldsEntry{
		{
			Manager:  "local:admin",
			Time:     &now,
			FieldsV1: &metav1.FieldsV1{Raw: []byte(fieldsJSON)},
		},
	}
	paths := extractAdminPaths(fields)
	if len(paths) != 1 {
		t.Fatalf("expected 1 path, got %d: %+v", len(paths), paths)
	}
	if paths[0].path != "spec.datacenter" {
		t.Errorf("expected spec.datacenter, got %q", paths[0].path)
	}
}

func TestExtractAdminPaths_NestedServerField(t *testing.T) {
	now := metav1.Now()
	fieldsJSON := `{
		"f:spec": {
			"f:servers": {
				"k:{\"serviceTag\":\"3RK3V64\"}": {
					"f:idrac": {
						"f:sshEnabled": {}
					}
				}
			}
		}
	}`
	fields := []metav1.ManagedFieldsEntry{
		{
			Manager:  "local:admin",
			Time:     &now,
			FieldsV1: &metav1.FieldsV1{Raw: []byte(fieldsJSON)},
		},
	}
	paths := extractAdminPaths(fields)
	if len(paths) != 1 {
		t.Fatalf("expected 1 path, got %d: %+v", len(paths), paths)
	}
	expected := "spec.servers[serviceTag=3RK3V64].idrac.sshEnabled"
	if paths[0].path != expected {
		t.Errorf("expected %q, got %q", expected, paths[0].path)
	}
}

func TestExtractAdminPaths_MultipleFields(t *testing.T) {
	now := metav1.Now()
	fieldsJSON := `{
		"f:spec": {
			"f:servers": {
				"k:{\"serviceTag\":\"3RK3V64\"}": {
					"f:idrac": {
						"f:sshEnabled": {},
						"f:ipmiEnabled": {}
					}
				}
			}
		}
	}`
	fields := []metav1.ManagedFieldsEntry{
		{
			Manager:  "local:admin",
			Time:     &now,
			FieldsV1: &metav1.FieldsV1{Raw: []byte(fieldsJSON)},
		},
	}
	paths := extractAdminPaths(fields)
	if len(paths) != 2 {
		t.Fatalf("expected 2 paths, got %d: %+v", len(paths), paths)
	}
	pathSet := map[string]bool{}
	for _, p := range paths {
		pathSet[p.path] = true
	}
	for _, want := range []string{
		"spec.servers[serviceTag=3RK3V64].idrac.sshEnabled",
		"spec.servers[serviceTag=3RK3V64].idrac.ipmiEnabled",
	} {
		if !pathSet[want] {
			t.Errorf("missing expected path %q, got %v", want, pathSet)
		}
	}
}

func TestSplitPath(t *testing.T) {
	tests := []struct {
		input string
		want  []pathPart
	}{
		{"spec.datacenter", []pathPart{{field: "spec"}, {field: "datacenter"}}},
		{
			"spec.servers[serviceTag=3RK3V64].idrac.sshEnabled",
			[]pathPart{
				{field: "spec"},
				{field: "servers"},
				{selector: "3RK3V64"},
				{field: "idrac"},
				{field: "sshEnabled"},
			},
		},
	}
	for _, tt := range tests {
		got := splitPath(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("splitPath(%q): got %d parts, want %d: %+v", tt.input, len(got), len(tt.want), got)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("splitPath(%q)[%d]: got %+v, want %+v", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestResolveValue(t *testing.T) {
	spec := armadav1.ConfigBundleSpec{
		Datacenter: "colo",
		Servers: []armadav1.ServerSpec{
			{
				ServiceTag: "3RK3V64",
				Hostname:   "host-01",
				OobIP:      "10.10.1.45",
				Idrac: armadav1.IdracSpec{
					SSHEnabled:      false,
					FirmwareVersion: "7.20.10.05",
				},
			},
		},
	}

	tests := []struct {
		path string
		want interface{}
	}{
		{"spec.datacenter", "colo"},
		{"spec.servers[serviceTag=3RK3V64].hostname", "host-01"},
		{"spec.servers[serviceTag=3RK3V64].idrac.sshEnabled", false},
		{"spec.servers[serviceTag=3RK3V64].idrac.firmwareVersion", "7.20.10.05"},
		{"spec.servers[serviceTag=NONEXIST].hostname", nil},
	}

	for _, tt := range tests {
		got := resolveValue(spec, tt.path)
		if got != tt.want {
			t.Errorf("resolveValue(%q): got %v (%T), want %v (%T)", tt.path, got, got, tt.want, tt.want)
		}
	}
}

func TestExtractOverrides_NoDivergence(t *testing.T) {
	spec := armadav1.ConfigBundleSpec{
		Datacenter: "colo",
		Servers: []armadav1.ServerSpec{
			{
				ServiceTag: "3RK3V64",
				Hostname:   "host-01",
				Idrac:      armadav1.IdracSpec{SSHEnabled: false},
			},
		},
	}

	now := metav1.Now()
	fieldsJSON := `{
		"f:spec": {
			"f:servers": {
				"k:{\"serviceTag\":\"3RK3V64\"}": {
					"f:idrac": {
						"f:sshEnabled": {}
					}
				}
			}
		}
	}`

	cb := &armadav1.ConfigBundle{
		ObjectMeta: metav1.ObjectMeta{
			Name: "colo",
			ManagedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:  "local:admin",
					Time:     &now,
					FieldsV1: &metav1.FieldsV1{Raw: []byte(fieldsJSON)},
				},
			},
		},
		Spec:   spec,
		Status: armadav1.ConfigBundleStatus{LastAppliedDigest: "sha256:abc"},
	}

	r := &DivergenceReporter{lastManifests: map[string]armadav1.ConfigBundleSpec{
		"colo": spec, // same as current — no divergence
	}}

	overrides := r.extractOverrides(cb)
	if len(overrides) != 0 {
		t.Errorf("expected 0 overrides (no divergence), got %d: %+v", len(overrides), overrides)
	}
}

func TestExtractOverrides_WithDivergence(t *testing.T) {
	intended := armadav1.ConfigBundleSpec{
		Datacenter: "colo",
		Servers: []armadav1.ServerSpec{
			{
				ServiceTag: "3RK3V64",
				Hostname:   "host-01",
				Idrac:      armadav1.IdracSpec{SSHEnabled: false},
			},
		},
	}

	current := armadav1.ConfigBundleSpec{
		Datacenter: "colo",
		Servers: []armadav1.ServerSpec{
			{
				ServiceTag: "3RK3V64",
				Hostname:   "host-01",
				Idrac:      armadav1.IdracSpec{SSHEnabled: true}, // admin overrode to true
			},
		},
	}

	now := metav1.Now()
	fieldsJSON := `{
		"f:spec": {
			"f:servers": {
				"k:{\"serviceTag\":\"3RK3V64\"}": {
					"f:idrac": {
						"f:sshEnabled": {}
					}
				}
			}
		}
	}`

	cb := &armadav1.ConfigBundle{
		ObjectMeta: metav1.ObjectMeta{
			Name: "colo",
			ManagedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:  "local:admin",
					Time:     &now,
					FieldsV1: &metav1.FieldsV1{Raw: []byte(fieldsJSON)},
				},
			},
		},
		Spec:   current,
		Status: armadav1.ConfigBundleStatus{LastAppliedDigest: "sha256:abc"},
	}

	r := &DivergenceReporter{lastManifests: map[string]armadav1.ConfigBundleSpec{
		"colo": intended,
	}}

	overrides := r.extractOverrides(cb)
	if len(overrides) != 1 {
		t.Fatalf("expected 1 override, got %d: %+v", len(overrides), overrides)
	}

	o := overrides[0]
	if o.Path != "spec.servers[serviceTag=3RK3V64].idrac.sshEnabled" {
		t.Errorf("path: got %q", o.Path)
	}
	if o.IntendedValue != false {
		t.Errorf("intendedValue: got %v", o.IntendedValue)
	}
	if o.OverrideValue != true {
		t.Errorf("overrideValue: got %v", o.OverrideValue)
	}
	if o.Who != "local:admin" {
		t.Errorf("who: got %q", o.Who)
	}
}

func TestFieldKeyToPath(t *testing.T) {
	tests := []struct {
		prefix string
		key    string
		want   string
	}{
		{"", "f:spec", "spec"},
		{"spec", "f:datacenter", "spec.datacenter"},
		{"spec.servers", `k:{"serviceTag":"3RK3V64"}`, "spec.servers[serviceTag=3RK3V64]"},
		{"", "v:something", ""},
	}
	for _, tt := range tests {
		got := fieldKeyToPath(tt.prefix, tt.key)
		if got != tt.want {
			t.Errorf("fieldKeyToPath(%q, %q): got %q, want %q", tt.prefix, tt.key, got, tt.want)
		}
	}
}

// Verify payload marshals correctly.
func TestDivergencePayload_JSON(t *testing.T) {
	payload := DivergencePayload{
		BundleDigest: "sha256:abc",
		Overrides: []OverrideEntry{
			{
				Path:          "spec.servers[serviceTag=X].idrac.sshEnabled",
				IntendedValue: false,
				OverrideValue: true,
				Who:           "local:admin",
				When:          time.Now().Format(time.RFC3339),
			},
		},
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded DivergencePayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.BundleDigest != "sha256:abc" {
		t.Errorf("bundleDigest: got %q", decoded.BundleDigest)
	}
	if len(decoded.Overrides) != 1 {
		t.Fatalf("overrides: got %d", len(decoded.Overrides))
	}

	// Suppress unused import warning — runtime is used by managedFields FieldsV1.
	_ = runtime.RawExtension{}
}
