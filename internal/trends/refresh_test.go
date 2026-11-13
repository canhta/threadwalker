package trends_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/trends"
)

func TestRefreshReplacesTheSnapshot(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "refresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	tmpl := agent.OfflineTemplate()
	fixture := agent.NewOfflineFixture(func() time.Time { return now })
	fetch := func(c context.Context) ([]trends.Trend, error) {
		outside, err := fixture.Trends(c)
		out := make([]trends.Trend, len(outside))
		for i, o := range outside {
			out[i] = trends.Trend{Source: o.Source, Title: o.Title, URL: o.URL}
		}
		return out, err
	}
	n, err := trends.Refresh(ctx, st, tmpl, fetch, now)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	stories, err := st.RecentTrendStories(ctx, tmpl.ID, now.Add(-time.Hour), 10)
	if err != nil || len(stories) != 1 || stories[0].Title != "Synthetic offline trend" {
		t.Fatalf("stories=%+v err=%v", stories, err)
	}
	failing := func(context.Context) ([]trends.Trend, error) { return nil, errors.New("private detail") }
	if _, err = trends.Refresh(ctx, st, tmpl, failing, now); err == nil || err.Error() != "refreshing trends: fetch failed" {
		t.Fatalf("err=%v", err)
	}
}
