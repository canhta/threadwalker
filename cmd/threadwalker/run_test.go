package main

import (
	"bytes"
	"context"
	"github.com/canhta/threadwalker/internal/store"
	"path/filepath"
	"testing"
)

func TestRunOnceOfflineUsesNoCredentialsOrExternalServices(t *testing.T) {
	ctx := context.Background()
	var out, stderr bytes.Buffer
	db := filepath.Join(t.TempDir(), "offline.db")
	code := run(ctx, []string{"run", "-once", "-offline", "-db", db}, func(string) string { t.Fatal("offline read environment"); return "" }, &out, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s logs=%s", code, &stderr, &out)
	}
	st, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var wakes, actions int
	st.DB().QueryRow(`SELECT COUNT(*) FROM wakes WHERE end_reason='completed'`).Scan(&wakes)
	st.DB().QueryRow(`SELECT COUNT(*) FROM actions WHERE dry_run=1`).Scan(&actions)
	if wakes != 1 || actions != 1 {
		t.Fatalf("wakes=%d actions=%d", wakes, actions)
	}
}
