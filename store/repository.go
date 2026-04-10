package store

import (
	"context"
	"database/sql"
)

// repoSummarySelect aggregates a repository's tag count, deduped blob size,
// and the artifact_type of its most recently pushed tag in one query.
//
// The dedup matters because two tags pointing at the same manifest digest
// share the same set of blobs in object storage. Naively summing
// tags.total_size double-counts every byte the second tag references; the
// inner GROUP BY collapses tags by digest first, then sums.
//
// The latest-tag pick orders by pushed_at (the manifest's
// org.opencontainers.image.created annotation, or upsert time as fallback)
// rather than MAX(id) because tags are upserted in place (ON DUPLICATE KEY
// UPDATE), so a re-pushed tag keeps its original id. id breaks ties when
// two tags share a pushed_at timestamp.
const repoSummarySelect = `
SELECT r.id, r.name, r.created_at, r.updated_at,
       COALESCE(tc.cnt, 0)              AS tag_count,
       COALESCE(rs.size, 0)             AS total_size,
       COALESCE(lt.artifact_type, '')   AS artifact_type
FROM repositories r
LEFT JOIN (
    SELECT repository_id, COUNT(*) AS cnt
    FROM tags GROUP BY repository_id
) tc ON tc.repository_id = r.id
LEFT JOIN (
    SELECT repository_id, SUM(blob_size) AS size FROM (
        SELECT repository_id, digest, MAX(total_size) AS blob_size
        FROM tags GROUP BY repository_id, digest
    ) u GROUP BY repository_id
) rs ON rs.repository_id = r.id
LEFT JOIN (
    SELECT repository_id, artifact_type FROM (
        SELECT repository_id, artifact_type,
               ROW_NUMBER() OVER (PARTITION BY repository_id ORDER BY pushed_at DESC, id DESC) AS rn
        FROM tags
    ) ranked WHERE rn = 1
) lt ON lt.repository_id = r.id`

// --- Repository queries ---

func (s *Store) ListRepositories(ctx context.Context) ([]Repository, error) {
	return queryRows(ctx, s.db, repoSummarySelect+`
		ORDER BY r.updated_at DESC`, func(rows *sql.Rows, repo *Repository) error {
		if err := repo.scanSummary(rows); err != nil {
			return err
		}
		repo.Kind = artifactKindString(repo.ArtifactType)
		return nil
	})
}

func (s *Store) GetRepository(ctx context.Context, name string) (*Repository, error) {
	return queryOptional(func(repo *Repository) error {
		if err := repo.scanSummary(s.db.QueryRowContext(ctx, repoSummarySelect+`
			WHERE r.name = ?`, name)); err != nil {
			return err
		}
		repo.Kind = artifactKindString(repo.ArtifactType)
		return nil
	})
}

// --- Tag queries ---

func (s *Store) ListTags(ctx context.Context, repoName string) ([]Tag, error) {
	return queryRows(ctx, s.db, `
		SELECT t.id, t.repository_id, t.name, t.digest, t.artifact_type, t.total_size, t.layer_count, t.pushed_at, t.synced_at
		FROM tags t
		JOIN repositories r ON r.id = t.repository_id
		WHERE r.name = ?
		ORDER BY t.pushed_at DESC`, func(rows *sql.Rows, tag *Tag) error {
		tag.RepoName = repoName
		if err := tag.scanSummary(rows); err != nil {
			return err
		}
		tag.Kind = artifactKindString(tag.ArtifactType)
		return nil
	}, repoName)
}

func (s *Store) GetTag(ctx context.Context, repoName, tagName string) (*Tag, error) {
	return queryOptional(func(tag *Tag) error {
		tag.RepoName = repoName
		if err := tag.scanDetails(s.db.QueryRowContext(ctx, `
		SELECT t.id, t.repository_id, t.name, t.digest, t.artifact_type, t.manifest_json,
			t.total_size, t.layer_count, t.pushed_at, t.synced_at
		FROM tags t
		JOIN repositories r ON r.id = t.repository_id
		WHERE r.name = ? AND t.name = ?`, repoName, tagName)); err != nil {
			return err
		}
		tag.Kind = artifactKindString(tag.ArtifactType)
		return nil
	})
}

func (s *Store) DeleteTag(ctx context.Context, repoName, tagName string) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE t FROM tags t
		JOIN repositories r ON r.id = t.repository_id
		WHERE r.name = ? AND t.name = ?`, repoName, tagName)
	return err
}

// --- Dashboard ---

func (s *Store) GetStats(ctx context.Context) (*DashboardStats, error) {
	var st DashboardStats
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM repositories`).Scan(&st.RepositoryCount)
	if err != nil {
		return nil, err
	}
	// Tag count is the raw row count; total size dedups blobs by digest so
	// two tags pointing at the same manifest do not double-count storage.
	err = s.db.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM tags),
		       COALESCE((SELECT SUM(blob_size) FROM (
		           SELECT digest, MAX(total_size) AS blob_size FROM tags GROUP BY digest
		       ) u), 0)`).Scan(&st.TagCount, &st.TotalSize)
	if err != nil {
		return nil, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM blobs`).Scan(&st.BlobCount)
	if err != nil {
		return nil, err
	}
	return &st, nil
}
