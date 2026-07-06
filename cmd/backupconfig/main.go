// backupconfig-controller — watches BackupConfig CRs and reconciles Velero
// Schedule CRDs + an etcd-backup CronJob in the same cluster. Mirror of
// serverconfig-controller for the backup domain.
package main

import (
	"flag"
	"os"
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
	"github.com/armada/configbundle/internal/backupconfig"
)

// Config holds all controller configuration. Defaults target a local minikube
// setup so `make run-controller` works without env-var soup. Production
// deploys override via ConfigMap/Deployment env. None of the defaults carry
// secrets — this controller talks only to the local K8s API.
type Config struct {
	// VeleroNamespace is where the controller writes Velero Schedule CRDs. The
	// Velero install conventionally lives in `velero` on the management cluster.
	VeleroNamespace string `envconfig:"VELERO_NAMESPACE" default:"velero"`

	// EtcdBackupNamespace is where the controller writes the etcd-backup CronJob.
	// `kube-system` is the conventional home for control-plane infrastructure.
	EtcdBackupNamespace string `envconfig:"ETCD_BACKUP_NAMESPACE" default:"kube-system"`

	// EtcdBackupImage is the container image the etcd-backup CronJob runs.
	// Placeholder until the dedicated snapshot image ships; swapped via the
	// kustomize images: block (or this env var) once the real image is built.
	EtcdBackupImage string `envconfig:"ETCD_BACKUP_IMAGE" default:"armadaeksatest.azurecr.io/etcd-snapshot:TBD"`

	// ObserveInterval is how often the controller re-polls Velero Schedule and
	// the etcd CronJob for each CR independent of CR spec changes. Drives
	// drift-detection metrics. Zero (the default, which keeps `go run` safe
	// for local dev) = event-driven only, no periodic poll. Production deploys
	// opt in via the K8s manifest — typical band is 1-5min.
	ObserveInterval time.Duration `envconfig:"BACKUP_OBSERVE_INTERVAL" default:"0s"`

	// MetricsBindAddress is the listen address for the Prometheus /metrics
	// endpoint. "0" disables it. Default :8096 avoids cb-controller (:8095) and
	// sc-controller (:8093) collisions.
	MetricsBindAddress string `envconfig:"METRICS_BIND_ADDRESS" default:":8096"`
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
	// Port 8094 — cb-controller uses 8091, sc-controller uses 8092; pick a free one.
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8094", "Health probe bind address.")
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

	setupLog.Info("backup target namespaces",
		"velero", cfg.VeleroNamespace,
		"etcd", cfg.EtcdBackupNamespace,
		"etcdImage", cfg.EtcdBackupImage)

	if cfg.ObserveInterval > 0 {
		setupLog.Info("drift-detection polling enabled", "interval", cfg.ObserveInterval)
	} else {
		setupLog.Info("drift-detection polling disabled (event-driven only); set BACKUP_OBSERVE_INTERVAL to enable")
	}

	if err := (&backupconfig.BackupConfigReconciler{
		Client:              mgr.GetClient(),
		Scheme:              mgr.GetScheme(),
		VeleroNamespace:     cfg.VeleroNamespace,
		EtcdBackupNamespace: cfg.EtcdBackupNamespace,
		EtcdBackupImage:     cfg.EtcdBackupImage,
		ObserveInterval:     cfg.ObserveInterval,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "backupconfig")
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
