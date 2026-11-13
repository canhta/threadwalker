package trends_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/trends"
)

func TestSpiked(t *testing.T) {
	now := time.Date(2026, 1, 8, 12, 0, 0, 0, time.UTC)
	old := func(score int) store.TrendStory {
		return store.TrendStory{ObservedAt: now.Add(-5 * time.Hour), Score: score}
	}
	fresh := func(score int) store.TrendStory {
		return store.TrendStory{ObservedAt: now.Add(-time.Hour), Score: score}
	}
	base := []store.TrendStory{old(2), old(2), old(2)}
	for name, tc := range map[string]struct {
		stories []store.TrendStory
		want    bool
	}{
		"spike":            {append([]store.TrendStory{fresh(9)}, base...), true},
		"below multiplier": {append([]store.TrendStory{fresh(5)}, base...), false},
		"below min score":  {append([]store.TrendStory{fresh(5)}, old(1), old(1), old(1)), false},
		"no fresh story":   {base, false},
		"thin baseline":    {[]store.TrendStory{fresh(9), old(2)}, false},
	} {
		if got := trends.Spiked(tc.stories, now, 0, 0); got != tc.want {
			t.Errorf("%s: got %v want %v", name, got, tc.want)
		}
	}
}

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
	if err = trends.Record(ctx, st, "a", "agent", stories, now); err != nil {
		t.Fatal(err)
	}
	rows, err := st.RecentTrendStories(ctx, "a", now.Add(-time.Hour), 100)
	if err != nil || len(rows) != 10 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
}
