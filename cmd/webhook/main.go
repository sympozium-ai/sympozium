// Package main is the entry point for the Sympozium admission webhook server.
package main

import (
	"flag"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	ctrlwebhook "sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/controller"
	"github.com/sympozium-ai/sympozium/internal/webhook"
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
	decoder := admission.NewDecoder(scheme)

	hookServer.Register("/validate-agent-pods", &ctrlwebhook.Admission{
		Handler: &webhook.PolicyEnforcer{
			Client:  mgr.GetClient(),
			Log:     log.WithName("validator"),
			Decoder: decoder,
		},
	})

	hookServer.Register("/mutate-agent-pods", &ctrlwebhook.Admission{
		Handler: &webhook.MutatingPolicyEnforcer{
			Client:  mgr.GetClient(),
			Log:     log.WithName("mutator"),
			Decoder: decoder,
		},
	})

	// Register fitness pre-flight validation webhook (optional).
	if os.Getenv("LLMFIT_PREFLIGHT_VALIDATION") == "true" {
		natsURL := os.Getenv("NATS_URL")
		if natsURL != "" {
			densityCache := controller.NewDensityCache(90 * time.Second)
			densitySub := &controller.DensitySubscriber{
				NATSUrl: natsURL,
				Cache:   densityCache,
				Log:     log.WithName("density-subscriber"),
			}
			ctx := ctrl.SetupSignalHandler()
			go func() {
				if err := densitySub.Start(ctx); err != nil {
					log.Error(err, "density subscriber failed in webhook")
				}
			}()

			hookServer.Register("/validate-model-density", &ctrlwebhook.Admission{
				Handler: &webhook.ModelDensityValidator{
					Cache:   densityCache,
					Log:     log.WithName("density-validator"),
					Decoder: decoder,
				},
			})
			log.Info("Model fitness pre-flight validation webhook enabled")
		}
	}

	log.Info("starting webhook server")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "webhook server failed")
		os.Exit(1)
	}
}
