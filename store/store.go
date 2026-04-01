// Package store provides MySQL-backed metadata storage for the Epoch control plane.
//
// MySQL serves as an index/cache over the object-store-backed registry.
// Object storage remains the source of truth for blobs; MySQL provides queryable metadata.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/cocoonstack/epoch/internal/util"
	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/registry"
)

// Store wraps a MySQL connection for Epoch metadata.
type Store struct {
	db *sql.DB
	mu sync.Mutex // guards SyncFromCatalog
}

// New opens a MySQL connection and runs migrations.
func New(dsn string) (*Store, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping mysql: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS repositories (
			id         BIGINT AUTO_INCREMENT PRIMARY KEY,
			name       VARCHAR(255) NOT NULL UNIQUE,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
			INDEX idx_name (name)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS tags (
			id              BIGINT AUTO_INCREMENT PRIMARY KEY,
			repository_id   BIGINT NOT NULL,
			name            VARCHAR(255) NOT NULL,
			digest          CHAR(64) NOT NULL,
			manifest_json   MEDIUMTEXT NOT NULL,
			total_size      BIGINT NOT NULL DEFAULT 0,
			layer_count     INT NOT NULL DEFAULT 0,
			pushed_at       TIMESTAMP NULL,
			synced_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE KEY uk_repo_tag (repository_id, name),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE,
			INDEX idx_digest (digest)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS blobs (
			digest     CHAR(64) PRIMARY KEY,
			size       BIGINT NOT NULL DEFAULT 0,
			media_type VARCHAR(127) NOT NULL DEFAULT 'application/octet-stream',
			ref_count  INT NOT NULL DEFAULT 0
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS tokens (
			id          BIGINT AUTO_INCREMENT PRIMARY KEY,
			name        VARCHAR(255) NOT NULL,
			token_hash  CHAR(64) NOT NULL UNIQUE,
			token_plain VARCHAR(255) NOT NULL DEFAULT '',
			created_by  VARCHAR(255) NOT NULL DEFAULT '',
			created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_used   TIMESTAMP NULL,
			INDEX idx_hash (token_hash)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("exec DDL: %w", err)
		}
	}
	// Migration: add token_plain column (ignore error if already exists).
	_, _ = s.db.Exec(`ALTER TABLE tokens ADD COLUMN token_plain VARCHAR(255) NOT NULL DEFAULT '' AFTER token_hash`)
	return nil
}

// --- Models ---

// Repository is a DB repository record.
type Repository struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	TagCount  int       `json:"tagCount"`
	TotalSize int64     `json:"totalSize"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Tag is a DB tag record.
type Tag struct {
	ID           int64     `json:"id"`
	RepositoryID int64     `json:"-"`
	RepoName     string    `json:"repoName,omitempty"`
	Name         string    `json:"name"`
	Digest       string    `json:"digest"`
	ManifestJSON string    `json:"-"`
	TotalSize    int64     `json:"totalSize"`
	LayerCount   int       `json:"layerCount"`
	PushedAt     time.Time `json:"pushedAt"`
	SyncedAt     time.Time `json:"syncedAt"`
}

// Blob is a DB blob record.
type Blob struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
	RefCount  int    `json:"refCount"`
}

// DashboardStats holds aggregate stats for the UI dashboard.
type DashboardStats struct {
	RepositoryCount int   `json:"repositoryCount"`
	TagCount        int   `json:"tagCount"`
	BlobCount       int   `json:"blobCount"`
	TotalSize       int64 `json:"totalSize"`
}

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
	if err == sql.ErrNoRows {
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
	if err == sql.ErrNoRows {
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

// --- Sync ---

// SyncFromCatalog reads the remote catalog and syncs all metadata into MySQL.
func (s *Store) SyncFromCatalog(ctx context.Context, reg *registry.Registry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cat, err := reg.GetCatalog(ctx)
	if err != nil {
		return fmt.Errorf("get catalog: %w", err)
	}

	for repoName, repo := range cat.Repositories {
		repoID, err := s.upsertRepository(ctx, repoName)
		if err != nil {
			log.Printf("[sync] upsert repo %s: %v", repoName, err)
			continue
		}
		for tagName := range repo.Tags {
			if err := s.syncTag(ctx, reg, repoID, repoName, tagName); err != nil {
				log.Printf("[sync] sync tag %s:%s: %v", repoName, tagName, err)
			}
		}
	}

	// Clean up repos/tags no longer in catalog.
	s.cleanOrphans(ctx, cat)
	return nil
}

func (s *Store) syncTag(ctx context.Context, reg *registry.Registry, repoID int64, repoName, tagName string) error {
	m, err := reg.PullManifest(ctx, repoName, tagName)
	if err != nil {
		return err
	}

	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	digest := util.SHA256Hex(data)

	t := Tag{
		Name:         tagName,
		Digest:       digest,
		ManifestJSON: string(data),
		TotalSize:    m.TotalSize,
		LayerCount:   len(m.Layers) + len(m.BaseImages),
		PushedAt:     m.PushedAt,
	}
	if err := s.upsertTag(ctx, repoID, t); err != nil {
		return err
	}

	// Upsert blobs.
	allLayers := make([]manifest.Layer, 0, len(m.Layers)+len(m.BaseImages))
	allLayers = append(allLayers, m.Layers...)
	allLayers = append(allLayers, m.BaseImages...)
	for _, layer := range allLayers {
		if err := s.upsertBlob(ctx, Blob{
			Digest:    layer.Digest,
			Size:      layer.Size,
			MediaType: layer.MediaType,
		}); err != nil {
			log.Printf("[sync] upsert blob %s: %v", layer.Digest[:12], err)
		}
	}
	return nil
}

func (s *Store) cleanOrphans(ctx context.Context, cat *manifest.Catalog) {
	// Get all repo names from DB.
	rows, err := s.db.QueryContext(ctx, `SELECT id, name FROM repositories`)
	if err != nil {
		return
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			continue
		}
		repo, exists := cat.Repositories[name]
		if !exists {
			_, _ = s.db.ExecContext(ctx, `DELETE FROM repositories WHERE id = ?`, id)
			continue
		}
		// Clean orphan tags.
		tagRows, err := s.db.QueryContext(ctx, `SELECT name FROM tags WHERE repository_id = ?`, id)
		if err != nil {
			continue
		}
		for tagRows.Next() {
			var tagName string
			if err := tagRows.Scan(&tagName); err != nil {
				continue
			}
			if _, ok := repo.Tags[tagName]; !ok {
				_, _ = s.db.ExecContext(ctx, `DELETE FROM tags WHERE repository_id = ? AND name = ?`, id, tagName)
			}
		}
		_ = tagRows.Close()
	}
}

// --- Token Management ---

// Token is a registry access token.
type Token struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Token     string     `json:"token"`
	CreatedBy string     `json:"createdBy"`
	CreatedAt time.Time  `json:"createdAt"`
	LastUsed  *time.Time `json:"lastUsed,omitempty"`
}

// CreateToken generates and stores a new token. Returns the plaintext.
func (s *Store) CreateToken(name, createdBy string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	plaintext := hex.EncodeToString(raw)
	hash := util.SHA256Hex([]byte(plaintext))
	_, err := s.db.Exec(`INSERT INTO tokens (name, token_hash, token_plain, created_by) VALUES (?, ?, ?, ?)`,
		name, hash, plaintext, createdBy)
	if err != nil {
		return "", err
	}
	return plaintext, nil
}

// ListTokens returns all tokens with plaintext visible.
func (s *Store) ListTokens() ([]Token, error) {
	rows, err := s.db.Query(`SELECT id, name, token_plain, created_by, created_at, last_used FROM tokens ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var tokens []Token
	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.ID, &t.Name, &t.Token, &t.CreatedBy, &t.CreatedAt, &t.LastUsed); err != nil {
			continue
		}
		tokens = append(tokens, t)
	}
	if tokens == nil {
		tokens = []Token{}
	}
	return tokens, nil
}

// DeleteToken removes a token by ID.
func (s *Store) DeleteToken(id int64) error {
	_, err := s.db.Exec(`DELETE FROM tokens WHERE id = ?`, id)
	return err
}

// ValidateToken checks if a token exists. Updates last_used on match.
func (s *Store) ValidateToken(plaintext string) bool {
	hash := util.SHA256Hex([]byte(plaintext))
	var exists int
	if err := s.db.QueryRow(`SELECT 1 FROM tokens WHERE token_hash = ? LIMIT 1`, hash).Scan(&exists); err != nil {
		return false
	}
	if _, err := s.db.Exec(`UPDATE tokens SET last_used = NOW() WHERE token_hash = ?`, hash); err != nil {
		log.Printf("[store] token last_used update failed: %v", err)
	}
	return true
}
