package metrics_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/metrics"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
)

func goneRun(t *testing.T, own []threads.Post) (*store.Store, metrics.Result) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "gone.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tmpl := config.Template{ID: "own", Limits: config.LimitsConfig{MinMinutesBetweenWakes: 15}}
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec(`INSERT INTO actions(id,agent_id,kind,text,dry_run,posted_at,thread_url,media_id) VALUES(1,'own','post','',0,?,'https://fixture.test/own','own')`, start.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	source := threads.NewMemory()
	source.Errors[threads.OpReadOwnMetrics] = threads.ErrMediaNotFound
	source.Pages[threads.OpReadOwnPosts] = threads.Page{Kind: "own_posts", Posts: own}
	p, err := metrics.New(metrics.Options{Store: st, Template: tmpl, Now: func() time.Time { return start.Add(time.Hour) }, FetchTrends: metricTrends, ThreadsFactory: func(context.Context, config.Template) (threads.Backend, error) { return source, nil }})
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.RunDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return st, got
}

func TestDeletedPostEndsItsJobsAndShowsAsDeleted(t *testing.T) {
	st, got := goneRun(t, nil)
	if got.DeferredJobs != 0 {
		t.Fatalf("result=%+v", got)
	}
	var ended int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM metric_jobs WHERE completed_at IS NOT NULL AND reason='media_gone'`).Scan(&ended); err != nil || ended != 3 {
		t.Fatalf("ended jobs=%d err=%v", ended, err)
	}
}

func TestListedPostIsNotGoneAndBacksOff(t *testing.T) {
	st, got := goneRun(t, []threads.Post{{ID: "own"}})
	if got.DeferredJobs != 1 {
		t.Fatalf("result=%+v", got)
	}
	var attempts int
	if err := st.DB().QueryRow(`SELECT attempts FROM metric_jobs WHERE window_hours=1`).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestMetricsPassWithNothingDueLeavesNoWake(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "idle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tmpl := config.Template{ID: "own", Limits: config.LimitsConfig{MinMinutesBetweenWakes: 15}}
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	p, err := metrics.New(metrics.Options{Store: st, Template: tmpl, Now: func() time.Time { return now }, FetchTrends: metricTrends})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	var wakes int
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM wakes`).Scan(&wakes); err != nil || wakes != 0 {
		t.Fatalf("wakes=%d err=%v", wakes, err)
	}
}
