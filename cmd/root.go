package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/projecteru2/core/log"
	"github.com/projecteru2/core/types"
	"github.com/spf13/cobra"

	"github.com/cocoonstack/epoch/internal/util"
)

var flagRootDir string

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "epoch",
		Short: "Epoch — Cocoon VM snapshot registry backed by S3-compatible object storage",
		Long: `Epoch is a Harbor-like registry for Cocoon MicroVM snapshots.

CLI commands talk to the Epoch HTTP server (default http://127.0.0.1:4300).
Set EPOCH_SERVER and EPOCH_REGISTRY_TOKEN environment variables.`,
		SilenceUsage: true,
	}

	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		level := util.FirstNonEmpty(os.Getenv("EPOCH_LOG_LEVEL"), "info")
		return log.SetupLog(ctx, &types.ServerLogConfig{Level: level}, "")
	}

	root.PersistentFlags().StringVar(&flagRootDir, "root-dir", "/data01/cocoon", "Cocoon root directory (for push/pull local snapshot data)")

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

func Execute() {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
