package store

import (
	"context"
	"database/sql"
)

// --- Repository queries ---

func (s *Store) ListRepositories(ctx context.Context) ([]Repository, error) {
	return queryRows(ctx, s.db, `
		SELECT r.id, r.name, r.created_at, r.updated_at,
			COUNT(t.id) AS tag_count,
			COALESCE(SUM(t.total_size), 0) AS total_size
		FROM repositories r
		LEFT JOIN tags t ON t.repository_id = r.id
		GROUP BY r.id
		ORDER BY r.updated_at DESC`, func(rows *sql.Rows, repo *Repository) error {
		return repo.scanSummary(rows)
	})
}

func (s *Store) GetRepository(ctx context.Context, name string) (*Repository, error) {
	return queryOptional(func(repo *Repository) error {
		return repo.scanSummary(s.db.QueryRowContext(ctx, `
		SELECT r.id, r.name, r.created_at, r.updated_at,
			COUNT(t.id) AS tag_count,
			COALESCE(SUM(t.total_size), 0) AS total_size
		FROM repositories r
		LEFT JOIN tags t ON t.repository_id = r.id
		WHERE r.name = ?
		GROUP BY r.id`, name))
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
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(total_size), 0) FROM tags`).Scan(&st.TagCount, &st.TotalSize)
	if err != nil {
		return nil, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM blobs`).Scan(&st.BlobCount)
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// --- Unexported helpers ---
