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

	pending, degraded, err := s.prepareSync(ctx, cat, reg)
	if err != nil {
		return err
	}
	if err := s.commitSync(ctx, pending); err != nil {
		return err
	}

	s.cleanOrphans(ctx, cat)
	// Rebuild the blobs index from the authoritative descriptor set. Skipped on
	// degraded syncs (any tag fetch failed) to avoid pruning blobs whose tags
	// are temporarily unreachable.
	if !degraded {
		s.reconcileBlobs(ctx, pending)
	}
	s.lastCatalogHash = digest
	return nil
}

type pendingTag struct {
	repoName    string
	tag         Tag
	descriptors []manifest.Descriptor
	needsCommit bool
}

func (s *Store) prepareSync(ctx context.Context, cat *manifest.Catalog, reg *registry.Registry) ([]pendingTag, bool, error) {
	var pending []pendingTag
	degraded := false
	for repoName, repo := range cat.Repositories {
		if err := ctx.Err(); err != nil {
			return nil, degraded, err
		}
		for _, tagName := range slices.Sorted(maps.Keys(repo.Tags)) {
			p, ok := s.prepareTag(ctx, reg, repoName, tagName)
			if !ok {
				degraded = true
				continue
			}
			pending = append(pending, p)
		}
	}
	return pending, degraded, nil
}

func (s *Store) prepareTag(ctx context.Context, reg *registry.Registry, repoName, tagName string) (pendingTag, bool) {
	logger := log.WithFunc("store.prepareTag")
	existing, dbErr := s.getTagSyncState(ctx, repoName, tagName)
	if dbErr != nil && !errors.Is(dbErr, sql.ErrNoRows) {
		logger.Errorf(ctx, dbErr, "lookup existing digest for %s:%s", repoName, tagName)
	}

	raw, err := reg.ManifestJSON(ctx, repoName, tagName)
	if err != nil {
		logger.Errorf(ctx, err, "fetch manifest %s:%s", repoName, tagName)
		return pendingTag{}, false
	}

	digest := utils.SHA256Hex(raw)
	m, err := manifest.Parse(raw)
	if err != nil {
		logger.Errorf(ctx, err, "decode manifest %s:%s", repoName, tagName)
		return pendingTag{}, false
	}

	kind := manifest.ClassifyParsed(m)
	needsPlatformBackfill := existing.kind == manifest.KindImageIndex.String() && !existing.hasPlatformSizes
	needsCommit := digest != existing.digest || needsPlatformBackfill

	totalSize, layerCount, descriptors, platformSizes := tagAggregates(ctx, reg, repoName, m, kind)

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
		needsCommit: needsCommit,
	}, true
}

func (s *Store) commitSync(ctx context.Context, pending []pendingTag) error {
	if len(pending) == 0 {
		return nil
	}
	logger := log.WithFunc("store.commitSync")
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
			logger.Errorf(ctx, err, "upsert repo %s", p.repoName)
			continue
		}
		repoIDs[p.repoName] = repoID
	}

	for _, p := range pending {
		if !p.needsCommit {
			continue
		}
		repoID, ok := repoIDs[p.repoName]
		if !ok {
			continue
		}
		if err := upsertTagTx(ctx, tx, repoID, p.tag); err != nil {
			logger.Errorf(ctx, err, "upsert tag %s:%s", p.repoName, p.tag.Name)
			continue
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// tagAggregates returns totals for a manifest; for indexes, dedupes across platforms.
func tagAggregates(ctx context.Context, reg *registry.Registry, repoName string, m *manifest.OCIManifest, kind manifest.Kind) (int64, int, []manifest.Descriptor, PlatformSizes) {
	if kind == manifest.KindImageIndex {
		return expandIndexAggregates(ctx, reg, repoName, m)
	}
	totalSize := m.Config.Size
	for _, layer := range m.Layers {
		totalSize += layer.Size
	}
	descriptors := slices.Concat([]manifest.Descriptor{m.Config}, m.Layers)
	return totalSize, len(m.Layers), descriptors, nil
}

func expandIndexAggregates(ctx context.Context, reg *registry.Registry, repoName string, m *manifest.OCIManifest) (int64, int, []manifest.Descriptor, PlatformSizes) {
	fetched := fetchIndexChildren(ctx, reg, repoName, m.Manifests)

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

func fetchIndexChildren(ctx context.Context, reg *registry.Registry, repoName string, children []manifest.IndexManifest) []*fetchedChild {
	results := make([]*fetchedChild, len(children))
	if len(children) == 0 {
		return results
	}

	logger := log.WithFunc("store.fetchIndexChildren")
	sem := make(chan struct{}, indexFetchConcurrency)
	var wg sync.WaitGroup
	for i, child := range children {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()

			raw, err := reg.ManifestJSONByDigest(ctx, repoName, child.Digest)
			if err != nil {
				logger.Errorf(ctx, err, "fetch index child %s@%s", repoName, child.Digest)
				return
			}
			parsed, err := manifest.Parse(raw)
			if err != nil {
				logger.Errorf(ctx, err, "parse index child %s@%s", repoName, child.Digest)
				return
			}
			results[i] = &fetchedChild{digest: child.Digest, parsed: parsed}
		})
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

type blobAggregate struct {
	size      int64
	mediaType string
	refCount  int
}

// aggregateBlobs collapses descriptors from all pending tags into one row per
// digest, counting how many tags reference each blob.
func aggregateBlobs(pending []pendingTag) map[string]blobAggregate {
	out := make(map[string]blobAggregate)
	for _, p := range pending {
		seen := make(map[string]bool, len(p.descriptors))
		for _, d := range p.descriptors {
			if d.Digest == "" || seen[d.Digest] {
				continue
			}
			seen[d.Digest] = true
			key := strings.TrimPrefix(d.Digest, "sha256:")
			agg := out[key]
			agg.size = d.Size
			agg.mediaType = d.MediaType
			agg.refCount++
			out[key] = agg
		}
	}
	return out
}

// reconcileBlobs rewrites the blobs table to match the authoritative set
// derived from current catalog tags. Runs in one transaction: truncate, then
// bulk insert. Dashboard COUNT(*) FROM blobs becomes consistent with
// actually-referenced blobs after every sync.
func (s *Store) reconcileBlobs(ctx context.Context, pending []pendingTag) {
	logger := log.WithFunc("store.reconcileBlobs")
	aggregates := aggregateBlobs(pending)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		logger.Errorf(ctx, err, "begin tx")
		return
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM blobs`); err != nil {
		logger.Errorf(ctx, err, "clear blobs")
		return
	}
	if err := bulkInsertBlobsTx(ctx, tx, aggregates); err != nil {
		logger.Errorf(ctx, err, "bulk insert blobs")
		return
	}
	if err := tx.Commit(); err != nil {
		logger.Errorf(ctx, err, "commit reconcile")
	}
}

func bulkInsertBlobsTx(ctx context.Context, tx *sql.Tx, aggregates map[string]blobAggregate) error {
	if len(aggregates) == 0 {
		return nil
	}
	const batchSize = 500
	type row struct {
		digest string
		agg    blobAggregate
	}
	rows := make([]row, 0, len(aggregates))
	for digest, agg := range aggregates {
		rows = append(rows, row{digest: digest, agg: agg})
	}
	for batch := range slices.Chunk(rows, batchSize) {
		var sb strings.Builder
		sb.WriteString(`INSERT INTO blobs (digest, size, media_type, ref_count) VALUES `)
		args := make([]any, 0, len(batch)*4)
		for j, r := range batch {
			if j > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("(?,?,?,?)")
			args = append(args, r.digest, r.agg.size, r.agg.mediaType, r.agg.refCount)
		}
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
		logger.Error(ctx, err, "list repositories")
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
				logger.Errorf(ctx, delErr, "delete orphan repository %s", r.name)
			}
			continue
		}
		for _, tagName := range r.tags {
			if _, ok := catRepo.Tags[tagName]; ok {
				continue
			}
			if _, delErr := s.db.ExecContext(ctx, `DELETE FROM tags WHERE repository_id = ? AND name = ?`, r.id, tagName); delErr != nil {
				logger.Errorf(ctx, delErr, "delete orphan tag %s:%s", r.name, tagName)
			}
		}
	}
}
