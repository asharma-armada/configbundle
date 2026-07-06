package serverconfig

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	armadav1 "github.com/armada/configbundle/api/v1"
)

// Gauges expose the intended and observed value of every allowlisted iDRAC
// field so an external observer (Prometheus + Grafana) can detect drift
// without scraping CR status. Values are 0 for disabled, 1 for enabled —
// matching iDRAC's Enabled/Disabled string toggles. Drift = `intent != observed`
// in PromQL; that's the only computation a dashboard needs.
//
// Labels:
//   - server : the ServerConfig CR name (one per physical box)
//   - oobIP  : the iDRAC management IP (lets you filter without joining on CR name)
//   - field  : the JSON tag name from IdracSpec (sshEnabled, racadmEnabled, ipmiEnabled, …)
//
// Cardinality stays bounded: ~100 servers × ~3 fields = a few hundred series
// per Galleon. Adding new fields requires extending fieldAttrKey below.
var (
	idracFieldIntent = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "armada_idrac_field_intent",
		Help: "Intended value of an allowlisted iDRAC field from the ServerConfig CR (0=disabled, 1=enabled). Absent series mean no intent set on the CR.",
	}, []string{"server", "oobIP", "field"})

	idracFieldObserved = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "armada_idrac_field_observed",
		Help: "Live value of an allowlisted iDRAC field read via Redfish (0=disabled, 1=enabled). Updated every reconcile (event-driven + periodic poll).",
	}, []string{"server", "oobIP", "field"})

	// idracFieldIgnored is 1 when the parent ConfigBundle has an IgnoredEntry
	// for {serverOrbID, field}, else the series is absent. Lets alerting
	// suppress drift alerts on fields the cloud admin has resolved as Ignored:
	//
	//   armada_idrac_field_intent != on(server, field) armada_idrac_field_observed
	//     unless armada_idrac_field_ignored == 1
	idracFieldIgnored = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "armada_idrac_field_ignored",
		Help: "1 when the parent ConfigBundle's spec.ignored[] lists this {server, field}; absent otherwise. Used by alert rules to suppress drift alerts on admin-overridden fields.",
	}, []string{"server", "oobIP", "field"})
)

func init() {
	metrics.Registry.MustRegister(idracFieldIntent, idracFieldObserved, idracFieldIgnored)
}

// fieldAttrKey maps a CRD JSON field name to the Dell Redfish attribute key
// that holds its observed value. Keep in sync with computeIdracDeltas.
var fieldAttrKey = map[string]string{
	"sshEnabled":    attrSSHEnable,
	"racadmEnabled": attrRacadmEnable,
	"ipmiEnabled":   attrIPMIEnable,
}

// fieldIntentPtr returns the pointer to the spec field for the given JSON name.
// Returns nil when the field name isn't recognized — caller skips.
func fieldIntentPtr(spec armadav1.IdracSpec, field string) *bool {
	switch field {
	case "sshEnabled":
		return spec.SSHEnabled
	case "racadmEnabled":
		return spec.RacadmEnabled
	case "ipmiEnabled":
		return spec.IPMIEnabled
	}
	return nil
}

func boolGauge(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// recordIntent updates the intent gauge for every allowlisted field. Fields
// with no intent on the CR (nil pointer) have their series deleted so
// PromQL queries don't see stale values after an admin release.
func recordIntent(server, oobIP string, spec armadav1.IdracSpec, allowed map[string]bool) {
	for _, field := range KnownIdracFields {
		if !allowed[field] {
			continue
		}
		labels := prometheus.Labels{"server": server, "oobIP": oobIP, "field": field}
		if p := fieldIntentPtr(spec, field); p != nil {
			idracFieldIntent.With(labels).Set(boolGauge(*p))
		} else {
			idracFieldIntent.Delete(labels)
		}
	}
}

// recordObserved updates the observed gauge for every allowlisted field
// using the live Redfish attribute map. A field missing from the response or
// carrying a non-string value is skipped (no series update) so transient
// firmware quirks don't flap the gauge.
func recordObserved(server, oobIP string, attrs map[string]any, allowed map[string]bool) {
	for _, field := range KnownIdracFields {
		if !allowed[field] {
			continue
		}
		key, ok := fieldAttrKey[field]
		if !ok {
			continue
		}
		raw, ok := attrs[key].(string)
		if !ok {
			continue
		}
		labels := prometheus.Labels{"server": server, "oobIP": oobIP, "field": field}
		idracFieldObserved.With(labels).Set(boolGauge(enableStrToBool(raw)))
	}
}

// recordIgnored sets the ignored gauge to 1 for every {server, field} present
// in the ignoredFields set (built from the parent ConfigBundle's spec.ignored[]
// entries that match this server's OrbID). Fields outside the set have their
// series deleted so the gauge doesn't go stale after the cloud admin reverses
// an Ignore decision.
func recordIgnored(server, oobIP string, ignoredFields map[string]bool, allowed map[string]bool) {
	for _, field := range KnownIdracFields {
		if !allowed[field] {
			continue
		}
		labels := prometheus.Labels{"server": server, "oobIP": oobIP, "field": field}
		if ignoredFields[field] {
			idracFieldIgnored.With(labels).Set(1)
		} else {
			idracFieldIgnored.Delete(labels)
		}
	}
}
