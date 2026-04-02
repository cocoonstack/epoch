package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/epoch/cocoon"
	"github.com/cocoonstack/epoch/internal/registryclient"
	"github.com/cocoonstack/epoch/internal/util"
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
			name, tag := util.ParseRef(args[0])
			return pullViaHTTP(cmd.Context(), name, tag)
		},
	}
	return cmd
}

func pullViaHTTP(ctx context.Context, name, tag string) error {
	client := newRegistryClient()
	paths := cocoon.NewPaths(flagRootDir)

	fmt.Printf("pulling %s:%s from %s ...\n", name, tag, client.BaseURL())

	// 1. Get manifest.
	m, err := client.GetManifest(ctx, name, tag)
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
		if err := downloadBlob(ctx, client, name, layer.Digest, destPath); err != nil {
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
			if err := downloadBlob(ctx, client, name, bi.Digest, destPath); err != nil {
				return fmt.Errorf("download base %s: %w", bi.Filename, err)
			}
			_ = os.Chmod(destPath, 0o444) //nolint:gosec // read-only for base images is intentional
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

func downloadBlob(ctx context.Context, client *registryclient.Client, name, digest, destPath string) error {
	body, err := client.GetBlob(ctx, name, digest)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	f, err := os.Create(destPath) //nolint:gosec // destPath is constructed from trusted snapshot data dir
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(f, body)
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
