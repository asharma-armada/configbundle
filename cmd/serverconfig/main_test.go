package main

import (
	"os"
	"reflect"
	"testing"

	"github.com/kelseyhightower/envconfig"
)

func TestParseAllowlist(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want map[string]bool
	}{
		{"empty string", "", map[string]bool{}},
		{"only commas", ",,,", map[string]bool{}},
		{"single ip", "10.20.21.44", map[string]bool{"10.20.21.44": true}},
		{"two ips", "10.20.21.44,10.20.21.45", map[string]bool{"10.20.21.44": true, "10.20.21.45": true}},
		{"whitespace tolerated", " 10.20.21.44 , 10.20.21.45 ", map[string]bool{"10.20.21.44": true, "10.20.21.45": true}},
		{"trailing comma", "10.20.21.44,", map[string]bool{"10.20.21.44": true}},
		{"duplicates collapse", "10.20.21.44,10.20.21.44", map[string]bool{"10.20.21.44": true}},
		{"comma-separated field names", "sshEnabled,racadmEnabled,ipmiEnabled",
			map[string]bool{"sshEnabled": true, "racadmEnabled": true, "ipmiEnabled": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAllowlist(tc.raw)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseAllowlist(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestConfigDefaults pins the prototype defaults so they don't drift silently.
// Update this test in the same change that changes a default.
func TestConfigDefaults(t *testing.T) {
	// envconfig applies struct-tag defaults only when an env var is truly
	// unset. Clear each known key for the duration of the test.
	for _, k := range []string{
		"IDRAC_OOB_ALLOWLIST",
		"IDRAC_FIELD_ALLOWLIST",
		"IDRAC_CREDENTIALS_NAMESPACE",
		"IDRAC_CREDENTIALS_SECRET",
	} {
		if old, had := os.LookupEnv(k); had {
			_ = os.Unsetenv(k)
			t.Cleanup(func() { _ = os.Setenv(k, old) })
		}
	}

	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		t.Fatalf("envconfig.Process: %v", err)
	}
	if cfg.OobAllowlist != "10.20.21.44" {
		t.Errorf("default OobAllowlist = %q, want %q", cfg.OobAllowlist, "10.20.21.44")
	}
	if cfg.FieldAllowlist != "sshEnabled,racadmEnabled,ipmiEnabled" {
		t.Errorf("default FieldAllowlist = %q, want %q", cfg.FieldAllowlist, "sshEnabled,racadmEnabled,ipmiEnabled")
	}
	if cfg.CredentialsNamespace != "default" {
		t.Errorf("default CredentialsNamespace = %q, want %q", cfg.CredentialsNamespace, "default")
	}
	if cfg.CredentialsSecretName != "idrac-credentials" {
		t.Errorf("default CredentialsSecretName = %q, want %q", cfg.CredentialsSecretName, "idrac-credentials")
	}
}
