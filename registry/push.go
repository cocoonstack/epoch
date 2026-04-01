package registry

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cocoonstack/epoch/cocoon"
	"github.com/cocoonstack/epoch/manifest"
)

// Push uploads a Cocoon snapshot to the Epoch registry.
func (r *Registry) Push(ctx context.Context, paths *cocoon.Paths, snapshotName, tag string, progress func(string)) (*manifest.Manifest, error) {
	if tag == "" {
		tag = "latest"
	}

	// Resolve snapshot name → ID.
	sid, err := paths.ResolveSnapshotID(snapshotName)
	if err != nil {
		return nil, err
	}
	dataDir := paths.SnapshotDataDir(sid)

	// Read snapshot record for metadata.
	db, err := paths.ReadSnapshotDB()
	if err != nil {
		return nil, err
	}
	rec := db.Snapshots[sid]
	if rec == nil {
		return nil, fmt.Errorf("snapshot %s not found in DB", sid)
	}

	// Enumerate files in snapshot data directory.
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, fmt.Errorf("read snapshot dir %s: %w", dataDir, err)
	}

	// Upload each file as a blob.
	var layers []manifest.Layer
	var totalSize int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filePath := filepath.Join(dataDir, entry.Name())
		if progress != nil {
			progress(fmt.Sprintf("uploading %s...", entry.Name()))
		}
		digest, size, err := r.PushBlob(ctx, filePath)
		if err != nil {
			return nil, fmt.Errorf("push blob %s: %w", entry.Name(), err)
		}
		layers = append(layers, manifest.Layer{
			Digest:    digest,
			Size:      size,
			Filename:  entry.Name(),
			MediaType: manifest.MediaTypeForFile(entry.Name()),
		})
		totalSize += size
		if progress != nil {
			progress(fmt.Sprintf("  %s → sha256:%s (%s)", entry.Name(), digest[:12], cocoon.HumanSize(size)))
		}
	}

	// Upload cloudimg base images referenced by this snapshot.
	var baseImages []manifest.Layer
	if len(rec.ImageBlobIDs) > 0 {
		blobDir := paths.CloudimgBlobDir()
		for hexID := range rec.ImageBlobIDs {
			for _, ext := range []string{".qcow2", ".raw", ""} {
				fp := filepath.Join(blobDir, hexID+ext)
				if _, err := os.Stat(fp); err == nil {
					if progress != nil {
						progress(fmt.Sprintf("uploading base image %s%s...", hexID[:12], ext))
					}
					digest, size, err := r.PushBlob(ctx, fp)
					if err != nil {
						return nil, fmt.Errorf("push base image %s: %w", hexID[:12], err)
					}
					baseImages = append(baseImages, manifest.Layer{
						Digest:    digest,
						Size:      size,
						Filename:  hexID + ext,
						MediaType: manifest.MediaTypeForFile(hexID + ext),
					})
					totalSize += size
					break
				}
			}
		}
	}

	m := &manifest.Manifest{
		SchemaVersion: 1,
		Name:          snapshotName,
		Tag:           tag,
		SnapshotID:    sid,
		Image:         rec.Image,
		CPU:           rec.CPU,
		Memory:        rec.Memory,
		Storage:       rec.Storage,
		NICs:          rec.NICs,
		Layers:        layers,
		BaseImages:    baseImages,
		TotalSize:     totalSize,
		CreatedAt:     rec.CreatedAt,
		PushedAt:      time.Now(),
	}

	if err := r.PushManifest(ctx, m); err != nil {
		return nil, fmt.Errorf("push manifest: %w", err)
	}
	return m, nil
}
