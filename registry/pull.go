package registry

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cocoonstack/epoch/cocoon"
	"github.com/cocoonstack/epoch/manifest"
)

// Pull downloads a snapshot from Epoch and writes it to Cocoon's snapshot directory.
func (r *Registry) Pull(ctx context.Context, paths *cocoon.Paths, name, tag string, progress func(string)) (*manifest.Manifest, error) {
	if tag == "" {
		tag = "latest"
	}

	if progress != nil {
		progress(fmt.Sprintf("pulling %s:%s...", name, tag))
	}

	m, err := r.PullManifest(ctx, name, tag)
	if err != nil {
		return nil, fmt.Errorf("pull manifest %s:%s: %w", name, tag, err)
	}

	sid := m.SnapshotID
	dataDir := paths.SnapshotDataDir(sid)

	if mkdirErr := os.MkdirAll(dataDir, 0o750); mkdirErr != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dataDir, mkdirErr)
	}

	// Download snapshot layers.
	for _, layer := range m.Layers {
		destPath := filepath.Join(dataDir, layer.Filename)
		if _, statErr := os.Stat(destPath); statErr == nil {
			if progress != nil {
				progress(fmt.Sprintf("  %s already exists, skipping", layer.Filename))
			}
			continue
		}
		if progress != nil {
			progress(fmt.Sprintf("  downloading %s (%s)...", layer.Filename, cocoon.HumanSize(layer.Size)))
		}
		if pullErr := r.PullBlob(ctx, layer.Digest, destPath); pullErr != nil {
			return nil, fmt.Errorf("pull blob %s: %w", layer.Filename, pullErr)
		}
	}

	// Download base images.
	if len(m.BaseImages) > 0 { //nolint:nestif // sequential download logic with progress reporting
		blobDir := paths.CloudimgBlobDir()
		if blobMkdirErr := os.MkdirAll(blobDir, 0o750); blobMkdirErr != nil {
			return nil, fmt.Errorf("mkdir %s: %w", blobDir, blobMkdirErr)
		}
		for _, bi := range m.BaseImages {
			destPath := filepath.Join(blobDir, bi.Filename)
			if _, statErr := os.Stat(destPath); statErr == nil {
				if progress != nil {
					progress(fmt.Sprintf("  base image %s already exists, skipping", bi.Filename))
				}
				continue
			}
			if progress != nil {
				progress(fmt.Sprintf("  downloading base image %s (%s)...", truncate(bi.Filename, 16), cocoon.HumanSize(bi.Size)))
			}
			if biPullErr := r.PullBlob(ctx, bi.Digest, destPath); biPullErr != nil {
				return nil, fmt.Errorf("pull base image %s: %w", bi.Filename, biPullErr)
			}
			_ = os.Chmod(destPath, 0o444) //nolint:gosec // read-only for base images is intentional
		}
	}

	// Update Cocoon's snapshots.json.
	db, err := paths.ReadSnapshotDB()
	if err != nil {
		return nil, fmt.Errorf("read snapshot DB: %w", err)
	}

	blobIDs := make(map[string]struct{})
	for _, bi := range m.BaseImages {
		id := bi.Filename
		for _, ext := range []string{".qcow2", ".raw"} {
			if trimmed, ok := strings.CutSuffix(id, ext); ok {
				id = trimmed
				break
			}
		}
		blobIDs[id] = struct{}{}
	}

	db.Snapshots[sid] = &cocoon.SnapshotRecord{
		ID:           sid,
		Name:         name,
		Image:        m.Image,
		ImageBlobIDs: blobIDs,
		CPU:          m.CPU,
		Memory:       m.Memory,
		Storage:      m.Storage,
		NICs:         m.NICs,
		CreatedAt:    m.CreatedAt,
		Pending:      false,
		DataDir:      dataDir,
	}
	db.Names[name] = sid

	if err := paths.WriteSnapshotDB(db); err != nil {
		return nil, fmt.Errorf("write snapshot DB: %w", err)
	}

	if progress != nil {
		progress(fmt.Sprintf("snapshot %s:%s pulled (%s)", name, tag, cocoon.HumanSize(m.TotalSize)))
	}
	return m, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
