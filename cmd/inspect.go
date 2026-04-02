package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/epoch/util"
)

func newInspectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <name>[:<tag>]",
		Short: "Show manifest details for a snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, tag := util.ParseRef(args[0])
			client := newRegistryClient()

			var m any
			if err := client.GetJSON(cmd.Context(), fmt.Sprintf("/repositories/%s/tags/%s", name, tag), &m); err != nil {
				return err
			}

			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(m)
		},
	}
}
