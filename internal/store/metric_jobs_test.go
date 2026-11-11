package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/store"
)

func metricCoreStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, id := range []string{"a", "b"} {
		if err := s.EnsureAgent(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	return s
}
func metricCoreAction(t *testing.T, s *store.Store, agent, stamp, url string, dry bool) int64 {
	t.Helper()
	r, err := s.DB().Exec(`INSERT INTO actions(agent_id,kind,text,posted_at,thread_url,dry_run) VALUES(?,'post','',?,?,?)`, agent, stamp, url, dry)
	if err != nil {
		t.Fatal(err)
	}
	id, err := r.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func TestMetricCoreSeedConfirmedOnlyAndExactDue(t *testing.T) {
	s := metricCoreStore(t)
	ctx := context.Background()
	at := time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC)
	id := metricCoreAction(t, s, "a", at.Format(time.RFC3339Nano), "https://example.test/own", false)
	metricCoreAction(t, s, "a", at.Format(time.RFC3339Nano), "", false)
	metricCoreAction(t, s, "a", at.Format(time.RFC3339Nano), "https://example.test/dry", true)
	metricCoreAction(t, s, "b", at.Format(time.RFC3339Nano), "https://example.test/other", false)
	if n, err := s.SeedMetricJobs(ctx, "a"); err != nil || n != 3 {
		t.Fatalf("seed %d %v", n, err)
	}
	if n, err := s.SeedMetricJobs(ctx, "a"); err != nil || n != 0 {
		t.Fatalf("reseed %d %v", n, err)
	}
	due, ok, err := s.NextMetricDue(ctx, "a")
	if err != nil || !ok || !due.Equal(at.Add(time.Hour)) {
		t.Fatalf("due %v %v %v", due, ok, err)
	}
	jobs, err := s.ClaimMetricJobs(ctx, "a", due.Add(-time.Nanosecond), time.Minute, 1)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("early claim %+v %v", jobs, err)
	}
	jobs, err = s.ClaimMetricJobs(ctx, "a", due, time.Minute, 1)
	if err != nil || len(jobs) != 1 || jobs[0].ActionID != id || jobs[0].WindowHours != 1 || !jobs[0].PostedAt.Equal(at) || jobs[0].ClaimVersion < 1 {
		t.Fatalf("exact claim %+v %v", jobs, err)
	}
}

func TestMetricCoreCatchUpOneActualSampleAndFencedRecovery(t *testing.T) {
	s := metricCoreStore(t)
	ctx := context.Background()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	metricCoreAction(t, s, "a", at.Format(time.RFC3339Nano), "https://example.test/own", false)
	if _, err := s.SeedMetricJobs(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	now := at.Add(25 * time.Hour)
	old, err := s.ClaimMetricJobs(ctx, "a", now, time.Minute, 1)
	if err != nil || len(old) != 3 {
		t.Fatalf("catch-up %+v %v", old, err)
	}
	if jobs, err := s.ClaimMetricJobs(ctx, "a", now.Add(time.Minute-time.Nanosecond), time.Minute, 1); err != nil || len(jobs) != 0 {
		t.Fatalf("live lease %+v %v", jobs, err)
	}
	fresh, err := s.ClaimMetricJobs(ctx, "a", now.Add(time.Minute), time.Minute, 1)
	if err != nil || len(fresh) != 3 || fresh[0].ClaimVersion <= old[0].ClaimVersion {
		t.Fatalf("recovery %+v %v", fresh, err)
	}
	if _, err = s.CompleteMetricJobs(ctx, old, now.Add(time.Minute), store.MetricValues{Views: 999}); !errors.Is(err, store.ErrMetricClaim) {
		t.Fatalf("stale complete %v", err)
	}
	if err = s.DeferMetricJobs(ctx, old, now.Add(time.Minute), now.Add(time.Hour), "read_failed"); !errors.Is(err, store.ErrMetricClaim) {
		t.Fatalf("stale defer %v", err)
	}
	measured := now.Add(time.Minute + time.Nanosecond)
	if ok, err := s.CompleteMetricJobs(ctx, fresh, measured, store.MetricValues{Views: 42, Likes: 3}); err != nil || !ok {
		t.Fatalf("complete %v %v", ok, err)
	}
	if ok, err := s.CompleteMetricJobs(ctx, fresh, measured.Add(time.Hour), store.MetricValues{Views: 999}); err != nil || ok {
		t.Fatalf("idempotent %v %v", ok, err)
	}
	summary, err := s.OwnerSummary(ctx, "a", at, at.Add(48*time.Hour))
	if err != nil || summary.MetricSamples != 1 || summary.Views != 42 {
		t.Fatalf("sample %+v %v", summary, err)
	}
	if _, ok, err := s.NextMetricDue(ctx, "a"); err != nil || ok {
		t.Fatalf("completed due %v %v", ok, err)
	}
}

func TestMetricCoreMaintenanceWakePreservesSchedule(t *testing.T) {
	s := metricCoreStore(t)
	ctx := context.Background()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.SetNextWake(ctx, "a", at.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	wake, err := s.BeginMetricWake(ctx, "a", at)
	if err != nil || wake < 1 {
		t.Fatalf("wake %d %v", wake, err)
	}
	state, err := s.AgentState(ctx, "a")
	if err != nil || !state.NextWake.Equal(at.Add(24*time.Hour)) {
		t.Fatalf("schedule %+v %v", state, err)
	}
	if _, err = s.BeginMetricWake(ctx, "a", at); err == nil {
		t.Fatal("overlapping wake allowed")
	}
	if err = s.FinishWake(ctx, wake, at.Add(time.Minute), 0, "completed"); err != nil {
		t.Fatal(err)
	}
	if err = s.PauseOwnerAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginMetricWake(ctx, "a", at); err == nil {
		t.Fatal("paused maintenance allowed")
	}
}
