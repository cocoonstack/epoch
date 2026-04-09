package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/projecteru2/core/log"

	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/registry"
	"github.com/cocoonstack/epoch/utils"
)

// errSyncInProgress signals that another goroutine is already running
// SyncFromCatalog. Returned (and silently swallowed by background callers)
// instead of blocking on the mutex.
var errSyncInProgress = errors.New("sync already in progress")

// SyncFromCatalog walks the registry catalog and ingests every (repo, tag)
// into MySQL. Each tag's manifest is parsed as OCI and indexed by digest,
// artifactType, and aggregate size. Orphaned rows (repos / tags that no
// longer appear in the catalog) are deleted in a second pass.
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

// syncTag fetches a single manifest from the registry, parses it, and writes
// the tag + its blob descriptors into the SQL transaction.
func (s *Store) syncTag(ctx context.Context, tx *sql.Tx, reg *registry.Registry, repoID int64, repoName, tagName string) error {
	existing, dbErr := s.getTagDigest(ctx, repoID, tagName)
	if dbErr != nil && !errors.Is(dbErr, sql.ErrNoRows) {
		log.WithFunc("store.syncTag").Warnf(ctx, "lookup existing digest for %s:%s: %v", repoName, tagName, dbErr)
	}

	raw, err := reg.ManifestJSON(ctx, repoName, tagName)
	if err != nil {
		return err
	}

	digest := utils.SHA256Hex(raw)
	if digest == existing {
		return nil
	}

	m, err := manifest.Parse(raw)
	if err != nil {
		return fmt.Errorf("decode manifest %s:%s: %w", repoName, tagName, err)
	}

	totalSize := m.Config.Size
	for _, layer := range m.Layers {
		totalSize += layer.Size
	}

	t := Tag{
		Name:         tagName,
		Digest:       digest,
		ArtifactType: m.ArtifactType,
		ManifestJSON: string(raw),
		TotalSize:    totalSize,
		LayerCount:   len(m.Layers),
		PushedAt:     manifestPushedAt(m),
	}
	if err := upsertTagTx(ctx, tx, repoID, t); err != nil {
		return err
	}

	descriptors := slices.Concat([]manifest.Descriptor{m.Config}, m.Layers)
	if err := batchUpsertBlobsTx(ctx, tx, descriptors); err != nil {
		log.WithFunc("store.syncTag").Warnf(ctx, "batch upsert blobs for %s:%s: %v", repoName, tagName, err)
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
		INSERT INTO tags (repository_id, name, digest, artifact_type, manifest_json, total_size, layer_count, pushed_at, synced_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, NOW())
		ON DUPLICATE KEY UPDATE
			digest=VALUES(digest), artifact_type=VALUES(artifact_type), manifest_json=VALUES(manifest_json),
			total_size=VALUES(total_size), layer_count=VALUES(layer_count),
			pushed_at=VALUES(pushed_at), synced_at=NOW()`,
		repoID, t.Name, t.Digest, t.ArtifactType, t.ManifestJSON, t.TotalSize, t.LayerCount, t.PushedAt)
	return err
}

// manifestPushedAt returns the manifest's `org.opencontainers.image.created`
// annotation parsed as RFC 3339, or the current time if the annotation is
// missing or unparseable.
func manifestPushedAt(m *manifest.OCIManifest) time.Time {
	if v, ok := m.Annotations[manifest.AnnotationCreated]; ok {
		if ts, err := time.Parse(time.RFC3339, v); err == nil {
			return ts
		}
	}
	return time.Now().UTC()
}

// batchUpsertBlobsTx records every descriptor referenced by a manifest into
// the blobs table so the UI / control plane has a single index of every
// content-addressable object in the registry. Failures are not fatal — the
// caller logs and continues so an unindexed blob does not block the tag
// upsert.
func batchUpsertBlobsTx(ctx context.Context, tx *sql.Tx, descriptors []manifest.Descriptor) error {
	if len(descriptors) == 0 {
		return nil
	}
	const batchSize = 100
	for batch := range slices.Chunk(descriptors, batchSize) {
		var sb strings.Builder
		sb.WriteString(`INSERT INTO blobs (digest, size, media_type, ref_count) VALUES `)
		args := make([]any, 0, len(batch)*3)
		for j, d := range batch {
			if j > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("(?,?,?,1)")
			args = append(args, strings.TrimPrefix(d.Digest, "sha256:"), d.Size, d.MediaType)
		}
		sb.WriteString(` ON DUPLICATE KEY UPDATE size=VALUES(size), media_type=VALUES(media_type)`)
		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) cleanOrphans(ctx context.Context, cat *manifest.Catalog) {
	logger := log.WithFunc("store.cleanOrphans")

	type repositoryRef struct {
		ID   int64
		Name string
	}

	repos, err := queryRows(ctx, s.db, `SELECT id, name FROM repositories`, func(rows *sql.Rows, repo *repositoryRef) error {
		return rows.Scan(&repo.ID, &repo.Name)
	})
	if err != nil {
		logger.Warnf(ctx, "list repositories: %v", err)
		return
	}

	for _, repoRef := range repos {
		repo, exists := cat.Repositories[repoRef.Name]
		if !exists {
			if _, delErr := s.db.ExecContext(ctx, `DELETE FROM repositories WHERE id = ?`, repoRef.ID); delErr != nil {
				logger.Warnf(ctx, "delete orphan repository %s: %v", repoRef.Name, delErr)
			}
			continue
		}

		tagNames, err := queryRows(ctx, s.db, `SELECT name FROM tags WHERE repository_id = ?`, func(rows *sql.Rows, tagName *string) error {
			return rows.Scan(tagName)
		}, repoRef.ID)
		if err != nil {
			logger.Warnf(ctx, "list tags for %s: %v", repoRef.Name, err)
			continue
		}

		for _, tagName := range tagNames {
			if _, ok := repo.Tags[tagName]; !ok {
				if _, delErr := s.db.ExecContext(ctx, `DELETE FROM tags WHERE repository_id = ? AND name = ?`, repoRef.ID, tagName); delErr != nil {
					logger.Warnf(ctx, "delete orphan tag %s:%s: %v", repoRef.Name, tagName, delErr)
				}
			}
		}
	}
}

// artifactKindString maps an OCI artifactType to the human-readable kind name
// used by the UI. Falls back to "container-image" for empty / unknown values
// because plain container images don't carry an artifactType.
func artifactKindString(artifactType string) string {
	switch artifactType {
	case manifest.ArtifactTypeSnapshot:
		return manifest.KindSnapshot.String()
	case manifest.ArtifactTypeOSImage:
		return manifest.KindCloudImage.String()
	default:
		return manifest.KindContainerImage.String()
	}
}
