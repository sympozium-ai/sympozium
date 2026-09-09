package main

import (
	"fmt"
	"github.com/spf13/cobra"
	"github.com/sympozium-ai/sympozium/internal/cellninstall"
)

func newCellnNativeInstallCmd() *cobra.Command {
	var o cellninstall.Options
	var approve bool
	cmd := &cobra.Command{Use: "install-native", Short: "Install an operator-reviewed native starter catalogue and all three grant layers; submits no runs.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if !approve {
			return fmt.Errorf("explicit --approve-starter-tools required: grants include run-owned read/write and bounded example.com HTTPS")
		}
		o.Namespace = namespace
		if err := cellninstall.Install(cmd.Context(), k8sClient, o); err != nil {
			return err
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "Installed catalogue and grants in %s; private controller/preview/run configuration in %s. No run submitted; execution readiness remains unverified.\n", o.Namespace, o.OutputDir)
		return err
	}}
	cmd.Flags().StringVar(&o.ConfigurationDir, "configuration-dir", "", "Absolute operator configuration from celln starter-configure")
	cmd.Flags().StringVar(&o.OutputDir, "output-dir", "", "New absolute private output directory")
	cmd.Flags().StringVar(&o.StatePath, "state-path", "", "Existing dedicated host state path")
	cmd.Flags().StringVar(&o.OwnerTarget, "owner-target", "", "Stable verified HTTPS owner origin")
	cmd.Flags().StringVar(&o.Scope, "scope", "", "Stable installation identity; never change to renew consumed authority")
	cmd.Flags().StringVar(&o.PackageHash, "reviewed-package-hash", "", "Exact operator-approved package BLAKE3 identity")
	cmd.Flags().StringVar(&o.ControllerNamespace, "controller-namespace", "sympozium-system", "General controller namespace for partition preflight")
	cmd.Flags().BoolVar(&approve, "approve-starter-tools", false, "Explicitly approve all three bounded starter tool grants at operator/runtime/agent layers")
	for _, flag := range []string{"configuration-dir", "output-dir", "state-path", "owner-target", "scope", "reviewed-package-hash"} {
		_ = cmd.MarkFlagRequired(flag)
	}
	return cmd
}
