package store

import (
	"context"
	"database/sql"
	"errors"
)

// --- Repository queries ---

func (s *Store) ListRepositories(ctx context.Context) ([]Repository, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.name, r.created_at, r.updated_at,
			COUNT(t.id) AS tag_count,
			COALESCE(SUM(t.total_size), 0) AS total_size
		FROM repositories r
		LEFT JOIN tags t ON t.repository_id = r.id
		GROUP BY r.id
		ORDER BY r.updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var repos []Repository
	for rows.Next() {
		var r Repository
		if err := rows.Scan(&r.ID, &r.Name, &r.CreatedAt, &r.UpdatedAt, &r.TagCount, &r.TotalSize); err != nil {
			return nil, err
		}
		repos = append(repos, r)
	}
	return repos, rows.Err()
}

func (s *Store) GetRepository(ctx context.Context, name string) (*Repository, error) {
	var r Repository
	err := s.db.QueryRowContext(ctx, `
		SELECT r.id, r.name, r.created_at, r.updated_at,
			COUNT(t.id) AS tag_count,
			COALESCE(SUM(t.total_size), 0) AS total_size
		FROM repositories r
		LEFT JOIN tags t ON t.repository_id = r.id
		WHERE r.name = ?
		GROUP BY r.id`, name).Scan(&r.ID, &r.Name, &r.CreatedAt, &r.UpdatedAt, &r.TagCount, &r.TotalSize)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &r, err
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
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.repository_id, t.name, t.digest, t.total_size, t.layer_count, t.pushed_at, t.synced_at
		FROM tags t
		JOIN repositories r ON r.id = t.repository_id
		WHERE r.name = ?
		ORDER BY t.pushed_at DESC`, repoName)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var tags []Tag
	for rows.Next() {
		var t Tag
		t.RepoName = repoName
		if err := rows.Scan(&t.ID, &t.RepositoryID, &t.Name, &t.Digest, &t.TotalSize, &t.LayerCount, &t.PushedAt, &t.SyncedAt); err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

func (s *Store) GetTag(ctx context.Context, repoName, tagName string) (*Tag, error) {
	var t Tag
	t.RepoName = repoName
	err := s.db.QueryRowContext(ctx, `
		SELECT t.id, t.repository_id, t.name, t.digest, t.manifest_json,
			t.total_size, t.layer_count, t.pushed_at, t.synced_at
		FROM tags t
		JOIN repositories r ON r.id = t.repository_id
		WHERE r.name = ? AND t.name = ?`, repoName, tagName).
		Scan(&t.ID, &t.RepositoryID, &t.Name, &t.Digest, &t.ManifestJSON,
			&t.TotalSize, &t.LayerCount, &t.PushedAt, &t.SyncedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &t, err
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
