package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"time"

	"github.com/projecteru2/core/log"

	"github.com/cocoonstack/epoch/utils"
)

const tokenCacheTTL = 30 * time.Second

type tokenCacheEntry struct {
	valid   bool
	expires time.Time
}

func (s *Store) CreateToken(ctx context.Context, name, createdBy string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	plaintext := hex.EncodeToString(raw)
	hash := utils.SHA256Hex([]byte(plaintext))
	_, err := s.db.ExecContext(ctx, `INSERT INTO tokens (name, token_hash, created_by) VALUES (?, ?, ?)`,
		name, hash, createdBy)
	if err != nil {
		return "", err
	}
	return plaintext, nil
}

func (s *Store) ListTokens(ctx context.Context) ([]Token, error) {
	return queryRows(ctx, s.db, `SELECT id, name, created_by, created_at, last_used FROM tokens ORDER BY id`, func(rows *sql.Rows, t *Token) error {
		return t.scan(rows)
	})
}

func (s *Store) DeleteToken(ctx context.Context, id int64) error {
	s.InvalidateTokenCache()
	_, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE id = ?`, id)
	return err
}

func (s *Store) ValidateToken(ctx context.Context, plaintext string) bool {
	logger := log.WithFunc("store.ValidateToken")
	hash := utils.SHA256Hex([]byte(plaintext))

	if entry, ok := s.tokenCache.Load(hash); ok {
		if ce, ok := entry.(tokenCacheEntry); ok && time.Now().Before(ce.expires) {
			return ce.valid
		}
	}

	var exists int
	valid := s.db.QueryRowContext(ctx, `SELECT 1 FROM tokens WHERE token_hash = ? LIMIT 1`, hash).Scan(&exists) == nil
	s.tokenCache.Store(hash, tokenCacheEntry{valid: valid, expires: time.Now().Add(tokenCacheTTL)})

	if valid {
		bgCtx := context.WithoutCancel(ctx)
		go func() {
			if _, err := s.db.ExecContext(bgCtx, `UPDATE tokens SET last_used = NOW() WHERE token_hash = ?`, hash); err != nil {
				logger.Warnf(bgCtx, "token last_used update failed: %v", err)
			}
		}()
	}
	return valid
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

func (s *Store) InvalidateTokenCache() {
	s.tokenCache.Range(func(key, _ any) bool {
		s.tokenCache.Delete(key)
		return true
	})
}
