// Package store provides MySQL-backed metadata storage for the Epoch control plane.
//
// MySQL serves as an index/cache over the object-store-backed registry.
// Object storage remains the source of truth for blobs; MySQL provides queryable metadata.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// Store wraps a MySQL connection for Epoch metadata.
type Store struct {
	db              *sql.DB
	mu              sync.Mutex // guards SyncFromCatalog
	tokenCache      sync.Map   // hash → tokenCacheEntry
	lastCatalogHash string     // digest of last synced catalog.json
}

// New opens a MySQL connection and runs migrations.
func New(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping mysql: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	s.startTokenCacheCleanup(ctx)
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
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
			created_by  VARCHAR(255) NOT NULL DEFAULT '',
			created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_used   TIMESTAMP NULL,
			INDEX idx_hash (token_hash)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("exec DDL: %w", err)
		}
	}
	// Migration: drop token_plain column (ignore error if already dropped).
	_, _ = s.db.ExecContext(ctx, `ALTER TABLE tokens DROP COLUMN token_plain`)
	return nil
}
