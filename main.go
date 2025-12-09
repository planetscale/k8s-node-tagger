// main.go
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

const (
	leaderElectionID = "node-label-controller"
	cacheSyncPeriod  = 4 * time.Hour
)

func main() {
	var probesAddr string
	var metricsAddr string
	var pprofAddr string
	var enableLeaderElection bool
	var labelsStr string
	var annotationsStr string
	var cloudProvider string

	flag.StringVar(&probesAddr, "probes-addr", ":8080", "The address the /readyz and /healthz probes endpoint binds to.")
	flag.StringVar(&metricsAddr, "metrics-addr", ":8081", "The address the metric endpoint binds to.")
	flag.StringVar(&pprofAddr, "pprof-addr", "", "The address the pprof server endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false, "Enable leader election.")
	flag.StringVar(&labelsStr, "labels", "", "Comma-separated list of label keys to sync")
	flag.StringVar(&annotationsStr, "annotations", "", "Comma-separated list of annotation keys to sync")
	flag.StringVar(&cloudProvider, "cloud", "", "Cloud provider (aws or gcp)")

	// Add zap logger flags (--zap-log-level, --zap-stacktrace-level, --zap-encoder, --zap-devel)
	zapOpts := zap.Options{}
	zapOpts.BindFlags(flag.CommandLine)

	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	logger := ctrl.Log.WithName("main")

	// validate flags
	if labelsStr == "" && annotationsStr == "" {
		logger.Error(fmt.Errorf("either --labels or --annotations is required"), "unable to start manager")
		os.Exit(1)
	}

	var labels []string
	if labelsStr != "" {
		labels = strings.Split(labelsStr, ",")
		logger.Info("Label keys to sync", "labelKeys", labels)
	}

	var annotations []string
	if annotationsStr != "" {
		annotations = strings.Split(annotationsStr, ",")
		logger.Info("Annotation keys to sync", "annotationKeys", annotations)
	}

	if cloudProvider != "aws" && cloudProvider != "gcp" {
		logger.Error(fmt.Errorf("cloud-provider must be either 'aws' or 'gcp'"), "unable to start manager")
		os.Exit(1)
	}

	// get a kubeconfig for the manager to use to access the k8s API:
	cfg, err := ctrl.GetConfig()
	if err != nil {
		logger.Error(err, "unable to get kubeconfig")
		os.Exit(1)
	}

	// configure the controller-runtime manager
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: probesAddr,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		PprofBindAddress: pprofAddr,
		LeaderElection:   enableLeaderElection,
		LeaderElectionID: leaderElectionID,
		Cache: cache.Options{
			SyncPeriod: ptr.To(cacheSyncPeriod),
		},
	})
	if err != nil {
		logger.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// setup /healthz and /readyz checks on the manager
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()

	// setup our controller and start it
	controller := &NodeLabelController{
		Client:      mgr.GetClient(),
		Logger:      ctrl.Log.WithName("controller"),
		Labels:      labels,
		Annotations: annotations,
		Cloud:       cloudProvider,
	}

	if err := controller.SetupCloudProvider(ctx); err != nil {
		logger.Error(err, "unable to setup cloud provider")
		os.Exit(1)
	}

	if err = controller.SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to create controller")
		os.Exit(1)
	}

	logger.Info("starting")
	if err := mgr.Start(ctx); err != nil {
		logger.Error(err, "problem starting manager")
		os.Exit(1)
	}
}
