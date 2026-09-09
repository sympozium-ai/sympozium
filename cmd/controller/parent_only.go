package main

import (
	"os"

	"github.com/sympozium-ai/sympozium/internal/cellnparent"
	"github.com/sympozium-ai/sympozium/internal/controller"
	"github.com/sympozium-ai/sympozium/internal/eventbus"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
)

// Separate startup branch: no pod builder, channel/schedule controllers,
// cluster inventory or background cleanup reconcilers are registered.
func runParentOnly(manager ctrl.Manager, natsURL string) error {
	ctx := ctrl.SetupSignalHandler()
	run := &controller.AgentRunReconciler{ParentOnly: true, Client: manager.GetClient(), APIReader: manager.GetAPIReader(), Scheme: manager.GetScheme(), Log: ctrl.Log.WithName("celln-parent"), ParentConfigPath: os.Getenv("CELLN_PARENT_CONFIG")}
	dispatcher, err := cellnparent.LoadRegistrationDispatcher(os.Getenv("CELLN_PARENT_REGISTRATIONS"), run.ParentConfigPath, manager.GetAPIReader())
	if err != nil {
		return err
	}
	run.ParentAdmission = dispatcher
	if natsURL == "" {
		natsURL = os.Getenv("NATS_URL")
	}
	if natsURL != "" {
		bus, err := eventbus.NewNATSEventBusWithContext(ctx, natsURL)
		if err != nil {
			return err
		}
		defer bus.Close()
		run.EventBus = bus
	}
	if err := run.SetupWithManager(manager); err != nil {
		return err
	}
	turn := &controller.AgentRunTurnReconciler{Client: manager.GetClient(), APIReader: manager.GetAPIReader(), ParentConfigPath: run.ParentConfigPath}
	if err := turn.SetupWithManager(manager); err != nil {
		return err
	}
	if err := manager.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := manager.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}
	return manager.Start(ctx)
}
