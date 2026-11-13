package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/canhta/threadz/internal/store"
)

func TestDashboardOpenReadOnlyNeverCreatesOrChangesSchemaAndCannotWrite(t *testing.T) {
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "missing", "dashboard.db")
	if st, err := store.OpenReadOnly(ctx, missing); err == nil {
		st.Close()
		t.Fatal("opened absent DB")
	}
	if _, err := os.Stat(filepath.Dir(missing)); !os.IsNotExist(err) {
		t.Fatal("created missing directory")
	}
	path := filepath.Join(t.TempDir(), "existing space#.db")
	writable, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer writable.Close()
	if err = writable.EnsureAgent(ctx, "selected"); err != nil {
		t.Fatal(err)
	}
	ro, err := store.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err = ro.DB().ExecContext(ctx, `INSERT INTO agents(id,status) VALUES('forbidden','running')`); err == nil {
		t.Fatal("read-only connection wrote")
	}
	if _, err = ro.DB().ExecContext(ctx, `CREATE TABLE forbidden(id)`); err == nil {
		t.Fatal("read-only connection changed the schema")
	}
	if err = writable.EnsureAgent(ctx, "concurrent"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = ro.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM agents`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("concurrent state count %d err %v", count, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.OpenReadOnly(cancelled, path); !errors.Is(err, context.Canceled) {
		t.Fatal("did not preserve cancellation")
	}
}
