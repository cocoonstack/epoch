package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/projecteru2/core/log"
	"github.com/spf13/cobra"

	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/utils"
)

func newInspectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <name>[:<tag>]",
		Short: "Show OCI manifest details for an artifact",
		Long: `Fetch the raw OCI manifest for the given artifact and pretty-print it.
Also reports the classified kind (snapshot / cloud-image / container-image / unknown).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			logger := log.WithFunc("cmd.inspect")
			name, tag := utils.ParseRef(args[0])

			client, err := newRegistryClient()
			if err != nil {
				return err
			}
			raw, contentType, err := client.GetManifest(ctx, name, tag)
			if err != nil {
				return fmt.Errorf("get manifest %s:%s: %w", name, tag, err)
			}

			kind, classifyErr := manifest.Classify(raw)
			if classifyErr != nil {
				logger.Warnf(ctx, "classify: %v", classifyErr)
			}
			logger.Infof(ctx, "kind:        %s", kind)
			logger.Infof(ctx, "contentType: %s", contentType)
			logger.Info(ctx, "manifest:")

			var pretty bytes.Buffer
			if indentErr := json.Indent(&pretty, raw, "", "  "); indentErr != nil {
				// Fall back to raw bytes if the manifest is not valid JSON.
				logger.Warnf(ctx, "indent: %v", indentErr)
				_, _ = os.Stdout.Write(raw)
				return nil
			}
			_, _ = os.Stdout.Write(pretty.Bytes())
			fmt.Println()
			return nil
		},
	}
}
