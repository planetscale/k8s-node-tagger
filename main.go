// main.go
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

const leaderElectionId = "node-label-controller"

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var labelKeysStr string
	var cloudProvider string
	var jsonLogs bool

	logger := ctrl.Log.WithName("main")

	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false, "Enable leader election.") // TODO: should be on by default?
	flag.StringVar(&labelKeysStr, "label-keys", "", "Comma-separated list of label keys to sync")
	flag.StringVar(&cloudProvider, "cloud", "", "Cloud provider (aws or gcp)")
	flag.BoolVar(&jsonLogs, "json", false, "Output logs in JSON format")
	flag.Parse()

	// setup logger. Use development mode by default or json output if --json is set
	var opts []zap.Opts
	opts = append(opts, zap.UseDevMode(!jsonLogs))
	if jsonLogs {
		opts = append(opts, zap.JSONEncoder())
	}
	ctrl.SetLogger(zap.New(opts...))

	// validate flags
	if labelKeysStr == "" {
		logger.Error(fmt.Errorf("label-keys is required"), "unable to start manager")
		os.Exit(1)
	}
	labels := strings.Split(labelKeysStr, ",")
	logger.Info("Label keys to sync", "labelKeys", labels)

	if cloudProvider != "aws" && cloudProvider != "gcp" {
		logger.Error(fmt.Errorf("cloud-provider must be either 'aws' or 'gcp'"), "unable to start manager")
		os.Exit(1)
	}

	cfg, err := ctrl.GetConfig()
	if err != nil {
		logger.Error(err, "unable to get kubeconfig")
		os.Exit(1)
	}

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		LeaderElection:   enableLeaderElection,
		LeaderElectionID: leaderElectionId,
	})
	if err != nil {
		logger.Error(err, "unable to start manager")
		os.Exit(1)
	}

	controller := &NodeLabelController{
		Client: mgr.GetClient(),
		Labels: labels,
		Cloud:  cloudProvider,
	}

	ctx := ctrl.SetupSignalHandler()

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
