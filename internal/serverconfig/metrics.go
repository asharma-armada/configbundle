package serverconfig

import (
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Operational metrics for the reconciler itself. Domain state (observed iDRAC
// settings) is surfaced separately by idracObservedCollector below, as the
// configbundle_serverconfig_status_idracsettings_info info metric.
var (
	// serverConfigReconcileTimestamp mirrors the bc-controller pattern:
	// updated on every successful reconcile; alerts fire when the
	// (time() - <this>) gap exceeds an expected cadence, catching a stuck /
	// dead sc-controller even when spec has not changed.
	serverConfigReconcileTimestamp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "configbundle_serverconfig_reconcile_timestamp_seconds",
		Help: "Unix timestamp of the last successful reconcile of this ServerConfig CR. Absent series = never reconciled.",
	}, []string{"server"})

	// serverConfigReconcileErrors counts reconcile failures per CR, labelled
	// by a bounded set of reason strings (MissingCredentials, CredentialsLoadFailed,
	// RedfishReadFailed, RedfishPatchFailed). Cloud rate() queries surface which
	// failure mode is dominant across the fleet.
	serverConfigReconcileErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "configbundle_serverconfig_reconciliation_errors_total",
		Help: "Cumulative reconcile failures per ServerConfig CR, labelled by failure reason.",
	}, []string{"server", "reason"})

	// serverConfigReconcileSuccess is the per-object "did the last reconcile
	// converge?" level: 1 = converged, 0 = failed. The series is ABSENT for a
	// deliberately-skipped server (not on the OOB allowlist / no OOB IP) or one
	// never reconciled — so `== 0` alerts fire only for genuinely-broken boxes,
	// never for skips. It is the load-bearing "which server is failing?" signal
	// for the cloud. Set from recordReconcileSuccess/recordReconcileError; the
	// series is deleted by removeReconcileSuccess on skip and on CR delete.
	// See docs/reference/DOMAIN-CONTROLLER.md §6.
	serverConfigReconcileSuccess = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "configbundle_serverconfig_reconcile_success",
		Help: "1 if this ServerConfig's last reconcile converged, 0 if it failed. Absent = deliberately skipped or never reconciled.",
	}, []string{"server"})
)

// idracObserved is the snapshot store the observed-state Collector renders on
// every scrape. The reconciler writes into it from its live Redfish read; the
// Collector reads it at scrape time. See idracObservedCollector below for why
// this is a Collector-over-snapshot rather than a mutated GaugeVec.
var idracObserved = newIdracObservedStore()

func init() {
	metrics.Registry.MustRegister(
		serverConfigReconcileTimestamp, serverConfigReconcileErrors, serverConfigReconcileSuccess,
		newIdracObservedCollector(idracObserved),
	)
}

// recordReconcileSuccess marks a successful reconcile: bumps the last-success
// timestamp and sets the reconcile_success level to 1 for this CR.
func recordReconcileSuccess(server string, now int64) {
	serverConfigReconcileTimestamp.With(prometheus.Labels{"server": server}).Set(float64(now))
	serverConfigReconcileSuccess.With(prometheus.Labels{"server": server}).Set(1)
}

// recordReconcileError increments the failure counter for the given reason and
// drops the reconcile_success level to 0 (the last reconcile did not converge).
// Reason strings should stay bounded — pick from a fixed enum, do not
// interpolate error messages.
func recordReconcileError(server, reason string) {
	serverConfigReconcileErrors.With(prometheus.Labels{"server": server, "reason": reason}).Inc()
	serverConfigReconcileSuccess.With(prometheus.Labels{"server": server}).Set(0)
}

// removeReconcileSuccess deletes a server's reconcile_success series so it
// becomes absent — called when the server is skipped (absent = skip, never 0)
// or its CR is deleted.
func removeReconcileSuccess(server string) {
	serverConfigReconcileSuccess.DeleteLabelValues(server)
}

// fieldAttrKey maps a CRD JSON field name to the Dell Redfish attribute key
// that holds its observed value. Keep in sync with computeIdracDeltas.
var fieldAttrKey = map[string]string{
	"sshEnabled":    attrSSHEnable,
	"racadmEnabled": attrRacadmEnable,
	"ipmiEnabled":   attrIPMIEnable,
}

// -----------------------------------------------------------------------------
// Observed-state info metric: configbundle_serverconfig_status_idracsettings_info
//
// One series per ServerConfig, value always 1, the observed iDRAC state carried
// entirely in labels — the "info metric" shape used by node-exporter's
// node_uname_info and the idrac_exporter. It answers the P0 question "surface
// each server's live iDRAC state in Grafana" as a single inventory row per box:
//
//   configbundle_serverconfig_status_idracsettings_info{
//     oob_ip="10.20.21.44", server="r09-u06", orb_id="colo:CFRHDX3",
//     firmware_version="7.20.10.05", ssh_enabled="false",
//     ipmi_enabled="true", racadm_enabled="true"} 1
//
// Why a Collector over a snapshot, NOT a mutated GaugeVec: every field value
// lives in a label, so when a value changes (firmware upgrade, ssh toggle) the
// label-set changes — i.e. it becomes a DIFFERENT series. A GaugeVec would keep
// exporting the old label-set at its last value forever (you'd see two rows,
// old and new, both =1) unless every stale tuple were explicitly Deleted. A
// Collector that rebuilds from current state each scrape sidesteps this: only
// the server's current row is ever emitted. This is exactly how the
// idrac_exporter avoids stale series.
//
// The metric is sourced from the reconciler's in-memory snapshot (fed by the
// same live Redfish read that drives the per-field gauges), NOT from the CR
// status subresource — so a failed status write can never blank the metric,
// per the project's "one live read, fan out to Prom and status independently"
// rule.
// -----------------------------------------------------------------------------

// observedIdrac is one server's observed iDRAC state, rendered as a single
// info-metric series. fields maps a CRD JSON field name (sshEnabled, …) to its
// observed boolean; a nil pointer (or absent key) renders as "unknown".
type observedIdrac struct {
	server          string // ServerConfig CR name
	oobIP           string
	orbID           string
	firmwareVersion string // observed; "" until firmware read is implemented
	fields          map[string]*bool
}

// idracObservedStore is a concurrency-safe latest-snapshot map keyed by CR name.
// The reconciler goroutine writes (set/remove); the scrape goroutine reads
// (snapshot). Only ever holds the current view — never historical.
type idracObservedStore struct {
	mu       sync.RWMutex
	byServer map[string]observedIdrac
}

func newIdracObservedStore() *idracObservedStore {
	return &idracObservedStore{byServer: map[string]observedIdrac{}}
}

func (s *idracObservedStore) set(o observedIdrac) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byServer[o.server] = o
}

func (s *idracObservedStore) remove(server string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byServer, server)
}

func (s *idracObservedStore) snapshot() []observedIdrac {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]observedIdrac, 0, len(s.byServer))
	for _, o := range s.byServer {
		out = append(out, o)
	}
	return out
}

// idracObservedCollector renders the store as configbundle_serverconfig_status_idracsettings_info.
// The variable-label list is identity labels + one label per KnownIdracFields
// entry, so adding a managed field to that registry automatically extends the
// metric's label set — no change needed here.
type idracObservedCollector struct {
	store     *idracObservedStore
	desc      *prometheus.Desc
	fieldKeys []string // KnownIdracFields, stable order — matches label order after the fixed identity labels
}

func newIdracObservedCollector(store *idracObservedStore) *idracObservedCollector {
	fieldKeys := append([]string(nil), KnownIdracFields...)
	labelKeys := []string{"oob_ip", "server", "orb_id", "firmware_version"}
	for _, f := range fieldKeys {
		labelKeys = append(labelKeys, camelToSnake(f))
	}
	return &idracObservedCollector{
		store:     store,
		fieldKeys: fieldKeys,
		desc: prometheus.NewDesc(
			"configbundle_serverconfig_status_idracsettings_info",
			"Observed iDRAC settings for a server, read live via Redfish. Value is always 1 (info metric); observed state is in the labels. One series per ServerConfig. Boolean fields are true|false|unknown; firmware_version is the observed firmware or 'unknown' until firmware read is implemented.",
			labelKeys, nil,
		),
	}
}

func (c *idracObservedCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

func (c *idracObservedCollector) Collect(ch chan<- prometheus.Metric) {
	for _, o := range c.store.snapshot() {
		vals := []string{o.oobIP, o.server, o.orbID, orUnknown(o.firmwareVersion)}
		for _, f := range c.fieldKeys {
			vals = append(vals, boolLabel(o.fields[f]))
		}
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, 1, vals...)
	}
}

// recordObservedIdracInfo replaces this server's observed-state snapshot. Called
// from the reconciler after its live Redfish read. observedFields is built from
// the same attribute map that feeds the per-field gauges.
func recordObservedIdracInfo(server, oobIP, orbID, firmwareVersion string, observedFields map[string]*bool) {
	idracObserved.set(observedIdrac{
		server:          server,
		oobIP:           oobIP,
		orbID:           orbID,
		firmwareVersion: firmwareVersion,
		fields:          observedFields,
	})
}

// removeObservedIdracInfo drops a server's series when its ServerConfig is
// deleted, so a deleted box stops showing up in the inventory metric.
func removeObservedIdracInfo(server string) {
	idracObserved.remove(server)
}

// observedFieldsFromAttrs projects the live Redfish attribute map into a
// {fieldName: *bool} map for the allowlisted, recognized fields. A field
// missing from the response (or non-string) is left absent → renders "unknown".
func observedFieldsFromAttrs(attrs map[string]any, allowed map[string]bool) map[string]*bool {
	out := make(map[string]*bool, len(KnownIdracFields))
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
		b := enableStrToBool(raw)
		out[field] = &b
	}
	return out
}

func boolLabel(p *bool) string {
	switch {
	case p == nil:
		return "unknown"
	case *p:
		return "true"
	default:
		return "false"
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// camelToSnake converts a CRD JSON field name (lowerCamel, e.g. "sshEnabled")
// to a Prometheus-conventional snake_case label key ("ssh_enabled").
func camelToSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r - 'A' + 'a')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
