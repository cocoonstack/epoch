package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var flagRootDir string

// NewRootCmd creates the root cobra command for epoch.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "epoch",
		Short: "Epoch — Cocoon VM snapshot registry backed by S3-compatible object storage",
		Long: `Epoch is a Harbor-like registry for Cocoon MicroVM snapshots.

CLI commands talk to the Epoch HTTP server (default http://127.0.0.1:4300).
Set EPOCH_SERVER and EPOCH_REGISTRY_TOKEN environment variables.`,
		SilenceUsage: true,
	}

	root.PersistentFlags().StringVar(&flagRootDir, "root-dir", "/var/lib/cocoon", "Cocoon root directory (for push/pull local snapshot data)")

	root.AddCommand(
		newPushCmd(),
		newPullCmd(),
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
