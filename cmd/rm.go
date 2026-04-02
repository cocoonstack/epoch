package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/epoch/internal/util"
)

func newRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <name>:<tag>",
		Short: "Remove a snapshot tag from the registry",
		Long: `Removes the manifest for the given name:tag.
Blobs are NOT deleted (they may be shared by other tags).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name, tag := util.ParseRef(args[0])
			client := newRegistryClient()
			if err := client.Delete(ctx, fmt.Sprintf("/repositories/%s/tags/%s", name, tag)); err != nil {
				return err
			}
			fmt.Printf("Removed %s:%s\n", name, tag)
			return nil
		},
	}
}
