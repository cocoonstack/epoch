package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/projecteru2/core/log"

	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/registry"
	"github.com/cocoonstack/epoch/utils"
)

// SyncFromCatalog reads the remote catalog and syncs all metadata into MySQL.
func (s *Store) SyncFromCatalog(ctx context.Context, reg *registry.Registry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cat, err := reg.GetCatalog(ctx)
	if err != nil {
		return fmt.Errorf("get catalog: %w", err)
	}

	logger := log.WithFunc("Store.SyncFromCatalog")
	for repoName, repo := range cat.Repositories {
		repoID, err := s.upsertRepository(ctx, repoName)
		if err != nil {
			logger.Warnf(ctx, "upsert repo %s: %v", repoName, err)
			continue
		}
		for tagName := range repo.Tags {
			if err := s.syncTag(ctx, reg, repoID, repoName, tagName); err != nil {
				logger.Warnf(ctx, "sync tag %s:%s: %v", repoName, tagName, err)
			}
		}
	}

	// Clean up repos/tags no longer in catalog.
	s.cleanOrphans(ctx, cat)
	return nil
}

func (s *Store) syncTag(ctx context.Context, reg *registry.Registry, repoID int64, repoName, tagName string) error {
	m, err := reg.PullManifest(ctx, repoName, tagName)
	if err != nil {
		return err
	}

	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	digest := utils.SHA256Hex(data)

	t := Tag{
		Name:         tagName,
		Digest:       digest,
		ManifestJSON: string(data),
		TotalSize:    m.TotalSize,
		LayerCount:   len(m.Layers) + len(m.BaseImages),
		PushedAt:     m.PushedAt,
	}
	if err := s.upsertTag(ctx, repoID, t); err != nil {
		return err
	}

	// Upsert blobs.
	allLayers := make([]manifest.Layer, 0, len(m.Layers)+len(m.BaseImages))
	allLayers = append(allLayers, m.Layers...)
	allLayers = append(allLayers, m.BaseImages...)
	logger := log.WithFunc("Store.syncTag")
	for _, layer := range allLayers {
		if err := s.upsertBlob(ctx, Blob{
			Digest:    layer.Digest,
			Size:      layer.Size,
			MediaType: layer.MediaType,
		}); err != nil {
			logger.Warnf(ctx, "upsert blob %s: %v", layer.Digest[:12], err)
		}
	}
	return nil
}

func (s *Store) cleanOrphans(ctx context.Context, cat *manifest.Catalog) {
	// Get all repo names from DB.
	rows, err := s.db.QueryContext(ctx, `SELECT id, name FROM repositories`)
	if err != nil {
		return
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			continue
		}
		repo, exists := cat.Repositories[name]
		if !exists {
			_, _ = s.db.ExecContext(ctx, `DELETE FROM repositories WHERE id = ?`, id)
			continue
		}
		// Clean orphan tags.
		tagRows, err := s.db.QueryContext(ctx, `SELECT name FROM tags WHERE repository_id = ?`, id)
		if err != nil {
			continue
		}
		for tagRows.Next() {
			var tagName string
			if err := tagRows.Scan(&tagName); err != nil {
				continue
			}
			if _, ok := repo.Tags[tagName]; !ok {
				_, _ = s.db.ExecContext(ctx, `DELETE FROM tags WHERE repository_id = ? AND name = ?`, id, tagName)
			}
		}
		_ = tagRows.Close()
	}
}
