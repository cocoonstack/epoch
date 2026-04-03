package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

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

	logger := log.WithFunc("store.SyncFromCatalog")
	for repoName, repo := range cat.Repositories {
		repoID, err := s.upsertRepository(ctx, repoName)
		if err != nil {
			logger.Warnf(ctx, "upsert repo %s: %v", repoName, err)
			continue
		}
		for _, tagName := range slices.Sorted(maps.Keys(repo.Tags)) {
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
	raw, err := reg.ManifestJSON(ctx, repoName, tagName)
	if err != nil {
		return err
	}

	var m manifest.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("decode manifest %s:%s: %w", repoName, tagName, err)
	}

	digest := utils.SHA256Hex(raw)
	t := Tag{
		Name:         tagName,
		Digest:       digest,
		ManifestJSON: string(raw),
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
	logger := log.WithFunc("store.syncTag")
	for _, layer := range allLayers {
		if err := s.upsertBlob(ctx, Blob{
			Digest:    layer.Digest,
			Size:      layer.Size,
			MediaType: layer.MediaType,
		}); err != nil {
			logger.Warnf(ctx, "upsert blob %s: %v", utils.Truncate(layer.Digest, 12), err)
		}
	}
	return nil
}

func (s *Store) cleanOrphans(ctx context.Context, cat *manifest.Catalog) {
	type repositoryRef struct {
		ID   int64
		Name string
	}

	repos, err := queryRows(ctx, s.db, `SELECT id, name FROM repositories`, func(rows *sql.Rows, repo *repositoryRef) error {
		return rows.Scan(&repo.ID, &repo.Name)
	})
	if err != nil {
		return
	}

	for _, repoRef := range repos {
		repo, exists := cat.Repositories[repoRef.Name]
		if !exists {
			_, _ = s.db.ExecContext(ctx, `DELETE FROM repositories WHERE id = ?`, repoRef.ID)
			continue
		}

		tagNames, err := queryRows(ctx, s.db, `SELECT name FROM tags WHERE repository_id = ?`, func(rows *sql.Rows, tagName *string) error {
			return rows.Scan(tagName)
		}, repoRef.ID)
		if err != nil {
			continue
		}

		for _, tagName := range tagNames {
			if _, ok := repo.Tags[tagName]; !ok {
				_, _ = s.db.ExecContext(ctx, `DELETE FROM tags WHERE repository_id = ? AND name = ?`, repoRef.ID, tagName)
			}
		}
	}
}
