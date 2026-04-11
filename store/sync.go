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

// indexFetchConcurrency caps the number of in-flight child-manifest fetches
// when expanding an OCI image index. Bounded so a 100-platform index does
// not spawn 100 simultaneous S3 round-trips and so the underlying object
// store client's connection pool stays predictable. 4 is a reasonable
// default for typical multi-arch images (linux/amd64 + linux/arm64 +
// optional attestations).
const indexFetchConcurrency = 4

// errSyncInProgress signals that another goroutine is already running
// SyncFromCatalog. Returned (and silently swallowed by background callers)
// instead of blocking on the mutex.
var errSyncInProgress = errors.New("sync already in progress")

// SyncFromCatalog walks the registry catalog and ingests every (repo, tag)
// into MySQL. Each tag's manifest is parsed as OCI and indexed by digest,
// artifactType, and aggregate size. Orphaned rows (repos / tags that no
// longer appear in the catalog) are deleted in a second pass.
//
// Sync is split into two phases so the write transaction never holds while
// epoch is talking to remote object storage:
//
//  1. prepareSync — read existing tag state from MySQL, fetch + parse +
//     aggregate every catalog tag from the registry. No write TX is open,
//     so the seconds spent on S3 round-trips do not block concurrent
//     writers (token last_used updates, tag deletes, etc).
//  2. commitSync — open one short write TX and upsert every prepared
//     repo / tag / blob. The lock window stays in milliseconds even when
//     phase 1 took seconds.
//
// Per-tag failures stay non-fatal so one bad manifest does not abort the
// whole pass. Context cancellation during phase 1 aborts the pass before
// any writes are issued.
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

// pendingTag is the in-memory result of [prepareSync] — everything
// [commitSync] needs to upsert one tag without doing any further remote I/O.
type pendingTag struct {
	repoName    string
	tag         Tag
	descriptors []manifest.Descriptor
}

// prepareSync iterates the catalog and produces one [pendingTag] per
// (repo, tag) that actually needs to be written. Unchanged tags (digest
// matches the cached row and no platform_sizes backfill is owed) are
// filtered out here so commitSync's TX stays minimal. No DB writes happen
// in this phase — the only DB touch is the per-tag read in getTagSyncState.
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

// prepareTag fetches a single manifest, parses it, and aggregates the
// totals. Returns ok=false when the tag is unchanged (fast path) or when
// fetching / parsing failed and was already logged. Lives entirely outside
// any TX so its remote round-trips do not hold a database lock.
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

	// Image-index tags synced before the platform_sizes column existed have
	// platform_sizes = NULL forever otherwise: their manifest digest is stable
	// upstream, so the unchanged-digest fast path below would skip them. Force
	// one resync per such tag to backfill the new column; subsequent runs hit
	// the fast path normally.
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

// commitSync writes every pending tag inside one short transaction.
// Repositories are upserted first (deduped) so each tag row has a parent
// ID; tag and blob upserts then run in catalog order. Per-tag failures
// stay non-fatal so one broken row does not roll back the whole pass.
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

// tagAggregates returns (totalSize, layerCount, descriptors, platformSizes)
// for the given manifest. For image manifests this is just config + layers
// and platformSizes is nil. For image indexes it walks each child manifest
// by digest, dedupes shared blobs across platforms for the totalSize union
// (most multi-arch images publish per-arch layers, but shared base layers
// and configs identical across arches collapse to a single counted byte),
// and additionally returns the per-child standalone size in platformSizes
// without that cross-platform dedup.
//
// layerCount for an index is the number of platform manifests it references
// (the natural answer to "how many things compose this tag" when the tag is
// multi-arch). Per-platform layer counts are carried inside platformSizes.
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

// expandIndexAggregates fetches every child manifest of an image index and
// returns the aggregated totals plus a per-child standalone size list. The
// totalSize dedupes shared blobs across platforms; the per-child entries in
// platformSizes do NOT — they answer "if I only pulled this platform, how
// big is it" and are what the UI surfaces in the Platforms table. Failures
// fetching a single child are logged and skipped — partial aggregates are
// better than refusing to sync the whole tag because one platform's manifest
// is unreachable.
//
// The fetch phase runs in parallel up to [indexFetchConcurrency] workers
// because a multi-arch index can otherwise serialize N S3 round-trips on a
// single sync pass. The aggregate phase is serialized to keep the descriptor
// ordering and dedup map race-free.
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

// fetchedChild carries one parallel-fetched child manifest of an index.
// Stored at a fixed slot in fetchIndexChildren's result slice so the
// aggregate walk preserves the original platform order from the index.
type fetchedChild struct {
	digest string
	parsed *manifest.OCIManifest
}

// fetchIndexChildren downloads and parses every child manifest of an image
// index in parallel under a fixed-size worker semaphore. Returns a slice
// where index i corresponds to children[i]; nil entries mean that child
// failed to fetch or parse and was already logged.
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

// tagSyncState captures the slice of an existing tag row that prepareTag
// needs to decide whether to skip recomputation. hasPlatformSizes is the
// NOT NULL presence flag (not the slice itself) because prepareTag only
// needs to know "is this row missing its index materialization" — pulling
// the actual JSON would be wasted I/O on every sync pass.
type tagSyncState struct {
	digest           string
	kind             string
	hasPlatformSizes bool
}

// getTagSyncState looks up the existing row for one (repoName, tagName)
// pair via a JOIN against repositories. Phase 1 of sync runs before any
// repository upsert has happened, so callers do not yet know the row's
// repository_id; the JOIN keeps the read path repoName-keyed throughout.
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

	// One LEFT JOIN replaces the previous N+1 (one SELECT-tags-per-repo) so
	// large registries pay a single round-trip for the whole orphan scan.
	// The LEFT JOIN keeps empty repositories in the result set so they can
	// still be garbage-collected when they disappear from the catalog; rows
	// for empty repos come back with a NULL tag name.
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
			// Tags are removed via ON DELETE CASCADE on repository_id, so the
			// per-tag loop below can skip orphan repos entirely.
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
