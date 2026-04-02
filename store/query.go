package store

import (
	"context"
	"database/sql"
	"errors"
)

type rowScanner interface {
	Scan(dest ...any) error
}

func queryRows[T any](ctx context.Context, db *sql.DB, query string, scan func(*sql.Rows, *T) error, args ...any) ([]T, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var items []T
	for rows.Next() {
		var item T
		if err := scan(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func queryOptional[T any](scan func(*T) error) (*T, error) {
	var item T
	if err := scan(&item); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &item, nil
}
