package cmd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/epoch/cocoon"
	"github.com/cocoonstack/epoch/manifest"
)

func newPullCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pull <name>[:<tag>]",
		Short: "Pull a snapshot from Epoch registry server",
		Long: `Download a snapshot manifest and all referenced blobs via HTTP.
Writes files to the Cocoon snapshot data directory and updates snapshots.json.
Existing files are skipped (resume-safe).

Requires EPOCH_SERVER (default http://127.0.0.1:4300) and
EPOCH_REGISTRY_TOKEN environment variables.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, tag := parseRef(args[0])
			return pullViaHTTP(cmd.Context(), name, tag)
		},
	}
	return cmd
}

func pullViaHTTP(ctx context.Context, name, tag string) error {
	serverURL := os.Getenv("EPOCH_SERVER")
	if serverURL == "" {
		serverURL = "http://127.0.0.1:4300"
	}
	serverURL = strings.TrimRight(serverURL, "/")
	token := os.Getenv("EPOCH_REGISTRY_TOKEN")

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:    &tls.Config{InsecureSkipVerify: true},
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:    90 * time.Second,
		},
	}
	paths := cocoon.NewPaths(flagRootDir)

	fmt.Printf("pulling %s:%s from %s ...\n", name, tag, serverURL)

	// 1. Get manifest.
	m, err := httpGetManifest(ctx, client, serverURL, token, name, tag)
	if err != nil {
		return err
	}

	sid := m.SnapshotID
	dataDir := paths.SnapshotDataDir(sid)
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return err
	}

	// 2. Download layers.
	for _, layer := range m.Layers {
		destPath := filepath.Join(dataDir, layer.Filename)
		if _, err := os.Stat(destPath); err == nil {
			fmt.Printf("  %s exists, skip\n", layer.Filename)
			continue
		}
		fmt.Printf("  downloading %s (%s)...\n", layer.Filename, cocoon.HumanSize(layer.Size))
		if err := httpDownloadBlob(ctx, client, serverURL, token, name, layer.Digest, destPath); err != nil {
			return fmt.Errorf("download %s: %w", layer.Filename, err)
		}
	}

	// 3. Download base images.
	if len(m.BaseImages) > 0 {
		blobDir := paths.CloudimgBlobDir()
		if err := os.MkdirAll(blobDir, 0o750); err != nil {
			return err
		}
		for _, bi := range m.BaseImages {
			destPath := filepath.Join(blobDir, bi.Filename)
			if _, err := os.Stat(destPath); err == nil {
				continue
			}
			fmt.Printf("  downloading base image %s (%s)...\n", bi.Filename, cocoon.HumanSize(bi.Size))
			if err := httpDownloadBlob(ctx, client, serverURL, token, name, bi.Digest, destPath); err != nil {
				return fmt.Errorf("download base %s: %w", bi.Filename, err)
			}
			_ = os.Chmod(destPath, 0o444)
		}
	}

	// 4. Update snapshots.json.
	if err := updateSnapshotDB(paths, m, name); err != nil {
		return err
	}

	fmt.Printf("\n=== Pulled %s:%s ===\n", m.Name, m.Tag)
	fmt.Printf("  snapshot-id: %s\n", m.SnapshotID)
	fmt.Printf("  layers:      %d\n", len(m.Layers))
	fmt.Printf("  total-size:  %s\n", cocoon.HumanSize(m.TotalSize))
	return nil
}

func httpGetManifest(ctx context.Context, client *http.Client, serverURL, token, name, tag string) (*manifest.Manifest, error) {
	url := fmt.Sprintf("%s/v2/%s/manifests/%s", serverURL, name, tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("GET manifest %s:%s: %d %s", name, tag, resp.StatusCode, string(body))
	}
	var m manifest.Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	return &m, nil
}

func httpDownloadBlob(ctx context.Context, client *http.Client, serverURL, token, name, digest, destPath string) error {
	url := fmt.Sprintf("%s/v2/%s/blobs/sha256:%s", serverURL, name, digest)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GET blob %s: %d %s", digest[:12], resp.StatusCode, string(body))
	}
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func updateSnapshotDB(paths *cocoon.Paths, m *manifest.Manifest, name string) error {
	db, err := paths.ReadSnapshotDB()
	if err != nil {
		return err
	}

	blobIDs := make(map[string]struct{})
	for _, bi := range m.BaseImages {
		id := bi.Filename
		for _, ext := range []string{".qcow2", ".raw"} {
			if len(id) > len(ext) && id[len(id)-len(ext):] == ext {
				id = id[:len(id)-len(ext)]
				break
			}
		}
		blobIDs[id] = struct{}{}
	}

	db.Snapshots[m.SnapshotID] = &cocoon.SnapshotRecord{
		ID:           m.SnapshotID,
		Name:         name,
		Image:        m.Image,
		ImageBlobIDs: blobIDs,
		CPU:          m.CPU,
		Memory:       m.Memory,
		Storage:      m.Storage,
		NICs:         m.NICs,
		CreatedAt:    m.CreatedAt,
		DataDir:      paths.SnapshotDataDir(m.SnapshotID),
	}
	db.Names[name] = m.SnapshotID

	return paths.WriteSnapshotDB(db)
}

func parseRef(ref string) (string, string) {
	if i := strings.LastIndex(ref, ":"); i > 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}

