package store

import (
	"context"
	"database/sql"
)

// repoSummarySelect aggregates tag count, deduped blob size, and the latest tag's artifact_type/kind.
const repoSummarySelect = `
SELECT r.id, r.name, r.created_at, r.updated_at,
       COALESCE(tc.cnt, 0)              AS tag_count,
       COALESCE(rs.size, 0)             AS total_size,
       COALESCE(lt.artifact_type, '')   AS artifact_type,
       COALESCE(lt.kind, '')            AS kind
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
    SELECT repository_id, artifact_type, kind FROM (
        SELECT repository_id, artifact_type, kind,
               ROW_NUMBER() OVER (PARTITION BY repository_id ORDER BY pushed_at DESC, id DESC) AS rn
        FROM tags
    ) ranked WHERE rn = 1
) lt ON lt.repository_id = r.id`

// ListRepositories returns all repositories ordered by last update.
func (s *Store) ListRepositories(ctx context.Context) ([]Repository, error) {
	return queryRows(ctx, s.db, repoSummarySelect+`
		ORDER BY r.updated_at DESC`, func(rows *sql.Rows, repo *Repository) error {
		return repo.scanSummary(rows)
	})
}

// GetRepository returns a single repository by name, or nil if not found.
func (s *Store) GetRepository(ctx context.Context, name string) (*Repository, error) {
	return queryOptional(func(repo *Repository) error {
		return repo.scanSummary(s.db.QueryRowContext(ctx, repoSummarySelect+`
			WHERE r.name = ?`, name))
	})
}

// ListTags returns all tags for a repository ordered by push time.
func (s *Store) ListTags(ctx context.Context, repoName string) ([]Tag, error) {
	return queryRows(ctx, s.db, `
		SELECT t.id, t.repository_id, t.name, t.digest, t.artifact_type, t.kind, t.total_size, t.layer_count, t.pushed_at, t.synced_at
		FROM tags t
		JOIN repositories r ON r.id = t.repository_id
		WHERE r.name = ?
		ORDER BY t.pushed_at DESC`, func(rows *sql.Rows, tag *Tag) error {
		tag.RepoName = repoName
		return tag.scanSummary(rows)
	}, repoName)
}

// GetTag returns a single tag with full details, or nil if not found.
func (s *Store) GetTag(ctx context.Context, repoName, tagName string) (*Tag, error) {
	return queryOptional(func(tag *Tag) error {
		tag.RepoName = repoName
		return tag.scanDetails(s.db.QueryRowContext(ctx, `
		SELECT t.id, t.repository_id, t.name, t.digest, t.artifact_type, t.kind, t.manifest_json,
			t.total_size, t.layer_count, t.platform_sizes, t.pushed_at, t.synced_at
		FROM tags t
		JOIN repositories r ON r.id = t.repository_id
		WHERE r.name = ? AND t.name = ?`, repoName, tagName))
	})
}

// DeleteTag removes a tag from the database by repository name and tag name.
func (s *Store) DeleteTag(ctx context.Context, repoName, tagName string) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE t FROM tags t
		JOIN repositories r ON r.id = t.repository_id
		WHERE r.name = ? AND t.name = ?`, repoName, tagName)
	return err
}

// GetStats returns aggregate counts for the dashboard.
func (s *Store) GetStats(ctx context.Context) (*DashboardStats, error) {
	var st DashboardStats
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM repositories`).Scan(&st.RepositoryCount)
	if err != nil {
		return nil, err
	}
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
