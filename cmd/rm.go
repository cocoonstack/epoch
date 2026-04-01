package cmd

import (
	"fmt"
	"net/http"

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
			serverURL, token := resolveConfig()
			client := newRegistryClient()

			req, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/api/repositories/%s/tags/%s", serverURL, name, tag), nil)
			if err != nil {
				return fmt.Errorf("new request: %w", err)
			}
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			_ = resp.Body.Close()
			if resp.StatusCode >= 400 {
				return fmt.Errorf("delete %s:%s: %d", name, tag, resp.StatusCode)
			}
			fmt.Printf("Removed %s:%s\n", name, tag)
			return nil
		},
	}
}
