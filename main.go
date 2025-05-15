// db-query-operator/main.go
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"strings"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	webhookserver "sigs.k8s.io/controller-runtime/pkg/webhook" // Corrected import path

	databasev1alpha1 "github.com/konnektr-io/db-query-operator/api/v1alpha1" // Adjust import path
	"github.com/konnektr-io/db-query-operator/internal/controller"           // Adjust import path
	"github.com/konnektr-io/db-query-operator/internal/util"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")

	gvkPattern string
	registeredGVKs        []schema.GroupVersionKind
)

func parseWatchedGVKs(pattern string) ([]schema.GroupVersionKind, error) {
	var gvks []schema.GroupVersionKind
	if pattern == "" {
		return gvks, nil
	}
	for _, entry := range strings.Split(pattern, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, "/")
		if len(parts) != 2 {
			setupLog.Error(fmt.Errorf("invalid GVK pattern: %s", entry), "Skipping invalid GVK entry")
			continue
		}
		groupVersion := parts[0]
		kind := parts[1]
		gvParts := strings.SplitN(groupVersion, ".", 2)
		group := ""
		version := groupVersion
		if len(gvParts) == 2 {
			group = gvParts[0]
			version = gvParts[1]
		}
		if version == "" || kind == "" {
			setupLog.Error(fmt.Errorf("empty version or kind in GVK: %s", entry), "Skipping invalid GVK entry")
			continue
		}
		gvk := schema.GroupVersionKind{Group: group, Version: version, Kind: kind}
		gvks = append(gvks, gvk)
	}
	if len(gvks) == 0 {
		return nil, fmt.Errorf("no valid watched GVKs specified")
	}
	return gvks, nil
}

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(databasev1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool

	// Set gvkPattern default from env, allow override by flag
	gvkPattern = os.Getenv("GVK_PATTERN")
	flag.StringVar(&gvkPattern, "gvk-pattern", gvkPattern, "Semicolon-separated list of GVKs to watch, e.g. 'v1/Service;v1/ConfigMap;apps/v1/Deployment;networking.k8s.io/v1/Ingress'")

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", false,
		"If set the metrics endpoint is served securely")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	opts := zap.Options{
		Development: true, // Use true for more verbose logs during development
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/kubernetes/kubernetes/issues/121504
	// - https://blog.cloudflare.com/technical-breakdown-http2-rapid-reset-ddos-attack/
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	tlsOpts := []func(*tls.Config){}
	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	webhookServer := webhookserver.NewServer(webhookserver.Options{ // Corrected type name
		TLSOpts: tlsOpts,
	})

	// Parse watched GVKs from flag or env
	var err error
	registeredGVKs, err = util.ParseGVKs(gvkPattern)
	if err != nil {
		setupLog.Error(err, "invalid watched-gvk-patterns")
		os.Exit(1)
	}
	setupLog.Info("Watching GVKs", "gvks", registeredGVKs)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:   metricsAddr,
			SecureServing: secureMetrics,
			TLSOpts:       tlsOpts,
		},
		WebhookServer:          webhookServer, // Pass the server instance
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "dbqueryoperator.konnektr.io",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&controller.DatabaseQueryResourceReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Log:    ctrl.Log.WithName("controllers").WithName("DatabaseQueryResource"),
	}).SetupWithManagerAndGVKs(mgr, registeredGVKs); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "DatabaseQueryResource")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
