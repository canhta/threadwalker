package trends_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/trends"
)

func TestRecordKeepsTopTen(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "trends.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.EnsureAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 8, 12, 0, 0, 0, time.UTC)
	var stories []trends.Story
	for i := 0; i < 12; i++ {
		stories = append(stories, trends.Story{Title: string(rune('a'+i)) + " story", URL: "https://x.invalid/" + string(rune('a'+i)), Sources: []string{"hn"}, Score: i})
	}
	if err = trends.Record(ctx, st, "a", stories, now); err != nil {
		t.Fatal(err)
	}
	rows, err := st.RecentTrendStories(ctx, "a", now.Add(-time.Hour), 100)
	if err != nil || len(rows) != 10 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
}
