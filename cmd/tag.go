package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/epoch/internal/util"
)

const defaultServerURL = "http://127.0.0.1:4300"

func newTagCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tag <name>:<existing-tag> <name>:<new-tag>",
		Short: "Create a new tag for an existing snapshot",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			srcName, srcTag := util.ParseRef(args[0])
			dstName, dstTag := util.ParseRef(args[1])

			if dstName != srcName {
				return fmt.Errorf("cross-repository tagging not supported: %s vs %s", srcName, dstName)
			}

			serverURL := os.Getenv("EPOCH_SERVER")
			if serverURL == "" {
				serverURL = defaultServerURL
			}
			token := os.Getenv("EPOCH_REGISTRY_TOKEN")

			// Get existing manifest via V2 API.
			getURL := fmt.Sprintf("%s/v2/%s/manifests/%s", serverURL, srcName, srcTag)
			req, _ := http.NewRequest("GET", getURL, nil)
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != 200 {
				return fmt.Errorf("get %s:%s: %d", srcName, srcTag, resp.StatusCode)
			}
			var m json.RawMessage
			if decErr := json.NewDecoder(resp.Body).Decode(&m); decErr != nil {
				return fmt.Errorf("decode manifest %s:%s: %w", srcName, srcTag, decErr)
			}

			// Re-push with new tag.
			putURL := fmt.Sprintf("%s/v2/%s/manifests/%s", serverURL, dstName, dstTag)
			putReq, _ := http.NewRequest("PUT", putURL, bytes.NewReader(m))
			putReq.Header.Set("Content-Type", "application/vnd.epoch.manifest.v1+json")
			if token != "" {
				putReq.Header.Set("Authorization", "Bearer "+token)
			}
			putResp, err := http.DefaultClient.Do(putReq)
			if err != nil {
				return err
			}
			_ = putResp.Body.Close()
			if putResp.StatusCode >= 400 {
				return fmt.Errorf("put %s:%s: %d", dstName, dstTag, putResp.StatusCode)
			}

			fmt.Printf("Tagged %s:%s → %s:%s\n", srcName, srcTag, dstName, dstTag)
			return nil
		},
	}
}
