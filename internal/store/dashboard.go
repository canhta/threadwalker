package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
)

// OpenReadOnly opens an existing database without creating directories, applying
// migrations or changing agent state. Every pooled connection is read-only.
func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	absolute, err := filepath.Abs(path)
	if err != nil || path == "" || path == ":memory:" {
		return nil, errors.New("opening dashboard database: invalid path")
	}
	uri := url.URL{Scheme: "file", Path: absolute}
	query := url.Values{"mode": {"ro"}, "_pragma": {"query_only(1)", "busy_timeout(1000)"}}
	uri.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, errors.New("opening dashboard database: unavailable")
	}
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		if ctx.Err() != nil {
			return nil, errors.Join(errors.New("opening dashboard database: cancelled"), ctx.Err())
		}
		return nil, errors.New("opening dashboard database: unavailable")
	}
	return &Store{db: db}, nil
}
