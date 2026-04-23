package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/epoch/utils"
)

func newRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <name>:<tag>",
		Short: "Remove a manifest tag from the registry",
		Long: `Removes the manifest at <name>:<tag> via OCI delete.
Blobs are NOT deleted (they may be shared with other tags).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name, tag := utils.ParseRef(args[0])
			client, err := newRegistryClient()
			if err != nil {
				return fmt.Errorf("create registry client: %w", err)
			}
			if err := client.DeleteManifest(ctx, name, tag); err != nil {
				return fmt.Errorf("delete manifest %s:%s: %w", name, tag, err)
			}
			fmt.Printf("Removed %s:%s\n", name, tag)
			return nil
		},
	}
}
