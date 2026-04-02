package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/projecteru2/core/log"

	"github.com/cocoonstack/epoch/utils"
)

// CreateToken generates and stores a new token. Returns the plaintext.
func (s *Store) CreateToken(ctx context.Context, name, createdBy string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	plaintext := hex.EncodeToString(raw)
	hash := utils.SHA256Hex([]byte(plaintext))
	_, err := s.db.ExecContext(ctx, `INSERT INTO tokens (name, token_hash, token_plain, created_by) VALUES (?, ?, ?, ?)`,
		name, hash, plaintext, createdBy)
	if err != nil {
		return "", err
	}
	return plaintext, nil
}

// ListTokens returns all tokens with plaintext visible.
func (s *Store) ListTokens(ctx context.Context) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, token_plain, created_by, created_at, last_used FROM tokens ORDER BY id`)
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
func (s *Store) DeleteToken(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE id = ?`, id)
	return err
}

// ValidateToken checks if a token exists. Updates last_used on match.
func (s *Store) ValidateToken(ctx context.Context, plaintext string) bool {
	hash := utils.SHA256Hex([]byte(plaintext))
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM tokens WHERE token_hash = ? LIMIT 1`, hash).Scan(&exists); err != nil {
		return false
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tokens SET last_used = NOW() WHERE token_hash = ?`, hash); err != nil {
		log.WithFunc("Store.ValidateToken").Warnf(ctx, "token last_used update failed: %v", err)
	}
	return true
}
