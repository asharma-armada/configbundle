package serverconfig

import (
	"reflect"
	"sort"
	"testing"

	"k8s.io/utils/ptr"

	armadav1 "github.com/armada/configbundle/api/v1"
)

// allFields returns an allow-everything map for tests that don't exercise the
// field-allowlist gate.
func allFields() map[string]bool {
	out := map[string]bool{}
	for _, f := range KnownIdracFields {
		out[f] = true
	}
	return out
}

func TestComputeIdracDeltas(t *testing.T) {
	cases := []struct {
		name    string
		spec    armadav1.IdracSpec
		live    map[string]any
		allowed map[string]bool
		want    map[string]string
	}{
		{
			name:    "no intent set, no deltas",
			spec:    armadav1.IdracSpec{},
			live:    map[string]any{"SSH.1.Enable": "Enabled", "Racadm.1.Enable": "Enabled"},
			allowed: allFields(),
			want:    map[string]string{},
		},
		{
			name:    "ssh intent matches live, no delta",
			spec:    armadav1.IdracSpec{SSHEnabled: ptr.To(true)},
			live:    map[string]any{"SSH.1.Enable": "Enabled"},
			allowed: allFields(),
			want:    map[string]string{},
		},
		{
			name:    "ssh intent differs, single-field delta",
			spec:    armadav1.IdracSpec{SSHEnabled: ptr.To(false)},
			live:    map[string]any{"SSH.1.Enable": "Enabled"},
			allowed: allFields(),
			want:    map[string]string{"SSH.1.Enable": "Disabled"},
		},
		{
			name:    "both intents differ, both keyed in single delta",
			spec:    armadav1.IdracSpec{SSHEnabled: ptr.To(false), RacadmEnabled: ptr.To(false)},
			live:    map[string]any{"SSH.1.Enable": "Enabled", "Racadm.1.Enable": "Enabled"},
			allowed: allFields(),
			want:    map[string]string{"SSH.1.Enable": "Disabled", "Racadm.1.Enable": "Disabled"},
		},
		{
			name: "all three intents differ, all three keyed in single delta",
			spec: armadav1.IdracSpec{SSHEnabled: ptr.To(false), RacadmEnabled: ptr.To(false), IPMIEnabled: ptr.To(false)},
			live: map[string]any{
				"SSH.1.Enable":     "Enabled",
				"Racadm.1.Enable":  "Enabled",
				"IPMILan.1.Enable": "Enabled",
			},
			allowed: allFields(),
			want: map[string]string{
				"SSH.1.Enable":     "Disabled",
				"Racadm.1.Enable":  "Disabled",
				"IPMILan.1.Enable": "Disabled",
			},
		},
		{
			name:    "ipmi intent differs, single-field delta",
			spec:    armadav1.IdracSpec{IPMIEnabled: ptr.To(true)},
			live:    map[string]any{"IPMILan.1.Enable": "Disabled"},
			allowed: allFields(),
			want:    map[string]string{"IPMILan.1.Enable": "Enabled"},
		},
		{
			name:    "policy blocks racadm — only ssh in delta",
			spec:    armadav1.IdracSpec{SSHEnabled: ptr.To(false), RacadmEnabled: ptr.To(false)},
			live:    map[string]any{"SSH.1.Enable": "Enabled", "Racadm.1.Enable": "Enabled"},
			allowed: map[string]bool{"sshEnabled": true}, // racadmEnabled NOT in allowlist
			want:    map[string]string{"SSH.1.Enable": "Disabled"},
		},
		{
			name:    "empty allowlist — all fields blocked, no delta",
			spec:    armadav1.IdracSpec{SSHEnabled: ptr.To(false), RacadmEnabled: ptr.To(false)},
			live:    map[string]any{"SSH.1.Enable": "Enabled", "Racadm.1.Enable": "Enabled"},
			allowed: map[string]bool{},
			want:    map[string]string{},
		},
		{
			name:    "case-insensitive live value still matches",
			spec:    armadav1.IdracSpec{SSHEnabled: ptr.To(true)},
			live:    map[string]any{"SSH.1.Enable": "enabled"},
			allowed: allFields(),
			want:    map[string]string{},
		},
		{
			name:    "live attribute missing — treated as not-Enabled",
			spec:    armadav1.IdracSpec{SSHEnabled: ptr.To(true)},
			live:    map[string]any{},
			allowed: allFields(),
			want:    map[string]string{"SSH.1.Enable": "Enabled"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeIdracDeltas(tc.spec, tc.live, tc.allowed)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHasReconcilableIntent(t *testing.T) {
	cases := []struct {
		name    string
		spec    armadav1.IdracSpec
		allowed map[string]bool
		want    bool
	}{
		{"empty spec, empty allowlist", armadav1.IdracSpec{}, map[string]bool{}, false},
		{"empty spec, full allowlist", armadav1.IdracSpec{}, allFields(), false},
		{"ssh set, ssh allowed", armadav1.IdracSpec{SSHEnabled: ptr.To(true)}, map[string]bool{"sshEnabled": true}, true},
		{"ssh set, ssh NOT allowed", armadav1.IdracSpec{SSHEnabled: ptr.To(true)}, map[string]bool{"racadmEnabled": true}, false},
		{"ssh set + racadm set, only ssh allowed", armadav1.IdracSpec{SSHEnabled: ptr.To(true), RacadmEnabled: ptr.To(true)}, map[string]bool{"sshEnabled": true}, true},
		{"all set, empty allowlist", armadav1.IdracSpec{SSHEnabled: ptr.To(true), RacadmEnabled: ptr.To(true)}, map[string]bool{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasReconcilableIntent(tc.spec, tc.allowed); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUnmanagedFields(t *testing.T) {
	cases := []struct {
		name string
		spec armadav1.IdracSpec
		want []string
	}{
		{"empty spec — all unmanaged", armadav1.IdracSpec{}, []string{"sshEnabled", "racadmEnabled", "ipmiEnabled"}},
		{"only ssh set", armadav1.IdracSpec{SSHEnabled: ptr.To(true)}, []string{"racadmEnabled", "ipmiEnabled"}},
		{"only racadm set", armadav1.IdracSpec{RacadmEnabled: ptr.To(false)}, []string{"sshEnabled", "ipmiEnabled"}},
		{"only ipmi set", armadav1.IdracSpec{IPMIEnabled: ptr.To(true)}, []string{"sshEnabled", "racadmEnabled"}},
		{"all three set — none unmanaged", armadav1.IdracSpec{SSHEnabled: ptr.To(true), RacadmEnabled: ptr.To(true), IPMIEnabled: ptr.To(true)}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := unmanagedFields(tc.spec)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPolicyBlockedFields(t *testing.T) {
	cases := []struct {
		name    string
		spec    armadav1.IdracSpec
		allowed map[string]bool
		want    []string
	}{
		{"no intent set — nothing blocked", armadav1.IdracSpec{}, map[string]bool{}, nil},
		{"intent set, field allowed — not blocked", armadav1.IdracSpec{SSHEnabled: ptr.To(true)}, map[string]bool{"sshEnabled": true}, nil},
		{"intent set, field NOT allowed — blocked", armadav1.IdracSpec{SSHEnabled: ptr.To(true)}, map[string]bool{}, []string{"sshEnabled"}},
		{"both intents set, only ssh allowed — racadm blocked", armadav1.IdracSpec{SSHEnabled: ptr.To(true), RacadmEnabled: ptr.To(true)}, map[string]bool{"sshEnabled": true}, []string{"racadmEnabled"}},
		{"both intents set, empty allowlist — both blocked", armadav1.IdracSpec{SSHEnabled: ptr.To(true), RacadmEnabled: ptr.To(true)}, map[string]bool{}, []string{"sshEnabled", "racadmEnabled"}},
		{"all three intents set, only racadm allowed — ssh+ipmi blocked", armadav1.IdracSpec{SSHEnabled: ptr.To(true), RacadmEnabled: ptr.To(true), IPMIEnabled: ptr.To(true)}, map[string]bool{"racadmEnabled": true}, []string{"sshEnabled", "ipmiEnabled"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := policyBlockedFields(tc.spec, tc.allowed)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUnknownAllowlistEntries(t *testing.T) {
	cases := []struct {
		name    string
		allowed map[string]bool
		want    []string
	}{
		{"empty allow set", map[string]bool{}, nil},
		{"all known", map[string]bool{"sshEnabled": true, "racadmEnabled": true}, nil},
		{"one unknown (typo)", map[string]bool{"sshenabled": true}, []string{"sshenabled"}},
		{"mixed known + unknown — only unknown returned", map[string]bool{"sshEnabled": true, "foo": true, "bar": true}, []string{"bar", "foo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UnknownAllowlistEntries(tc.allowed)
			if got != nil {
				sort.Strings(got) // already sorted by impl but defensive
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFormatDeltas(t *testing.T) {
	got := formatDeltas(map[string]string{
		"Racadm.1.Enable": "Disabled",
		"SSH.1.Enable":    "Enabled",
	})
	want := "Racadm.1.Enable=Disabled, SSH.1.Enable=Enabled"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBoolEnabledStringRoundTrip(t *testing.T) {
	if boolToEnableStr(true) != "Enabled" {
		t.Errorf("true → 'Enabled'")
	}
	if boolToEnableStr(false) != "Disabled" {
		t.Errorf("false → 'Disabled'")
	}
	if !enableStrToBool("Enabled") {
		t.Errorf("'Enabled' → true")
	}
	if !enableStrToBool("enabled") {
		t.Errorf("case-insensitive: 'enabled' → true")
	}
	if enableStrToBool("Disabled") {
		t.Errorf("'Disabled' → false")
	}
	if enableStrToBool("") {
		t.Errorf("empty string → false")
	}
}
