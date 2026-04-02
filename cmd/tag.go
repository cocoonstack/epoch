package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/epoch/util"
)

func newTagCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tag <name>:<existing-tag> <name>:<new-tag>",
		Short: "Create a new tag for an existing snapshot",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			srcName, srcTag := util.ParseRef(args[0])
			dstName, dstTag := util.ParseRef(args[1])

			if dstName != srcName {
				return fmt.Errorf("cross-repository tagging not supported: %s vs %s", srcName, dstName)
			}

			client := newRegistryClient()
			m, err := client.GetManifestJSON(ctx, srcName, srcTag)
			if err != nil {
				return err
			}
			if err := client.PutManifestJSON(ctx, dstName, dstTag, m); err != nil {
				return err
			}

			fmt.Printf("Tagged %s:%s → %s:%s\n", srcName, srcTag, dstName, dstTag)
			return nil
		},
	}
}
