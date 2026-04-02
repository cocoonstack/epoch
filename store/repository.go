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

func (s *Store) upsertRepository(ctx context.Context, name string) (int64, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO repositories (name) VALUES (?) ON DUPLICATE KEY UPDATE updated_at=NOW()`, name)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRowContext(ctx, `SELECT id FROM repositories WHERE name = ?`, name).Scan(&id)
	return id, err
}

// --- Tag queries ---

func (s *Store) ListTags(ctx context.Context, repoName string) ([]Tag, error) {
	return queryRows(ctx, s.db, `
		SELECT t.id, t.repository_id, t.name, t.digest, t.total_size, t.layer_count, t.pushed_at, t.synced_at
		FROM tags t
		JOIN repositories r ON r.id = t.repository_id
		WHERE r.name = ?
		ORDER BY t.pushed_at DESC`, func(rows *sql.Rows, tag *Tag) error {
		tag.RepoName = repoName
		return tag.scanSummary(rows)
	}, repoName)
}

func (s *Store) GetTag(ctx context.Context, repoName, tagName string) (*Tag, error) {
	return queryOptional(func(tag *Tag) error {
		tag.RepoName = repoName
		return tag.scanDetails(s.db.QueryRowContext(ctx, `
		SELECT t.id, t.repository_id, t.name, t.digest, t.manifest_json,
			t.total_size, t.layer_count, t.pushed_at, t.synced_at
		FROM tags t
		JOIN repositories r ON r.id = t.repository_id
		WHERE r.name = ? AND t.name = ?`, repoName, tagName))
	})
}

func (s *Store) upsertTag(ctx context.Context, repoID int64, t Tag) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tags (repository_id, name, digest, manifest_json, total_size, layer_count, pushed_at, synced_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, NOW())
		ON DUPLICATE KEY UPDATE
			digest=VALUES(digest), manifest_json=VALUES(manifest_json),
			total_size=VALUES(total_size), layer_count=VALUES(layer_count),
			pushed_at=VALUES(pushed_at), synced_at=NOW()`,
		repoID, t.Name, t.Digest, t.ManifestJSON, t.TotalSize, t.LayerCount, t.PushedAt)
	return err
}

func (s *Store) DeleteTag(ctx context.Context, repoName, tagName string) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE t FROM tags t
		JOIN repositories r ON r.id = t.repository_id
		WHERE r.name = ? AND t.name = ?`, repoName, tagName)
	return err
}

// --- Blob queries ---

func (s *Store) upsertBlob(ctx context.Context, b Blob) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO blobs (digest, size, media_type, ref_count)
		VALUES (?, ?, ?, 1)
		ON DUPLICATE KEY UPDATE ref_count=ref_count+1`, b.Digest, b.Size, b.MediaType)
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
