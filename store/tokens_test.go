package store

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/cocoonstack/epoch/utils"
)

func TestListTokensReturnsScanError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	s := &Store{db: db}
	mock.ExpectQuery("SELECT id, name, created_by, created_at, last_used FROM tokens ORDER BY id").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))

	if _, err := s.ListTokens(t.Context()); err == nil {
		t.Fatalf("ListTokens returned nil error for malformed row")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("ExpectationsWereMet: %v", err)
	}
}

// TestValidateTokenDBErrorNotCached asserts that a transient DB failure
// returns false but does NOT cache the result, so the next call retries
// the DB instead of locking the token out for the full TTL.
func TestValidateTokenDBErrorNotCached(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	s := &Store{db: db}
	plaintext := "abc123"
	hash := utils.SHA256Hex([]byte(plaintext))

	dbErr := errors.New("mysql: connection reset")
	mock.ExpectQuery("SELECT 1 FROM tokens WHERE token_hash = ?").
		WithArgs(hash).
		WillReturnError(dbErr)

	if s.ValidateToken(t.Context(), plaintext) {
		t.Fatalf("ValidateToken returned true on DB error")
	}
	if _, ok := s.tokenCache.Load(hash); ok {
		t.Fatalf("DB error was cached; subsequent retries would be locked out")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("ExpectationsWereMet: %v", err)
	}
}

// TestValidateTokenNotFoundCached asserts that a definitive "not found"
// result IS cached, so brute-force probes do not hit the DB on every
// attempt.
func TestValidateTokenNotFoundCached(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	s := &Store{db: db}
	plaintext := "missing-token"
	hash := utils.SHA256Hex([]byte(plaintext))

	mock.ExpectQuery("SELECT 1 FROM tokens WHERE token_hash = ?").
		WithArgs(hash).
		WillReturnRows(sqlmock.NewRows([]string{"exists"})) // empty -> sql.ErrNoRows

	if s.ValidateToken(t.Context(), plaintext) {
		t.Fatalf("ValidateToken returned true for missing token")
	}
	entry, ok := s.tokenCache.Load(hash)
	if !ok {
		t.Fatalf("not-found result was not cached")
	}
	if ce, ok := entry.(tokenCacheEntry); !ok || ce.valid {
		t.Fatalf("cache entry = %+v, want {valid:false}", entry)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("ExpectationsWereMet: %v", err)
	}
}
