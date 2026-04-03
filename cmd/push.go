package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/epoch/cocoon"
	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/registryclient"
	"github.com/cocoonstack/epoch/utils"
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

			client := newRegistryClient()
			paths := cocoon.NewPaths(flagRootDir)

			// Find snapshot data directory and read metadata.
			sid, err := paths.ResolveSnapshotID(name)
			if err != nil {
				return fmt.Errorf("resolve snapshot %s: %w", name, err)
			}
			db, err := paths.ReadSnapshotDB()
			if err != nil {
				return fmt.Errorf("read snapshot db: %w", err)
			}
			rec := db.Snapshots[sid]
			dataDir := paths.SnapshotDataDir(sid)
			fmt.Printf("pushing %s:%s (id=%s) to %s ...\n", name, tag, utils.Truncate(sid, 12), client.BaseURL())

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
				digest, size, blobErr := pushBlob(ctx, client, name, filePath)
				if blobErr != nil {
					return fmt.Errorf("push blob %s: %w", entry.Name(), blobErr)
				}
				layers = append(layers, layerInfo{
					Filename: entry.Name(),
					Digest:   digest,
					Size:     size,
				})
				totalSize += size
				fmt.Printf("  %s → sha256:%s (%s)\n", entry.Name(), utils.Truncate(digest, 12), utils.HumanSize(size))
			}

			manifestDoc := manifest.Manifest{
				SchemaVersion: 1,
				Name:          name,
				Tag:           tag,
				SnapshotID:    sid,
				Layers:        make([]manifest.Layer, 0, len(layers)),
				TotalSize:     totalSize,
				CreatedAt:     time.Now().UTC(),
			}
			if rec != nil {
				manifestDoc.Image = rec.Image
				manifestDoc.CPU = rec.CPU
				manifestDoc.Memory = rec.Memory
				manifestDoc.Storage = rec.Storage
				manifestDoc.NICs = rec.NICs
				if len(rec.ImageBlobIDs) > 0 {
					manifestDoc.ImageBlobIDs = make(map[string]string, len(rec.ImageBlobIDs))
					for k := range rec.ImageBlobIDs {
						manifestDoc.ImageBlobIDs[k] = k
					}
				}
			}
			for _, layer := range layers {
				manifestDoc.Layers = append(manifestDoc.Layers, manifest.Layer{
					Filename:  layer.Filename,
					Digest:    layer.Digest,
					Size:      layer.Size,
					MediaType: manifest.MediaTypeForFile(layer.Filename),
				})
			}
			data, err := json.MarshalIndent(manifestDoc, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal manifest: %w", err)
			}
			if err := client.PutManifestJSON(ctx, name, tag, data); err != nil {
				return fmt.Errorf("put manifest %s:%s: %w", name, tag, err)
			}

			fmt.Printf("\n=== Pushed %s:%s ===\n", name, tag)
			fmt.Printf("  layers:     %d\n", len(layers))
			fmt.Printf("  total-size: %s\n", utils.HumanSize(totalSize))
			return nil
		},
	}

	cmd.Flags().StringVarP(&tag, "tag", "t", "latest", "Tag for this snapshot")
	return cmd
}

func pushBlob(ctx context.Context, client *registryclient.Client, name, filePath string) (string, int64, error) {
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
	exists, err := client.BlobExists(ctx, name, digest)
	if err != nil {
		return "", 0, err
	}
	if exists {
		return digest, size, nil
	}

	// Upload.
	f, err = os.Open(filePath) //nolint:gosec // filePath is from trusted snapshot data dir
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()

	if err := client.PutBlob(ctx, name, digest, f, size); err != nil {
		return "", 0, err
	}
	return digest, size, nil
}
