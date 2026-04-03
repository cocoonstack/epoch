package store

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestListTokensReturnsScanError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	s := &Store{db: db}
	mock.ExpectQuery("SELECT id, name, token_plain, created_by, created_at, last_used FROM tokens ORDER BY id").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))

	if _, err := s.ListTokens(context.Background()); err == nil {
		t.Fatalf("ListTokens returned nil error for malformed row")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("ExpectationsWereMet: %v", err)
	}
}
