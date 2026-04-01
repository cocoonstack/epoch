package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/epoch/cocoon"
)

func newPushCmd() *cobra.Command {
	var tag string

	cmd := &cobra.Command{
		Use:   "push <snapshot-name>",
		Short: "Push a Cocoon snapshot to Epoch registry server",
		Long: `Upload a local Cocoon snapshot to the Epoch registry via HTTP.

Each file is hashed and uploaded as a blob via PUT /v2/.
A manifest is created referencing all blobs.
Existing blobs are skipped (dedup by SHA-256).

Requires EPOCH_SERVER (default http://127.0.0.1:4300) and
EPOCH_REGISTRY_TOKEN environment variables.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := cmd.Context()

			serverURL := os.Getenv("EPOCH_SERVER")
			if serverURL == "" {
				serverURL = defaultServerURL
			}
			token := os.Getenv("EPOCH_REGISTRY_TOKEN")

			client := &http.Client{
				Transport: &http.Transport{
					TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // registry may use self-signed certs
					MaxIdleConnsPerHost: 4,
					IdleConnTimeout:     90 * time.Second,
				},
			}
			paths := cocoon.NewPaths(flagRootDir)

			// Find snapshot data directory.
			sid, err := paths.ResolveSnapshotID(name)
			if err != nil {
				return fmt.Errorf("resolve snapshot %s: %w", name, err)
			}
			dataDir := paths.SnapshotDataDir(sid)
			fmt.Printf("pushing %s:%s (id=%s) to %s ...\n", name, tag, sid[:12], serverURL)

			entries, err := os.ReadDir(dataDir)
			if err != nil {
				return fmt.Errorf("read snapshot dir %s: %w", dataDir, err)
			}

			type layerInfo struct {
				Filename string `json:"filename"`
				Digest   string `json:"digest"`
				Size     int64  `json:"size"`
			}
			var layers []layerInfo
			var totalSize int64

			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				filePath := filepath.Join(dataDir, entry.Name())
				digest, size, blobErr := pushBlobHTTP(ctx, client, serverURL, token, name, filePath)
				if blobErr != nil {
					return fmt.Errorf("push blob %s: %w", entry.Name(), blobErr)
				}
				layers = append(layers, layerInfo{
					Filename: entry.Name(),
					Digest:   digest,
					Size:     size,
				})
				totalSize += size
				fmt.Printf("  %s → sha256:%s (%s)\n", entry.Name(), digest[:12], cocoon.HumanSize(size))
			}

			// Build and push manifest.
			manifest := map[string]any{
				"name":       name,
				"tag":        tag,
				"snapshotID": sid,
				"layers":     layers,
				"totalSize":  totalSize,
				"createdAt":  time.Now().UTC().Format(time.RFC3339),
			}
			data, _ := json.MarshalIndent(manifest, "", "  ")
			url := fmt.Sprintf("%s/v2/%s/manifests/%s", serverURL, name, tag)
			req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(data))
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", "application/vnd.epoch.manifest.v1+json")
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("PUT manifest: %w", err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode >= 400 {
				return fmt.Errorf("PUT manifest: %d", resp.StatusCode)
			}

			fmt.Printf("\n=== Pushed %s:%s ===\n", name, tag)
			fmt.Printf("  layers:     %d\n", len(layers))
			fmt.Printf("  total-size: %s\n", cocoon.HumanSize(totalSize))
			return nil
		},
	}

	cmd.Flags().StringVarP(&tag, "tag", "t", "latest", "Tag for this snapshot")
	return cmd
}

func pushBlobHTTP(ctx context.Context, client *http.Client, serverURL, token, name, filePath string) (string, int64, error) {
	// Hash file.
	f, err := os.Open(filePath) //nolint:gosec // filePath is from trusted snapshot data dir
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	size, err := io.Copy(h, f)
	_ = f.Close()
	if err != nil {
		return "", 0, err
	}
	digest := hex.EncodeToString(h.Sum(nil))

	// HEAD check — skip if exists.
	headURL := fmt.Sprintf("%s/v2/%s/blobs/sha256:%s", serverURL, name, digest)
	if headReq, headErr := http.NewRequestWithContext(ctx, http.MethodHead, headURL, nil); headErr == nil {
		if token != "" {
			headReq.Header.Set("Authorization", "Bearer "+token)
		}
		if headResp, doErr := client.Do(headReq); doErr == nil {
			_ = headResp.Body.Close()
			if headResp.StatusCode == 200 {
				return digest, size, nil
			}
		}
	}

	// Upload.
	f, err = os.Open(filePath) //nolint:gosec // filePath is from trusted snapshot data dir
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()

	url := fmt.Sprintf("%s/v2/%s/blobs/sha256:%s", serverURL, name, digest)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, f)
	if err != nil {
		return "", 0, err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("PUT blob: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", 0, fmt.Errorf("PUT blob %s: %d %s", digest[:12], resp.StatusCode, string(body))
	}
	return digest, size, nil
}
