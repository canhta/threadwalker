package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
)

func TestWakeOpenSince(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	since := now.Add(-30 * time.Minute)
	open := func() bool {
		got, err := st.WakeOpenSince(ctx, since)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if open() {
		t.Fatal("empty database reports an open wake")
	}
	st.DB().Exec(`INSERT INTO wakes(agent_id,started_at,kind) VALUES('a','2026-11-13T10:00:00Z','agent')`)
	if open() {
		t.Fatal("a wake older than the window counts as running")
	}
	st.DB().Exec(`INSERT INTO wakes(agent_id,started_at,ended_at,kind) VALUES('a','2026-11-13T11:50:00Z','2026-11-13T11:55:00Z','agent')`)
	if open() {
		t.Fatal("an ended wake counts as running")
	}
	st.DB().Exec(`INSERT INTO wakes(agent_id,started_at,kind) VALUES('a','2026-11-13T11:50:00Z','agent')`)
	if !open() {
		t.Fatal("an open recent wake is not reported")
	}
}
