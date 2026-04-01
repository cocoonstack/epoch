package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestValidateTokenIgnoresZeroRowsAffectedOnLastUsedUpdate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	s := &Store{db: db}
	token := "test-token-for-validate-token"
	hash := sha256.Sum256([]byte(token))
	digest := hex.EncodeToString(hash[:])

	mock.ExpectQuery("SELECT 1 FROM tokens WHERE token_hash = \\? LIMIT 1").
		WithArgs(digest).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectExec("UPDATE tokens SET last_used = NOW\\(\\) WHERE token_hash = \\?").
		WithArgs(digest).
		WillReturnResult(sqlmock.NewResult(0, 0))

	if !s.ValidateToken(token) {
		t.Fatalf("ValidateToken returned false for an existing token when UPDATE affected 0 rows")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("ExpectationsWereMet: %v", err)
	}
}

func TestValidateTokenReturnsFalseWhenTokenMissing(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	s := &Store{db: db}
	token := "missing"
	hash := sha256.Sum256([]byte(token))
	digest := hex.EncodeToString(hash[:])

	mock.ExpectQuery("SELECT 1 FROM tokens WHERE token_hash = \\? LIMIT 1").
		WithArgs(digest).
		WillReturnError(sql.ErrNoRows)

	if s.ValidateToken(token) {
		t.Fatalf("ValidateToken returned true for a missing token")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("ExpectationsWereMet: %v", err)
	}
}
