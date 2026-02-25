// Package main is the entry point for the Sympozium admission webhook server.
package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	ctrlwebhook "sigs.k8s.io/controller-runtime/pkg/webhook"

	sympoziumv1alpha1 "github.com/alexsjones/sympozium/api/v1alpha1"
	"github.com/alexsjones/sympozium/internal/webhook"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(sympoziumv1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var certDir string
	var webhookPort int

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8443", "Metrics bind address")
	flag.StringVar(&certDir, "cert-dir", "/tmp/k8s-webhook-server/serving-certs", "TLS cert directory")
	flag.IntVar(&webhookPort, "webhook-port", 9443, "Webhook server port")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	log := ctrl.Log.WithName("webhook")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		WebhookServer: ctrlwebhook.NewServer(ctrlwebhook.Options{
			Port:    webhookPort,
			CertDir: certDir,
		}),
	})
	if err != nil {
		log.Error(err, "unable to create manager")
		os.Exit(1)
	}

	// Register webhooks
	hookServer := mgr.GetWebhookServer()

	hookServer.Register("/validate-agent-pods", &ctrlwebhook.Admission{
		Handler: &webhook.PolicyEnforcer{
			Client: mgr.GetClient(),
			Log:    log.WithName("validator"),
		},
	})

	hookServer.Register("/mutate-agent-pods", &ctrlwebhook.Admission{
		Handler: &webhook.MutatingPolicyEnforcer{
			Client: mgr.GetClient(),
			Log:    log.WithName("mutator"),
		},
	})

	log.Info("starting webhook server")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "webhook server failed")
		os.Exit(1)
	}
}
