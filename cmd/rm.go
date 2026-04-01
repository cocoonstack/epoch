package cmd

import (
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

func newRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <name>:<tag>",
		Short: "Remove a snapshot tag from the registry",
		Long: `Removes the manifest for the given name:tag.
Blobs are NOT deleted (they may be shared by other tags).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, tag := parseRef(args[0])
			serverURL := os.Getenv("EPOCH_SERVER")
			if serverURL == "" {
				serverURL = "http://127.0.0.1:4300"
			}
			token := os.Getenv("EPOCH_REGISTRY_TOKEN")

			req, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/api/repositories/%s/tags/%s", serverURL, name, tag), nil)
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			resp.Body.Close()
			if resp.StatusCode >= 400 {
				return fmt.Errorf("delete %s:%s: %d", name, tag, resp.StatusCode)
			}
			fmt.Printf("Removed %s:%s\n", name, tag)
			return nil
		},
	}
}
