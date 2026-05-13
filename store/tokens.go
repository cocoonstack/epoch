package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/projecteru2/core/log"

	"github.com/cocoonstack/epoch/utils"
)

const tokenCacheTTL = 30 * time.Second

type tokenCacheEntry struct {
	valid   bool
	expires time.Time
}

// CreateToken generates a random token, stores its hash, and returns the plaintext.
func (s *Store) CreateToken(ctx context.Context, name, createdBy string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	plaintext := hex.EncodeToString(raw)
	hash := utils.SHA256Hex([]byte(plaintext))
	_, err := s.db.ExecContext(ctx, `INSERT INTO tokens (name, token_hash, created_by) VALUES (?, ?, ?)`,
		name, hash, createdBy)
	if err != nil {
		return "", fmt.Errorf("insert token: %w", err)
	}
	return plaintext, nil
}

// ListTokens returns all tokens ordered by ID.
func (s *Store) ListTokens(ctx context.Context) ([]Token, error) {
	return queryRows(ctx, s.db, `SELECT id, name, created_by, created_at, last_used FROM tokens ORDER BY id`, func(rows *sql.Rows, t *Token) error {
		return t.scan(rows)
	})
}

// DeleteToken removes a token by ID and invalidates the cache.
//
// Order matters: the DB DELETE must happen BEFORE InvalidateTokenCache.
// If invalidation runs first, a concurrent ValidateToken can race in
// between (cache miss → DB SELECT → re-cache valid=true) and the token
// stays usable for up to tokenCacheTTL after the DELETE. Deleting first
// closes that window; the cache flush afterwards is idempotent.
func (s *Store) DeleteToken(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete token %d: %w", id, err)
	}
	s.InvalidateTokenCache()
	return nil
}

// ValidateToken checks whether a plaintext token is valid, using a cache.
//
// DB errors that are NOT sql.ErrNoRows (e.g. transient MySQL hiccups) are
// returned as false but NOT cached, so a flaky connection cannot lock
// legitimate users out for the full cache TTL. Only definitive lookups
// (found or not-found) are persisted to the cache.
func (s *Store) ValidateToken(ctx context.Context, plaintext string) bool {
	logger := log.WithFunc("store.ValidateToken")
	hash := utils.SHA256Hex([]byte(plaintext))

	if entry, ok := s.tokenCache.Load(hash); ok {
		if ce, ok := entry.(tokenCacheEntry); ok && time.Now().Before(ce.expires) {
			return ce.valid
		}
	}

	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM tokens WHERE token_hash = ? LIMIT 1`, hash).Scan(&exists)
	switch {
	case err == nil:
		s.tokenCache.Store(hash, tokenCacheEntry{valid: true, expires: time.Now().Add(tokenCacheTTL)})
		bgCtx := context.WithoutCancel(ctx)
		go func() {
			if _, updateErr := s.db.ExecContext(bgCtx, `UPDATE tokens SET last_used = NOW() WHERE token_hash = ?`, hash); updateErr != nil {
				logger.Warnf(bgCtx, "token last_used update failed: %v", updateErr)
			}
		}()
		return true
	case errors.Is(err, sql.ErrNoRows):
		s.tokenCache.Store(hash, tokenCacheEntry{valid: false, expires: time.Now().Add(tokenCacheTTL)})
		return false
	default:
		// Transient DB failure — reject this attempt but do not cache the
		// negative result, otherwise a flaky connection locks the token
		// out for the full TTL.
		logger.Warnf(ctx, "token validation query failed: %v", err)
		return false
	}
}

// InvalidateTokenCache clears all cached token validation results.
func (s *Store) InvalidateTokenCache() {
	s.tokenCache.Range(func(key, _ any) bool {
		s.tokenCache.Delete(key)
		return true
	})
}

func (s *Store) startTokenCacheCleanup(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(tokenCacheTTL)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := time.Now()
				s.tokenCache.Range(func(key, value any) bool {
					if ce, ok := value.(tokenCacheEntry); ok && now.After(ce.expires) {
						s.tokenCache.Delete(key)
					}
					return true
				})
			}
		}
	}()
}
