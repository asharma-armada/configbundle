package controller

import (
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
		Idrac: armadav1.IdracSpec{
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
		{"sshEnabled", func(d *armadav1.ServerSpec) bool { return d.Idrac.SSHEnabled != nil && *d.Idrac.SSHEnabled == true }},
		{"ipmiEnabled", func(d *armadav1.ServerSpec) bool { return d.Idrac.IPMIEnabled != nil && *d.Idrac.IPMIEnabled == false }},
		{"lockdownModeEnabled", func(d *armadav1.ServerSpec) bool {
			return d.Idrac.LockdownModeEnabled != nil && *d.Idrac.LockdownModeEnabled == true
		}},
		{"osToIdracPassThroughEnabled", func(d *armadav1.ServerSpec) bool {
			return d.Idrac.OsToIdracPassThroughEnabled != nil && *d.Idrac.OsToIdracPassThroughEnabled == true
		}},
		{"usbManagementPortEnabled", func(d *armadav1.ServerSpec) bool {
			return d.Idrac.UsbManagementPortEnabled != nil && *d.Idrac.UsbManagementPortEnabled == false
		}},
		{"dhcpEnabled", func(d *armadav1.ServerSpec) bool { return d.Idrac.DHCPEnabled != nil && *d.Idrac.DHCPEnabled == true }},
		{"racadmEnabled", func(d *armadav1.ServerSpec) bool {
			return d.Idrac.RacadmEnabled != nil && *d.Idrac.RacadmEnabled == false
		}},
		{"firmwareVersion", func(d *armadav1.ServerSpec) bool {
			return d.Idrac.FirmwareVersion != nil && *d.Idrac.FirmwareVersion == "7.20.10.05"
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
		Idrac: armadav1.IdracSpec{
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

	if dst.Idrac.SSHEnabled == nil || *dst.Idrac.SSHEnabled != true {
		t.Error("sshEnabled should be true")
	}
	if dst.Idrac.IPMIEnabled != nil {
		t.Error("ipmiEnabled should remain nil (not set)")
	}
	if dst.Idrac.RacadmEnabled != nil {
		t.Error("racadmEnabled should remain nil (not set)")
	}
	if dst.Hostname != nil {
		t.Error("hostname should remain nil")
	}
}
