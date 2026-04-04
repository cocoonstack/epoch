package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/projecteru2/core/log"

	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/registry"
	"github.com/cocoonstack/epoch/utils"
)

var errSyncInProgress = fmt.Errorf("sync already in progress")

func (s *Store) SyncFromCatalog(ctx context.Context, reg *registry.Registry) error {
	if !s.mu.TryLock() {
		return errSyncInProgress
	}
	defer s.mu.Unlock()

	cat, digest, err := reg.GetCatalogWithDigest(ctx)
	if err != nil {
		return fmt.Errorf("get catalog: %w", err)
	}

	if digest != "" && digest == s.lastCatalogHash {
		return nil
	}

	logger := log.WithFunc("store.SyncFromCatalog")

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for repoName, repo := range cat.Repositories {
		repoID, err := upsertRepositoryTx(ctx, tx, repoName)
		if err != nil {
			logger.Warnf(ctx, "upsert repo %s: %v", repoName, err)
			continue
		}
		for _, tagName := range slices.Sorted(maps.Keys(repo.Tags)) {
			if err := s.syncTag(ctx, tx, reg, repoID, repoName, tagName); err != nil {
				logger.Warnf(ctx, "sync tag %s:%s: %v", repoName, tagName, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}

	s.cleanOrphans(ctx, cat)
	s.lastCatalogHash = digest
	return nil
}

func (s *Store) syncTag(ctx context.Context, tx *sql.Tx, reg *registry.Registry, repoID int64, repoName, tagName string) error {
	existing, _ := s.getTagDigest(ctx, repoID, tagName)

	raw, err := reg.ManifestJSON(ctx, repoName, tagName)
	if err != nil {
		return err
	}

	digest := utils.SHA256Hex(raw)
	if digest == existing {
		return nil
	}

	var m manifest.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("decode manifest %s:%s: %w", repoName, tagName, err)
	}

	t := Tag{
		Name:         tagName,
		Digest:       digest,
		ManifestJSON: string(raw),
		TotalSize:    m.TotalSize,
		LayerCount:   len(m.Layers) + len(m.BaseImages),
		PushedAt:     m.PushedAt,
	}
	if err := upsertTagTx(ctx, tx, repoID, t); err != nil {
		return err
	}

	allLayers := make([]manifest.Layer, 0, len(m.Layers)+len(m.BaseImages))
	allLayers = append(allLayers, m.Layers...)
	allLayers = append(allLayers, m.BaseImages...)
	if err := batchUpsertBlobsTx(ctx, tx, allLayers); err != nil {
		logger := log.WithFunc("store.syncTag")
		logger.Warnf(ctx, "batch upsert blobs for %s:%s: %v", repoName, tagName, err)
	}
	return nil
}

func (s *Store) getTagDigest(ctx context.Context, repoID int64, tagName string) (string, error) {
	var digest string
	err := s.db.QueryRowContext(ctx, `SELECT digest FROM tags WHERE repository_id = ? AND name = ?`, repoID, tagName).Scan(&digest)
	return digest, err
}

func upsertRepositoryTx(ctx context.Context, tx *sql.Tx, name string) (int64, error) {
	result, err := tx.ExecContext(ctx,
		`INSERT INTO repositories (name) VALUES (?) ON DUPLICATE KEY UPDATE id=LAST_INSERT_ID(id), updated_at=NOW()`, name)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func upsertTagTx(ctx context.Context, tx *sql.Tx, repoID int64, t Tag) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO tags (repository_id, name, digest, manifest_json, total_size, layer_count, pushed_at, synced_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, NOW())
		ON DUPLICATE KEY UPDATE
			digest=VALUES(digest), manifest_json=VALUES(manifest_json),
			total_size=VALUES(total_size), layer_count=VALUES(layer_count),
			pushed_at=VALUES(pushed_at), synced_at=NOW()`,
		repoID, t.Name, t.Digest, t.ManifestJSON, t.TotalSize, t.LayerCount, t.PushedAt)
	return err
}

func batchUpsertBlobsTx(ctx context.Context, tx *sql.Tx, layers []manifest.Layer) error {
	if len(layers) == 0 {
		return nil
	}
	const batchSize = 100
	for i := 0; i < len(layers); i += batchSize {
		end := min(i+batchSize, len(layers))
		batch := layers[i:end]

		var sb strings.Builder
		sb.WriteString(`INSERT INTO blobs (digest, size, media_type, ref_count) VALUES `)
		args := make([]any, 0, len(batch)*3)
		for j, layer := range batch {
			if j > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("(?,?,?,1)")
			args = append(args, layer.Digest, layer.Size, layer.MediaType)
		}
		sb.WriteString(` ON DUPLICATE KEY UPDATE size=VALUES(size), media_type=VALUES(media_type)`)
		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return err
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
