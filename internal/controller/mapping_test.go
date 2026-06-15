package controller

import (
	"strings"
	"testing"
)

func newTestMapping(t *testing.T) *Mapping {
	t.Helper()
	m, err := ParseMapping([]byte(`{
		"bundleDigest": "sha256:abc",
		"items": [
			{"path": "spec", "orbId": "colo:colo-galleon", "type": "DataCenter"},
			{"path": "spec.servers[orbId=colo:srv-3rk3v64]", "orbId": "colo:srv-001", "type": "Server"},
			{"path": "spec.servers[orbId=colo:srv-3rk3v64].idrac", "orbId": "colo:srv-001-idrac", "type": "IdracSettings"},
			{"path": "spec.servers[orbId=colo:srv-jl3pv82]", "orbId": "colo:srv-002", "type": "Server"},
			{"path": "spec.servers[orbId=colo:srv-jl3pv82].idrac", "orbId": "colo:srv-002-idrac", "type": "IdracSettings"}
		]
	}`))
	if err != nil {
		t.Fatalf("ParseMapping: %v", err)
	}
	return m
}

func TestResolve_LongestPrefixWins(t *testing.T) {
	m := newTestMapping(t)

	cases := []struct {
		name      string
		path      string
		wantOrbID string
		wantField string
		wantType  string
	}{
		{
			name:      "idrac field resolves to IdracSettings orbId, not Server",
			path:      "spec.servers[orbId=colo:srv-3rk3v64].idrac.sshEnabled",
			wantOrbID: "colo:srv-001-idrac",
			wantField: "sshEnabled",
			wantType:  "IdracSettings",
		},
		{
			name:      "top-level server field resolves to Server orbId",
			path:      "spec.servers[orbId=colo:srv-3rk3v64].oobIP",
			wantOrbID: "colo:srv-001",
			wantField: "oobIP",
			wantType:  "Server",
		},
		{
			name:      "datacenter-level field resolves to DataCenter orbId",
			path:      "spec.datacenter",
			wantOrbID: "colo:colo-galleon",
			wantField: "datacenter",
			wantType:  "DataCenter",
		},
		{
			name:      "second server resolves independently",
			path:      "spec.servers[orbId=colo:srv-jl3pv82].idrac.ipmiEnabled",
			wantOrbID: "colo:srv-002-idrac",
			wantField: "ipmiEnabled",
			wantType:  "IdracSettings",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotOrbID, gotField, gotType, err := m.Resolve(tc.path)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tc.path, err)
			}
			if gotOrbID != tc.wantOrbID {
				t.Errorf("orbId: got %q, want %q", gotOrbID, tc.wantOrbID)
			}
			if gotField != tc.wantField {
				t.Errorf("field: got %q, want %q", gotField, tc.wantField)
			}
			if gotType != tc.wantType {
				t.Errorf("type: got %q, want %q", gotType, tc.wantType)
			}
		})
	}
}

func TestResolve_NoPrefixMatch(t *testing.T) {
	m := newTestMapping(t)
	_, _, _, err := m.Resolve("status.foo")
	if err == nil {
		t.Fatal("expected error for unmatched path, got nil")
	}
	if !strings.Contains(err.Error(), "no mapping prefix matches") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolve_RefusesShallowMatch(t *testing.T) {
	m := newTestMapping(t)
	_, _, _, err := m.Resolve("spec.clusters[clusterName=prod-1].config.someField")
	if err == nil {
		t.Fatal("expected error when shallow prefix would produce structured leaf, got nil")
	}
	if !strings.Contains(err.Error(), "too shallow") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolve_ExactPathIsRejected(t *testing.T) {
	m := newTestMapping(t)
	_, _, _, err := m.Resolve("spec.servers[orbId=colo:srv-3rk3v64].idrac")
	if err == nil {
		t.Fatal("expected error for ConfigItem-boundary path, got nil")
	}
}

func TestResolve_PrefixBoundaryDot(t *testing.T) {
	m, err := ParseMapping([]byte(`{
		"bundleDigest": "sha256:abc",
		"items": [
			{"path": "spec.servers[orbId=colo:srv-3rk3v64]", "orbId": "colo:srv-001"}
		]
	}`))
	if err != nil {
		t.Fatalf("ParseMapping: %v", err)
	}
	_, _, _, err = m.Resolve("spec.servers[orbId=colo:srv-3rk3v64z].field")
	if err == nil {
		t.Fatal("expected mismatch for prefix without dot boundary, got match")
	}
}

func TestParseMapping_EmptyItems(t *testing.T) {
	_, err := ParseMapping([]byte(`{"bundleDigest":"sha256:abc","items":[]}`))
	if err == nil {
		t.Fatal("expected error for empty items, got nil")
	}
}

func TestParseMapping_InvalidJSON(t *testing.T) {
	_, err := ParseMapping([]byte(`{not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}
