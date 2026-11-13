package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestOpenSetsPoolAndBusyTimeout(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if got := st.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("max open connections=%d", got)
	}
	var timeout int
	if err = st.db.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&timeout); err != nil || timeout != busyTimeoutMS {
		t.Fatalf("busy_timeout=%d err=%v", timeout, err)
	}
}

func TestWriteRestoresBusyTimeoutAndRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "write.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err = st.db.ExecContext(ctx, `PRAGMA busy_timeout=1`); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	err = st.write(ctx, func(conn *sql.Conn) error {
		var timeout int
		if e := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&timeout); e != nil || timeout != busyTimeoutMS {
			t.Errorf("busy_timeout=%d err=%v", timeout, e)
		}
		if _, e := conn.ExecContext(ctx, `INSERT INTO agents(id,status) VALUES('x','running')`); e != nil {
			return e
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
	var n int
	if err = st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE id='x'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rollback failed n=%d err=%v", n, err)
	}
}

func TestWriteCommits(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "commit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.write(ctx, func(conn *sql.Conn) error {
		_, e := conn.ExecContext(ctx, `INSERT INTO agents(id,status) VALUES('x','running')`)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE id='x'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestParseTimeRejectsMalformedStamps(t *testing.T) {
	for _, bad := range []string{"", "2026-11-11", "2026-11-11T00:00:00", "2026-11-11T00:00:00.1234567890Z", "0000-01-01T00:00:00+24:00"} {
		if _, err := parseTime(bad); !errors.Is(err, errInvalidTimestamp) {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
	at, err := parseTime("2026-11-11T07:00:00.5+07:00")
	if err != nil || formatTime(at) != "2026-11-11T00:00:00.500000000Z" {
		t.Fatalf("at=%v err=%v", at, err)
	}
}
