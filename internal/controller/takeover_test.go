package controller

import (
	"encoding/json"
	"testing"

	"k8s.io/utils/ptr"

	armadav1 "github.com/armada/configbundle/api/v1"
)

func TestSetFieldOnServer_IdracFields(t *testing.T) {
	src := &armadav1.ServerSpec{
		OrbID:      "colo:srv-3rk3v64",
		ServiceTag: "3RK3V64",
		Hostname:   ptr.To("host-01"),
		OobIP:      ptr.To("10.10.1.45"),
		IdracSettings: armadav1.IdracSettingsSpec{
			FirmwareVersion:             ptr.To("7.20.10.05"),
			SSHEnabled:                  ptr.To(true),
			IPMIEnabled:                 ptr.To(false),
			LockdownModeEnabled:         ptr.To(true),
			OsToIdracPassThroughEnabled: ptr.To(true),
			UsbManagementPortEnabled:    ptr.To(false),
			DHCPEnabled:                 ptr.To(true),
			RacadmEnabled:               ptr.To(false),
		},
	}

	tests := []struct {
		field string
		check func(dst *armadav1.ServerSpec) bool
	}{
		{"sshEnabled", func(d *armadav1.ServerSpec) bool {
			return d.IdracSettings.SSHEnabled != nil && *d.IdracSettings.SSHEnabled == true
		}},
		{"ipmiEnabled", func(d *armadav1.ServerSpec) bool {
			return d.IdracSettings.IPMIEnabled != nil && *d.IdracSettings.IPMIEnabled == false
		}},
		{"lockdownModeEnabled", func(d *armadav1.ServerSpec) bool {
			return d.IdracSettings.LockdownModeEnabled != nil && *d.IdracSettings.LockdownModeEnabled == true
		}},
		{"osToIdracPassThroughEnabled", func(d *armadav1.ServerSpec) bool {
			return d.IdracSettings.OsToIdracPassThroughEnabled != nil && *d.IdracSettings.OsToIdracPassThroughEnabled == true
		}},
		{"usbManagementPortEnabled", func(d *armadav1.ServerSpec) bool {
			return d.IdracSettings.UsbManagementPortEnabled != nil && *d.IdracSettings.UsbManagementPortEnabled == false
		}},
		{"dhcpEnabled", func(d *armadav1.ServerSpec) bool {
			return d.IdracSettings.DHCPEnabled != nil && *d.IdracSettings.DHCPEnabled == true
		}},
		{"racadmEnabled", func(d *armadav1.ServerSpec) bool {
			return d.IdracSettings.RacadmEnabled != nil && *d.IdracSettings.RacadmEnabled == false
		}},
		{"firmwareVersion", func(d *armadav1.ServerSpec) bool {
			return d.IdracSettings.FirmwareVersion != nil && *d.IdracSettings.FirmwareVersion == "7.20.10.05"
		}},
		{"hostname", func(d *armadav1.ServerSpec) bool { return d.Hostname != nil && *d.Hostname == "host-01" }},
		{"oobIP", func(d *armadav1.ServerSpec) bool { return d.OobIP != nil && *d.OobIP == "10.10.1.45" }},
	}

	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			dst := &armadav1.ServerSpec{OrbID: "colo:srv-3rk3v64", ServiceTag: "3RK3V64"}
			if err := setFieldOnServer(dst, src, tt.field); err != nil {
				t.Fatalf("setFieldOnServer(%q): %v", tt.field, err)
			}
			if !tt.check(dst) {
				t.Errorf("field %q not correctly set on dst", tt.field)
			}
			// Verify serviceTag was not overwritten
			if dst.ServiceTag != "3RK3V64" {
				t.Errorf("serviceTag changed: got %q", dst.ServiceTag)
			}
			// Verify orbId was not overwritten
			if dst.OrbID != "colo:srv-3rk3v64" {
				t.Errorf("orbId changed: got %q", dst.OrbID)
			}
		})
	}
}

func TestSetFieldOnServer_UnknownField(t *testing.T) {
	dst := &armadav1.ServerSpec{}
	src := &armadav1.ServerSpec{}
	err := setFieldOnServer(dst, src, "nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestSetFieldOnServer_MinimalPatch(t *testing.T) {
	src := &armadav1.ServerSpec{
		OrbID:      "colo:srv-3rk3v64",
		ServiceTag: "3RK3V64",
		Hostname:   ptr.To("host-01"),
		OobIP:      ptr.To("10.10.1.45"),
		IdracSettings: armadav1.IdracSettingsSpec{
			SSHEnabled:    ptr.To(true),
			IPMIEnabled:   ptr.To(true),
			RacadmEnabled: ptr.To(true),
		},
	}

	// Only set sshEnabled — verify other fields remain zero
	dst := &armadav1.ServerSpec{OrbID: "colo:srv-3rk3v64", ServiceTag: "3RK3V64"}
	if err := setFieldOnServer(dst, src, "sshEnabled"); err != nil {
		t.Fatal(err)
	}

	if dst.IdracSettings.SSHEnabled == nil || *dst.IdracSettings.SSHEnabled != true {
		t.Error("sshEnabled should be true")
	}
	if dst.IdracSettings.IPMIEnabled != nil {
		t.Error("ipmiEnabled should remain nil (not set)")
	}
	if dst.IdracSettings.RacadmEnabled != nil {
		t.Error("racadmEnabled should remain nil (not set)")
	}
	if dst.Hostname != nil {
		t.Error("hostname should remain nil")
	}
}

// reconstructApplyExcluding produces the Apply body cb-controller submits as
// each non-self manager to release takeover-target claims via SSA's
// release-on-omit semantic. The reconstruction must:
//   - include orbId (listMapKey) so SSA matches the existing list element
//   - include serviceTag (CRD-Required) so validation passes
//   - include the manager's OTHER claimed fields with current spec values so
//     those claims persist (Ignore-resolved and pending-divergence claims)
//   - OMIT any field whose (serverOrbId, field) pair appears in the exclusion
//     set so SSA's release-on-omit strips just that claim
//   - return touched=true iff at least one excluded field was actually
//     present in this manager's claims (caller skips the Apply otherwise)
func TestReconstructApplyExcluding(t *testing.T) {
	// Live spec: one server with several idrac leaves set.
	specMap := map[string]any{
		"servers": []any{
			map[string]any{
				"orbId":      "colo:CWJHDX3",
				"serviceTag": "CWJHDX3",
				"hostname":   "r09-u02.colo-galleon",
				"oobIP":      "10.10.1.45",
				"idracSettings": map[string]any{
					"sshEnabled":    true,
					"dhcpEnabled":   true,
					"ipmiEnabled":   false,
					"racadmEnabled": true,
				},
			},
		},
	}

	t.Run("local:admin claims sshEnabled+dhcpEnabled; only sshEnabled is takeover-target", func(t *testing.T) {
		owned := unmarshalFields(t, `{
			"f:servers": {
				"k:{\"orbId\":\"colo:CWJHDX3\"}": {
					".": {},
					"f:idracSettings": {"f:sshEnabled": {}, "f:dhcpEnabled": {}},
					"f:orbId": {}
				}
			}
		}`)
		exclude := map[string]map[string]bool{
			"colo:CWJHDX3": {"sshEnabled": true},
		}
		out, touched := reconstructApplyExcluding(specMap, owned, exclude)
		if !touched {
			t.Fatal("expected touched=true (excluded field was present)")
		}
		servers, _ := out["servers"].([]any)
		if len(servers) != 1 {
			t.Fatalf("expected 1 server, got %d", len(servers))
		}
		entry := servers[0].(map[string]any)
		if entry["orbId"] != "colo:CWJHDX3" {
			t.Errorf("missing orbId in reconstructed entry")
		}
		// serviceTag is NOT included — see takeover.go comment about CRD
		// validation running against merged state, not the Apply body.
		if _, has := entry["serviceTag"]; has {
			t.Error("serviceTag should not be injected; would silently extend claims")
		}
		idrac, _ := entry["idracSettings"].(map[string]any)
		if _, has := idrac["sshEnabled"]; has {
			t.Error("sshEnabled (takeover target) should be omitted")
		}
		if v, _ := idrac["dhcpEnabled"].(bool); v != true {
			t.Errorf("dhcpEnabled (not takeover) must be preserved with live value, got %v", idrac["dhcpEnabled"])
		}
	})

	t.Run("local:admin's only claim is a takeover target — server entry fully omitted (releases listMapKey too)", func(t *testing.T) {
		// When Accept/Reject consumes EVERY leaf this manager owned on the server,
		// the release body must NOT include the server entry at all. Including it
		// with just {orbId: X} would preserve the listMapKey claim, leaving a
		// residual "manager touched this entry" marker in managedFields — which
		// violates orbital's "Accept/Reject = full release" semantic.
		owned := unmarshalFields(t, `{
			"f:servers": {
				"k:{\"orbId\":\"colo:CWJHDX3\"}": {
					".": {},
					"f:idracSettings": {"f:sshEnabled": {}},
					"f:orbId": {}
				}
			}
		}`)
		exclude := map[string]map[string]bool{
			"colo:CWJHDX3": {"sshEnabled": true},
		}
		out, touched := reconstructApplyExcluding(specMap, owned, exclude)
		if !touched {
			t.Fatal("expected touched=true (excluded field was present)")
		}
		servers, _ := out["servers"].([]any)
		if len(servers) != 0 {
			t.Errorf("expected zero server entries in release body (entry fully released), got %d: %+v", len(servers), servers)
		}
	})

	t.Run("manager doesn't claim any takeover-target — touched=false", func(t *testing.T) {
		owned := unmarshalFields(t, `{
			"f:servers": {
				"k:{\"orbId\":\"colo:CWJHDX3\"}": {
					".": {},
					"f:idracSettings": {"f:dhcpEnabled": {}},
					"f:orbId": {}
				}
			}
		}`)
		exclude := map[string]map[string]bool{
			"colo:CWJHDX3": {"sshEnabled": true},
		}
		_, touched := reconstructApplyExcluding(specMap, owned, exclude)
		if touched {
			t.Error("touched must be false — manager claims dhcpEnabled but takeover targets only sshEnabled")
		}
	})

	t.Run("top-level Server field is a takeover target", func(t *testing.T) {
		owned := unmarshalFields(t, `{
			"f:servers": {
				"k:{\"orbId\":\"colo:CWJHDX3\"}": {
					".": {},
					"f:hostname": {},
					"f:oobIP": {},
					"f:orbId": {}
				}
			}
		}`)
		exclude := map[string]map[string]bool{
			"colo:CWJHDX3": {"hostname": true},
		}
		out, touched := reconstructApplyExcluding(specMap, owned, exclude)
		if !touched {
			t.Fatal("expected touched=true")
		}
		entry := out["servers"].([]any)[0].(map[string]any)
		if _, has := entry["hostname"]; has {
			t.Error("hostname (takeover target) should be omitted")
		}
		if entry["oobIP"] != "10.10.1.45" {
			t.Errorf("oobIP must be preserved with live value, got %v", entry["oobIP"])
		}
	})
}

func unmarshalFields(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}
