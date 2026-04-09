package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/utils"
)

func newTagCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tag <name>:<existing-tag> <name>:<new-tag>",
		Short: "Create a new tag for an existing manifest",
		Long: `Re-uploads the manifest at <name>:<existing-tag> under <name>:<new-tag>.
Cross-repository tagging is not supported (the source and destination
repository names must match).`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			srcName, srcTag := utils.ParseRef(args[0])
			dstName, dstTag := utils.ParseRef(args[1])

			if dstName != srcName {
				return fmt.Errorf("cross-repository tagging not supported: %s vs %s", srcName, dstName)
			}

			client := newRegistryClient()
			data, contentType, err := client.GetManifest(ctx, srcName, srcTag)
			if err != nil {
				return fmt.Errorf("get manifest %s:%s: %w", srcName, srcTag, err)
			}
			if contentType == "" {
				contentType = manifest.MediaTypeOCIManifest
			}
			if err := client.PutManifest(ctx, dstName, dstTag, data, contentType); err != nil {
				return fmt.Errorf("put manifest %s:%s: %w", dstName, dstTag, err)
			}

			fmt.Printf("Tagged %s:%s -> %s:%s\n", srcName, srcTag, dstName, dstTag)
			return nil
		},
	}
}
