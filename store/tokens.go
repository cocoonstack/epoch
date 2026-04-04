package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
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
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, created_by, created_at, last_used FROM tokens ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var tokens []Token
	for rows.Next() {
		var t Token
		if err := t.scan(rows); err != nil {
			return nil, fmt.Errorf("scan token: %w", err)
		}
		tokens = append(tokens, t)
	}
	if tokens == nil {
		tokens = []Token{}
	}
	return tokens, nil
}

func (s *Store) DeleteToken(ctx context.Context, id int64) error {
	s.tokenCache.Range(func(key, _ any) bool {
		s.tokenCache.Delete(key)
		return true
	})
	_, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE id = ?`, id)
	return err
}

func (s *Store) ValidateToken(ctx context.Context, plaintext string) bool {
	hash := utils.SHA256Hex([]byte(plaintext))

	if entry, ok := s.tokenCache.Load(hash); ok {
		if ce := entry.(tokenCacheEntry); time.Now().Before(ce.expires) {
			return ce.valid
		}
	}

	var exists int
	valid := s.db.QueryRowContext(ctx, `SELECT 1 FROM tokens WHERE token_hash = ? LIMIT 1`, hash).Scan(&exists) == nil
	s.tokenCache.Store(hash, tokenCacheEntry{valid: valid, expires: time.Now().Add(tokenCacheTTL)})

	if valid {
		go func() {
			if _, err := s.db.ExecContext(context.WithoutCancel(ctx), `UPDATE tokens SET last_used = NOW() WHERE token_hash = ?`, hash); err != nil {
				log.WithFunc("store.ValidateToken").Warnf(context.WithoutCancel(ctx), "token last_used update failed: %v", err)
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
					if now.After(value.(tokenCacheEntry).expires) {
						s.tokenCache.Delete(key)
					}
					return true
				})
			}
		}
	}()
}

// InvalidateTokenCache clears all cached token validations.
func (s *Store) InvalidateTokenCache() {
	s.tokenCache = sync.Map{}
}
