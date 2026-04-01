package cmd

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/epoch/internal/util"
)

const defaultServerURL = "http://127.0.0.1:4300"

// resolveConfig returns the server URL and token from environment variables.
func resolveConfig() (serverURL, token string) {
	serverURL = os.Getenv("EPOCH_SERVER")
	if serverURL == "" {
		serverURL = defaultServerURL
	}
	token = os.Getenv("EPOCH_REGISTRY_TOKEN")
	return
}

// newRegistryClient returns an HTTP client configured for talking to the Epoch registry.
func newRegistryClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // registry may use self-signed certs
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

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

			serverURL, token := resolveConfig()
			client := newRegistryClient()

			// Get existing manifest via V2 API.
			getURL := fmt.Sprintf("%s/v2/%s/manifests/%s", serverURL, srcName, srcTag)
			req, err := http.NewRequest("GET", getURL, nil)
			if err != nil {
				return fmt.Errorf("new request GET manifest: %w", err)
			}
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			resp, err := client.Do(req)
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
			putReq, err := http.NewRequest("PUT", putURL, bytes.NewReader(m))
			if err != nil {
				return fmt.Errorf("new request PUT manifest: %w", err)
			}
			putReq.Header.Set("Content-Type", "application/vnd.epoch.manifest.v1+json")
			if token != "" {
				putReq.Header.Set("Authorization", "Bearer "+token)
			}
			putResp, err := client.Do(putReq)
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
