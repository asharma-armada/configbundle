package serverconfig

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"k8s.io/utils/ptr"
)

// gather renders the collector's series into a flat "name{k=v,...}" string per
// series so tests can assert on labels via substring. Uses a private registry
// so tests don't touch the global controller-runtime one.
func gather(t *testing.T, c prometheus.Collector) string {
	t.Helper()
	reg := prometheus.NewRegistry()
	if err := reg.Register(c); err != nil {
		t.Fatalf("register collector: %v", err)
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var sb strings.Builder
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			sb.WriteString(mf.GetName() + "{")
			for _, l := range m.GetLabel() {
				sb.WriteString(l.GetName() + "=" + l.GetValue() + ",")
			}
			sb.WriteString("}\n")
		}
	}
	return sb.String()
}

func TestIdracObservedCollector_RendersCurrentState(t *testing.T) {
	store := newIdracObservedStore()
	c := newIdracObservedCollector(store)

	store.set(observedIdrac{
		server:          "r09-u06",
		oobIP:           "10.20.21.44",
		orbID:           "colo:CFRHDX3",
		firmwareVersion: "7.20.10.05",
		fields: map[string]*bool{
			"sshEnabled":    ptr.To(false),
			"ipmiEnabled":   ptr.To(true),
			"racadmEnabled": ptr.To(true),
		},
	})

	got := gather(t, c)
	for _, want := range []string{
		"oob_ip=10.20.21.44", "server=r09-u06", "orb_id=colo:CFRHDX3",
		"firmware_version=7.20.10.05",
		"ssh_enabled=false", "ipmi_enabled=true", "racadm_enabled=true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected series to contain %q\ngot:\n%s", want, got)
		}
	}
}

// The load-bearing test: when an observed value changes, the info metric must
// REPLACE the server's row, not accumulate a stale one. This is the whole
// reason for the Collector-over-snapshot design vs a mutated GaugeVec.
func TestIdracObservedCollector_ValueChangeReplacesSeries(t *testing.T) {
	store := newIdracObservedStore()
	c := newIdracObservedCollector(store)

	base := func(ssh bool) observedIdrac {
		return observedIdrac{
			server: "r09-u06", oobIP: "10.20.21.44", orbID: "colo:CFRHDX3",
			firmwareVersion: "7.20.10.05",
			fields:          map[string]*bool{"sshEnabled": ptr.To(ssh), "ipmiEnabled": ptr.To(true), "racadmEnabled": ptr.To(true)},
		}
	}

	store.set(base(true))
	if n := testutil.CollectAndCount(c); n != 1 {
		t.Fatalf("expected 1 series after first set, got %d", n)
	}

	// SSH flips true→false. Same server; the row must be replaced, count stays 1.
	store.set(base(false))
	if n := testutil.CollectAndCount(c); n != 1 {
		t.Fatalf("value change must REPLACE the series (no stale duplicate); got %d series", n)
	}
	got := gather(t, c)
	if strings.Contains(got, "ssh_enabled=true") {
		t.Errorf("stale ssh_enabled=true series survived a value change:\n%s", got)
	}
	if !strings.Contains(got, "ssh_enabled=false") {
		t.Errorf("expected current ssh_enabled=false series:\n%s", got)
	}
}

func TestIdracObservedCollector_RemoveDropsSeries(t *testing.T) {
	store := newIdracObservedStore()
	c := newIdracObservedCollector(store)

	store.set(observedIdrac{server: "r09-u06", oobIP: "10.20.21.44", orbID: "colo:CFRHDX3",
		fields: map[string]*bool{"sshEnabled": ptr.To(true)}})
	if n := testutil.CollectAndCount(c); n != 1 {
		t.Fatalf("expected 1 series, got %d", n)
	}

	store.remove("r09-u06")
	if n := testutil.CollectAndCount(c); n != 0 {
		t.Errorf("expected 0 series after remove, got %d", n)
	}
}

// TestReconcileSuccessGauge pins the reconcile_success contract: success → 1,
// failure → 0, and skip/delete → ABSENT (never 0 — absence is how a skipped
// server stays out of `== 0` alerts).
func TestReconcileSuccessGauge(t *testing.T) {
	defer removeReconcileSuccess("gauge-ok")
	defer removeReconcileSuccess("gauge-fail")

	recordReconcileSuccess("gauge-ok", 1234567890)
	recordReconcileError("gauge-fail", "RedfishReadFailed")

	if v := testutil.ToFloat64(serverConfigReconcileSuccess.WithLabelValues("gauge-ok")); v != 1 {
		t.Errorf("gauge-ok = %v, want 1 (converged)", v)
	}
	if v := testutil.ToFloat64(serverConfigReconcileSuccess.WithLabelValues("gauge-fail")); v != 0 {
		t.Errorf("gauge-fail = %v, want 0 (failed)", v)
	}

	// skip/delete ⇒ series absent, not 0.
	removeReconcileSuccess("gauge-fail")
	if got := gather(t, serverConfigReconcileSuccess); strings.Contains(got, "server=gauge-fail") {
		t.Errorf("expected gauge-fail series absent after remove, got:\n%s", got)
	}
}

// A field the controller never observed (missing from the Redfish attrs, or not
// allowlisted) must render "unknown", not a false zero — and firmware, unset
// until firmware read is implemented, must render "unknown" too.
func TestIdracObservedCollector_UnknownForUnreadFields(t *testing.T) {
	store := newIdracObservedStore()
	c := newIdracObservedCollector(store)

	store.set(observedIdrac{
		server: "r09-u06", oobIP: "10.20.21.44", orbID: "colo:CFRHDX3",
		// firmwareVersion "" and only sshEnabled observed
		fields: map[string]*bool{"sshEnabled": ptr.To(true)},
	})

	got := gather(t, c)
	for _, want := range []string{
		"firmware_version=unknown",
		"ipmi_enabled=unknown",
		"racadm_enabled=unknown",
		"ssh_enabled=true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q\ngot:\n%s", want, got)
		}
	}
}

func TestObservedFieldsFromAttrs(t *testing.T) {
	attrs := map[string]any{
		attrSSHEnable:    "Disabled",
		attrIPMIEnable:   "Enabled",
		attrRacadmEnable: "Enabled",
	}
	got := observedFieldsFromAttrs(attrs, allFields())

	if got["sshEnabled"] == nil || *got["sshEnabled"] {
		t.Errorf("sshEnabled: want false, got %v", got["sshEnabled"])
	}
	if got["ipmiEnabled"] == nil || !*got["ipmiEnabled"] {
		t.Errorf("ipmiEnabled: want true, got %v", got["ipmiEnabled"])
	}

	// Field missing from attrs → absent from map → renders "unknown".
	delete(attrs, attrSSHEnable)
	got2 := observedFieldsFromAttrs(attrs, allFields())
	if _, present := got2["sshEnabled"]; present {
		t.Errorf("field missing from Redfish attrs must be absent (→unknown), got %v", got2["sshEnabled"])
	}
}

func TestCamelToSnake(t *testing.T) {
	cases := map[string]string{
		"sshEnabled":                  "ssh_enabled",
		"ipmiEnabled":                 "ipmi_enabled",
		"racadmEnabled":               "racadm_enabled",
		"osToIdracPassThroughEnabled": "os_to_idrac_pass_through_enabled",
		"firmwareVersion":             "firmware_version",
	}
	for in, want := range cases {
		if got := camelToSnake(in); got != want {
			t.Errorf("camelToSnake(%q) = %q, want %q", in, got, want)
		}
	}
}
