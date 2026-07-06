// serverconfig-controller — watches ServerConfig CRs and reconciles iDRAC
// settings via Redfish on whitelisted BMCs.
package main

import (
	"flag"
	"os"
	"strings"
	"time"

	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"github.com/kelseyhightower/envconfig"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	armadav1 "github.com/armada/configbundle/api/v1"
	"github.com/armada/configbundle/internal/serverconfig"
)

// Config holds all controller configuration. Defaults target the prototype
// lab setup so `make run-controller` works without env-var soup. Production
// deploys override via ConfigMap/Deployment env. None of the defaults carry
// secrets — credentials live exclusively in the K8s Secret named by
// IDRAC_CREDENTIALS_SECRET.
type Config struct {
	// OobAllowlist is the comma-separated list of OOB IPs the controller may
	// reconcile against. Empty = controller reconciles NOTHING. Default points
	// to the prototype dev iDRAC. This is the primary blast-radius control —
	// CRs targeting other IPs are short-circuited regardless of namespace.
	OobAllowlist string `envconfig:"IDRAC_OOB_ALLOWLIST" default:"10.20.21.44"`

	// FieldAllowlist is the comma-separated list of CRD field names (JSON tag
	// form) the controller may PATCH. Empty = no fields managed. Field names
	// must appear in serverconfig.KnownIdracFields; unknown names log a warning
	// at startup and are ignored.
	FieldAllowlist string `envconfig:"IDRAC_FIELD_ALLOWLIST" default:"sshEnabled,racadmEnabled,ipmiEnabled"`

	// CredentialsNamespace + CredentialsSecretName name the K8s Secret that
	// carries iDRAC basic-auth credentials. Secret data keys must be
	// `username` and `password`.
	CredentialsNamespace  string `envconfig:"IDRAC_CREDENTIALS_NAMESPACE" default:"default"`
	CredentialsSecretName string `envconfig:"IDRAC_CREDENTIALS_SECRET"    default:"idrac-credentials"`

	// ObserveInterval is how often the controller re-polls iDRAC for each CR
	// independent of CR spec changes. Drives drift-detection metrics. Zero
	// (the default, which keeps `go run` safe for local dev) = event-driven
	// only, no periodic poll. Production deploys opt in via the K8s manifest
	// — typical band is 1-5min; tighter intervals add load on iDRAC firmware.
	ObserveInterval time.Duration `envconfig:"IDRAC_OBSERVE_INTERVAL" default:"0s"`

	// MetricsBindAddress is the listen address for the Prometheus /metrics
	// endpoint. "0" disables it. Default :8093 sits next to the health probe
	// on :8092 — neither conflicts with cb-controller's :8091/:8095.
	MetricsBindAddress string `envconfig:"METRICS_BIND_ADDRESS" default:":8093"`
}

// parseAllowlist splits a comma-separated string into a set, trimming
// whitespace and dropping empties. Used for both OOB IPs and field names.
func parseAllowlist(raw string) map[string]bool {
	out := map[string]bool{}
	for _, p := range strings.Split(raw, ",") {
		v := strings.TrimSpace(p)
		if v != "" {
			out[v] = true
		}
	}
	return out
}

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(armadav1.AddToScheme(scheme))
}

func main() {
	var probeAddr string
	// Port 8092 — cb-controller uses 8091 for probe + 8095 for dispatch; pick a free one.
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8092", "Health probe bind address.")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		setupLog.Error(err, "Failed to load config")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: cfg.MetricsBindAddress},
		HealthProbeBindAddress: probeAddr,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	oobAllowlist := parseAllowlist(cfg.OobAllowlist)
	if len(oobAllowlist) == 0 {
		setupLog.Info("WARNING: IDRAC_OOB_ALLOWLIST is empty — controller will reconcile NOTHING. Set the env var to a comma-separated list of OOB IPs.")
	} else {
		ips := make([]string, 0, len(oobAllowlist))
		for ip := range oobAllowlist {
			ips = append(ips, ip)
		}
		setupLog.Info("iDRAC OOB allowlist loaded", "count", len(oobAllowlist), "ips", ips)
	}

	fieldAllowlist := parseAllowlist(cfg.FieldAllowlist)
	if len(fieldAllowlist) == 0 {
		setupLog.Info("WARNING: IDRAC_FIELD_ALLOWLIST is empty — controller will PATCH NO fields. Set the env var to a comma-separated list of CRD field names, e.g. sshEnabled,racadmEnabled.")
	} else {
		fields := make([]string, 0, len(fieldAllowlist))
		for f := range fieldAllowlist {
			fields = append(fields, f)
		}
		setupLog.Info("iDRAC field allowlist loaded", "count", len(fieldAllowlist), "fields", fields)
	}
	if unknown := serverconfig.UnknownAllowlistEntries(fieldAllowlist); len(unknown) > 0 {
		setupLog.Info("WARNING: IDRAC_FIELD_ALLOWLIST contains entries this controller does not recognize — they will be ignored. Check spelling against IdracSpec JSON tags.",
			"unknown", unknown, "known", serverconfig.KnownIdracFields)
	}

	setupLog.Info("iDRAC credentials Secret", "namespace", cfg.CredentialsNamespace, "name", cfg.CredentialsSecretName)

	if cfg.ObserveInterval > 0 {
		setupLog.Info("drift-detection polling enabled", "interval", cfg.ObserveInterval)
	} else {
		setupLog.Info("drift-detection polling disabled (event-driven only); set IDRAC_OBSERVE_INTERVAL to enable")
	}

	if err := (&serverconfig.ServerConfigReconciler{
		Client:                mgr.GetClient(),
		Scheme:                mgr.GetScheme(),
		AllowedOobIPs:         oobAllowlist,
		AllowedFields:         fieldAllowlist,
		CredentialsNamespace:  cfg.CredentialsNamespace,
		CredentialsSecretName: cfg.CredentialsSecretName,
		ObserveInterval:       cfg.ObserveInterval,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "serverconfig")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager", "probe", probeAddr, "metrics", cfg.MetricsBindAddress)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
