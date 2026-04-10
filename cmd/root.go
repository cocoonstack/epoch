package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// NewRootCmd creates the root cobra command for epoch.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "epoch",
		Short: "Epoch — generic OCI registry for cocoonstack artifacts (snapshots, cloud images, container images)",
		Long: `Epoch is an OCI Distribution-compatible registry that stores three kinds of
cocoonstack artifacts side by side:

  - Container images       — pushed/pulled by oras, crane, docker
  - OCI cloud images       — disk-only artifacts, e.g. ghcr.io/cocoonstack/windows/win11:25h2
  - OCI VM snapshots       — cocoon snapshots packaged as OCI artifacts

CLI commands talk to the Epoch HTTP server (default http://127.0.0.1:8080).
Set EPOCH_SERVER and EPOCH_REGISTRY_TOKEN environment variables.`,
		SilenceUsage: true,
	}

	root.AddCommand(
		newPushCmd(),
		newPullCmd(),
		newGetCmd(),
		newLsCmd(),
		newTagCmd(),
		newRmCmd(),
		newInspectCmd(),
		newServeCmd(),
	)
	return root
}

// Execute runs the root command.
func Execute() {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
