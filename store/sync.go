package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/projecteru2/core/log"

	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/registry"
	"github.com/cocoonstack/epoch/utils"
)

const indexFetchConcurrency = 4

var errSyncInProgress = errors.New("sync already in progress")

// SyncFromCatalog ingests catalog entries into MySQL. Per-tag failures are non-fatal.
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

	pending, err := s.prepareSync(ctx, cat, reg, logger)
	if err != nil {
		return err
	}
	if err := s.commitSync(ctx, pending, logger); err != nil {
		return err
	}

	s.cleanOrphans(ctx, cat)
	s.lastCatalogHash = digest
	return nil
}

type pendingTag struct {
	repoName    string
	tag         Tag
	descriptors []manifest.Descriptor
}

func (s *Store) prepareSync(ctx context.Context, cat *manifest.Catalog, reg *registry.Registry, logger *log.Fields) ([]pendingTag, error) {
	var pending []pendingTag
	for repoName, repo := range cat.Repositories {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, tagName := range slices.Sorted(maps.Keys(repo.Tags)) {
			p, ok := s.prepareTag(ctx, reg, repoName, tagName, logger)
			if !ok {
				continue
			}
			pending = append(pending, p)
		}
	}
	return pending, nil
}

func (s *Store) prepareTag(ctx context.Context, reg *registry.Registry, repoName, tagName string, logger *log.Fields) (pendingTag, bool) {
	existing, dbErr := s.getTagSyncState(ctx, repoName, tagName)
	if dbErr != nil && !errors.Is(dbErr, sql.ErrNoRows) {
		logger.Warnf(ctx, "lookup existing digest for %s:%s: %v", repoName, tagName, dbErr)
	}

	raw, err := reg.ManifestJSON(ctx, repoName, tagName)
	if err != nil {
		logger.Warnf(ctx, "fetch manifest %s:%s: %v", repoName, tagName, err)
		return pendingTag{}, false
	}

	// Force resync for index tags missing platform_sizes (backfill).
	digest := utils.SHA256Hex(raw)
	needsPlatformBackfill := existing.kind == manifest.KindImageIndex.String() && !existing.hasPlatformSizes
	if digest == existing.digest && !needsPlatformBackfill {
		return pendingTag{}, false
	}

	m, err := manifest.Parse(raw)
	if err != nil {
		logger.Warnf(ctx, "decode manifest %s:%s: %v", repoName, tagName, err)
		return pendingTag{}, false
	}

	kind := manifest.ClassifyParsed(m)
	totalSize, layerCount, descriptors, platformSizes := tagAggregates(ctx, reg, repoName, m, kind, logger)

	return pendingTag{
		repoName: repoName,
		tag: Tag{
			Name:          tagName,
			Digest:        digest,
			ArtifactType:  m.ArtifactType,
			Kind:          kind.String(),
			ManifestJSON:  string(raw),
			TotalSize:     totalSize,
			LayerCount:    layerCount,
			PlatformSizes: platformSizes,
			PushedAt:      manifestPushedAt(m),
		},
		descriptors: descriptors,
	}, true
}

func (s *Store) commitSync(ctx context.Context, pending []pendingTag, logger *log.Fields) error {
	if len(pending) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	repoIDs := make(map[string]int64)
	for _, p := range pending {
		if _, ok := repoIDs[p.repoName]; ok {
			continue
		}
		repoID, err := upsertRepositoryTx(ctx, tx, p.repoName)
		if err != nil {
			logger.Warnf(ctx, "upsert repo %s: %v", p.repoName, err)
			continue
		}
		repoIDs[p.repoName] = repoID
	}

	for _, p := range pending {
		repoID, ok := repoIDs[p.repoName]
		if !ok {
			continue
		}
		if err := upsertTagTx(ctx, tx, repoID, p.tag); err != nil {
			logger.Warnf(ctx, "upsert tag %s:%s: %v", p.repoName, p.tag.Name, err)
			continue
		}
		if err := batchUpsertBlobsTx(ctx, tx, p.descriptors); err != nil {
			logger.Warnf(ctx, "batch upsert blobs for %s:%s: %v", p.repoName, p.tag.Name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// tagAggregates returns totals for a manifest; for indexes, dedupes across platforms.
func tagAggregates(ctx context.Context, reg *registry.Registry, repoName string, m *manifest.OCIManifest, kind manifest.Kind, logger *log.Fields) (int64, int, []manifest.Descriptor, PlatformSizes) {
	if kind == manifest.KindImageIndex {
		return expandIndexAggregates(ctx, reg, repoName, m, logger)
	}
	totalSize := m.Config.Size
	for _, layer := range m.Layers {
		totalSize += layer.Size
	}
	descriptors := slices.Concat([]manifest.Descriptor{m.Config}, m.Layers)
	return totalSize, len(m.Layers), descriptors, nil
}

func expandIndexAggregates(ctx context.Context, reg *registry.Registry, repoName string, m *manifest.OCIManifest, logger *log.Fields) (int64, int, []manifest.Descriptor, PlatformSizes) {
	fetched := fetchIndexChildren(ctx, reg, repoName, m.Manifests, logger)

	seen := make(map[string]bool)
	var totalSize int64
	descriptors := make([]manifest.Descriptor, 0, 4*len(m.Manifests))
	platformSizes := make(PlatformSizes, 0, len(m.Manifests))

	for _, f := range fetched {
		if f == nil {
			continue
		}
		childSize := f.parsed.Config.Size
		for _, l := range f.parsed.Layers {
			childSize += l.Size
		}
		platformSizes = append(platformSizes, PlatformSize{
			Digest:     f.digest,
			Size:       childSize,
			LayerCount: len(f.parsed.Layers),
		})

		for _, d := range slices.Concat([]manifest.Descriptor{f.parsed.Config}, f.parsed.Layers) {
			if d.Digest == "" || seen[d.Digest] {
				continue
			}
			seen[d.Digest] = true
			totalSize += d.Size
			descriptors = append(descriptors, d)
		}
	}
	return totalSize, len(m.Manifests), descriptors, platformSizes
}

type fetchedChild struct {
	digest string
	parsed *manifest.OCIManifest
}

func fetchIndexChildren(ctx context.Context, reg *registry.Registry, repoName string, children []manifest.IndexManifest, logger *log.Fields) []*fetchedChild {
	results := make([]*fetchedChild, len(children))
	if len(children) == 0 {
		return results
	}

	sem := make(chan struct{}, indexFetchConcurrency)
	var wg sync.WaitGroup
	for i, child := range children {
		wg.Add(1)
		go func(i int, child manifest.IndexManifest) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			raw, err := reg.ManifestJSONByDigest(ctx, repoName, child.Digest)
			if err != nil {
				logger.Warnf(ctx, "fetch index child %s@%s: %v", repoName, child.Digest, err)
				return
			}
			parsed, err := manifest.Parse(raw)
			if err != nil {
				logger.Warnf(ctx, "parse index child %s@%s: %v", repoName, child.Digest, err)
				return
			}
			results[i] = &fetchedChild{digest: child.Digest, parsed: parsed}
		}(i, child)
	}
	wg.Wait()
	return results
}

type tagSyncState struct {
	digest           string
	kind             string
	hasPlatformSizes bool
}

func (s *Store) getTagSyncState(ctx context.Context, repoName, tagName string) (tagSyncState, error) {
	var st tagSyncState
	err := s.db.QueryRowContext(ctx,
		`SELECT t.digest, t.kind, t.platform_sizes IS NOT NULL
		 FROM tags t
		 JOIN repositories r ON r.id = t.repository_id
		 WHERE r.name = ? AND t.name = ?`,
		repoName, tagName,
	).Scan(&st.digest, &st.kind, &st.hasPlatformSizes)
	return st, err
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
		INSERT INTO tags (repository_id, name, digest, artifact_type, kind, manifest_json, total_size, layer_count, platform_sizes, pushed_at, synced_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NOW())
		ON DUPLICATE KEY UPDATE
			digest=VALUES(digest), artifact_type=VALUES(artifact_type), kind=VALUES(kind),
			manifest_json=VALUES(manifest_json),
			total_size=VALUES(total_size), layer_count=VALUES(layer_count),
			platform_sizes=VALUES(platform_sizes),
			pushed_at=VALUES(pushed_at), synced_at=NOW()`,
		repoID, t.Name, t.Digest, t.ArtifactType, t.Kind, t.ManifestJSON, t.TotalSize, t.LayerCount, t.PlatformSizes, t.PushedAt)
	return err
}

func manifestPushedAt(m *manifest.OCIManifest) time.Time {
	if v, ok := m.Annotations[manifest.AnnotationCreated]; ok {
		if ts, err := time.Parse(time.RFC3339, v); err == nil {
			return ts
		}
	}
	return time.Now().UTC()
}

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

	type repoTagRow struct {
		repoID   int64
		repoName string
		tagName  sql.NullString
	}
	rows, err := queryRows(ctx, s.db,
		`SELECT r.id, r.name, t.name
		 FROM repositories r
		 LEFT JOIN tags t ON t.repository_id = r.id`,
		func(r *sql.Rows, row *repoTagRow) error {
			return r.Scan(&row.repoID, &row.repoName, &row.tagName)
		})
	if err != nil {
		logger.Warnf(ctx, "list repositories: %v", err)
		return
	}

	type repoState struct {
		id   int64
		name string
		tags []string
	}
	repos := make(map[int64]*repoState)
	for _, row := range rows {
		r, ok := repos[row.repoID]
		if !ok {
			r = &repoState{id: row.repoID, name: row.repoName}
			repos[row.repoID] = r
		}
		if row.tagName.Valid {
			r.tags = append(r.tags, row.tagName.String)
		}
	}

	for _, r := range repos {
		catRepo, exists := cat.Repositories[r.name]
		if !exists {
			if _, delErr := s.db.ExecContext(ctx, `DELETE FROM repositories WHERE id = ?`, r.id); delErr != nil {
				logger.Warnf(ctx, "delete orphan repository %s: %v", r.name, delErr)
			}
			continue
		}
		for _, tagName := range r.tags {
			if _, ok := catRepo.Tags[tagName]; ok {
				continue
			}
			if _, delErr := s.db.ExecContext(ctx, `DELETE FROM tags WHERE repository_id = ? AND name = ?`, r.id, tagName); delErr != nil {
				logger.Warnf(ctx, "delete orphan tag %s:%s: %v", r.name, tagName, delErr)
			}
		}
	}
}
