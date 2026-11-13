package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// busyTimeoutMS is the one SQLite lock wait for every Store connection.
const busyTimeoutMS = 5000

// Store wraps the SQLite database connection.
type Store struct {
	db *sql.DB
}

// Open opens or creates the SQLite database at path and creates the schema when the file is empty.
func Open(ctx context.Context, path string) (*Store, error) {
	if path != ":memory:" {
		dir := filepath.Dir(path)
		if dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("creating database directory %s: %w", dir, err)
			}
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite database %s: %w", path, err)
	}

	// One connection serialises in-process access and keeps :memory: databases
	// on their initialised connection; BEGIN IMMEDIATE protects across processes.
	db.SetMaxOpenConns(1)
	st := &Store{db: db}
	if err := st.setBusyTimeout(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := st.create(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("opening database %s: %w", path, err)
	}
	return st, nil
}

func (s *Store) setBusyTimeout(ctx context.Context, x interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	if _, err := x.ExecContext(ctx, fmt.Sprintf(`PRAGMA busy_timeout=%d`, busyTimeoutMS)); err != nil {
		return fmt.Errorf("configuring busy timeout: %w", err)
	}
	return nil
}

// write runs fn inside BEGIN IMMEDIATE so the SQLite writer is reserved before
// any read, across connections and processes. Nothing survives a failed or
// cancelled fn. The timeout is reapplied because guard transactions share this
// connection and set their own.
func (s *Store) write(ctx context.Context, fn func(*sql.Conn) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("opening write transaction: %w", err)
	}
	defer conn.Close()
	if err = s.setBusyTimeout(ctx, conn); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("starting write transaction: %w", err)
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(rollbackCtx, `ROLLBACK`)
	}()
	if err = fn(conn); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("committing write transaction: %w", err)
	}
	return nil
}

// Fixed-width UTC timestamps preserve nanosecond ordering in SQLite TEXT.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

var errInvalidTimestamp = errors.New("invalid timestamp")

func formatTime(at time.Time) string { return at.UTC().Format(timeLayout) }

func validTime(at time.Time) bool {
	year := at.UTC().Year()
	return !at.IsZero() && year >= 1 && year <= 9999
}

// time.Parse accepts excess fractional precision and some invalid RFC3339
// spellings. Reject them before parsing so stored times are never rounded.
var timestampPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

func parseTime(stamp string) (time.Time, error) {
	if !timestampPattern.MatchString(stamp) {
		return time.Time{}, errInvalidTimestamp
	}
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil || !validTime(at) {
		return time.Time{}, errInvalidTimestamp
	}
	return at.UTC(), nil
}

// DB exposes the underlying *sql.DB.
func (s *Store) DB() *sql.DB {
	return s.db
}

// Close closes the database connection.
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("closing database: %w", err)
	}
	return nil
}

// schemaVersion is the one schema this build reads and writes. It is stored in
// SQLite's user_version; bump it whenever schema.sql changes.
const schemaVersion = 11

// create builds the schema in an empty database; a database holding any other
// schema version is refused rather than altered.
func (s *Store) create(ctx context.Context) error {
	var version, objects int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master`).Scan(&objects); err != nil {
		return fmt.Errorf("reading schema: %w", err)
	}
	if objects > 0 {
		if version == schemaVersion {
			return nil
		}
		return fmt.Errorf("database has schema version %d, this build needs %d: move the file aside and start a fresh database", version, schemaVersion)
	}
	// auto_vacuum only takes effect before the first table exists.
	if _, err := s.db.ExecContext(ctx, `PRAGMA auto_vacuum=INCREMENTAL`); err != nil {
		return fmt.Errorf("enabling incremental auto_vacuum: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning schema transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("creating schema: %w", err)
	}
	if _, err = tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version=%d`, schemaVersion)); err != nil {
		return fmt.Errorf("recording schema version: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("committing schema: %w", err)
	}
	return nil
}

// SchemaVersion reports the version stored in the database and the one this
// build requires.
func (s *Store) SchemaVersion(ctx context.Context) (stored, expected int, err error) {
	if err = s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&stored); err != nil {
		return 0, schemaVersion, fmt.Errorf("reading schema version: %w", err)
	}
	return stored, schemaVersion, nil
}

// WakeOpenSince reports whether a wake that started after since has not ended.
func (s *Store) WakeOpenSince(ctx context.Context, since time.Time) (bool, error) {
	var open bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wakes WHERE ended_at IS NULL AND julianday(started_at) > julianday(?))`, formatStoreTimestamp(since)).Scan(&open)
	if err != nil {
		return false, fmt.Errorf("reading open wakes: %w", err)
	}
	return open, nil
}
