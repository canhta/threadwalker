package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/canhta/threadz/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "threadz.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing store: %v", err)
		}
	})
	return st
}

func insertTestAgent(t *testing.T, st *store.Store, agentID string) {
	t.Helper()
	if _, err := st.DB().ExecContext(context.Background(), `INSERT INTO agents (id, status, next_wake_at) VALUES (?, 'running', NULL)`, agentID); err != nil {
		t.Fatalf("inserting agent %q: %v", agentID, err)
	}
}

func countRows(t *testing.T, st *store.Store, query string, args ...any) int {
	t.Helper()
	var count int
	if err := st.DB().QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("counting rows with %q: %v", query, err)
	}
	return count
}
