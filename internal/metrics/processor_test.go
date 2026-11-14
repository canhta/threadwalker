package metrics_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/metrics"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
)

func processorSource() *threads.Memory {
	m := threads.NewMemory()
	m.Actions["own"] = threads.Metrics{Views: 42, Likes: 3, Replies: 2, Reposts: 1}
	return m
}

func TestProcessorCloseFailureKeepsCompletedMetricScope(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "close.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start.Add(time.Hour)
	tmpl := config.Template{ID: "own", DryRun: true, Limits: config.LimitsConfig{MinMinutesBetweenWakes: 15}}
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec(`INSERT INTO actions(id,agent_id,kind,text,dry_run,posted_at,thread_url,media_id) VALUES(1,'own','post','',0,?,'https://fixture.test/own','own')`, start.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	source := processorSource()
	source.Errors[threads.OpClose] = errors.New("synthetic close failure")
	p, err := metrics.New(metrics.Options{Store: st, Template: tmpl, Now: func() time.Time { return now }, FetchTrends: metricTrends, ThreadsFactory: func(context.Context, config.Template) (threads.Backend, error) { return source, nil }})
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.RunDue(ctx)
	if err != nil || got.Samples != 1 || source.Count(threads.OpClose) != 1 {
		t.Fatalf("result=%+v closes=%d err=%v", got, source.Count(threads.OpClose), err)
	}
	var reason string
	if err = st.DB().QueryRow(`SELECT end_reason FROM wakes WHERE kind='metrics'`).Scan(&reason); err != nil || reason != "completed" {
		t.Fatalf("metric wake reason=%q err=%v", reason, err)
	}
}

func TestProcessorThreeWindowsAndCatchupUseActualObservation(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start
	tmpl := config.Template{ID: "own", DryRun: true, Limits: config.LimitsConfig{MinMinutesBetweenWakes: 15}}
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec(`INSERT INTO actions(id,agent_id,kind,text,dry_run,posted_at,thread_url,media_id) VALUES(1,'own','post','',0,?,'https://fixture.test/own','own')`, start.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	source := processorSource()
	makeProcessor := func() *metrics.Processor {
		p, e := metrics.New(metrics.Options{Store: st, Template: tmpl, Now: func() time.Time { return now }, FetchTrends: metricTrends, ThreadsFactory: func(context.Context, config.Template) (threads.Backend, error) { return source, nil }})
		if e != nil {
			t.Fatal(e)
		}
		return p
	}
	p := makeProcessor()
	for _, tc := range []struct{ hour, jobs, samples int }{{0, 0, 0}, {1, 1, 1}, {1, 0, 0}, {25, 2, 1}, {48, 0, 0}} {
		now = start.Add(time.Duration(tc.hour) * time.Hour)
		if tc.hour == 25 {
			p = makeProcessor()
		}
		got, e := p.RunDue(ctx)
		if e != nil || got.ClaimedJobs != tc.jobs || got.Samples != tc.samples {
			t.Fatalf("hour%d got=%+v err=%v", tc.hour, got, e)
		}
	}
	var n int
	if e := st.DB().QueryRow(`SELECT COUNT(*) FROM metrics WHERE action_id=1`).Scan(&n); e != nil || n != 2 {
		t.Fatalf("samples=%d %v", n, e)
	}
	if source.Count(threads.OpReadOwnMetrics) != 4 || source.Count(threads.OpClose) != 2 {
		t.Fatalf("reads=%d closed=%d", source.Count(threads.OpReadOwnMetrics), source.Count(threads.OpClose))
	}
	var at string
	if e := st.DB().QueryRow(`SELECT measured_at FROM metrics WHERE action_id=1 ORDER BY measured_at DESC LIMIT 1`).Scan(&at); e != nil {
		t.Fatal(e)
	}
	parsed, e := time.Parse(time.RFC3339Nano, at)
	if e != nil || !parsed.Equal(start.Add(25*time.Hour)) {
		t.Fatalf("fabricated target time %s %v", at, e)
	}
}
