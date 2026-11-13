package store_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
)

// This file is the independent acceptance suite for the metric-job store API.
// It uses only the public store surface plus the published SQLite tables. The
// job state table is discovered by name so the suite does not encode a private
// schema choice made by the store implementation.

var metricAcceptanceBase = time.Date(2026, 3, 2, 9, 15, 30, 123456789, time.UTC)

func metricAcceptanceStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "metrics-acceptance.db"))
	if err != nil {
		t.Fatalf("opening acceptance store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing acceptance store: %v", err)
		}
	})
	return st
}

func metricAcceptanceAgent(t *testing.T, st *store.Store, agentID string) {
	t.Helper()
	if err := st.EnsureAgent(context.Background(), agentID); err != nil {
		t.Fatalf("creating agent %q: %v", agentID, err)
	}
}

func metricAcceptanceAction(t *testing.T, st *store.Store, agentID, stamp, threadURL string, dryRun bool) int64 {
	t.Helper()
	return metricAcceptanceActionKind(t, st, agentID, "post", stamp, threadURL, dryRun)
}

func metricAcceptanceActionKind(t *testing.T, st *store.Store, agentID, kind, stamp, threadURL string, dryRun bool) int64 {
	t.Helper()
	ctx := context.Background()
	result, err := st.DB().ExecContext(ctx,
		`INSERT INTO actions(agent_id,kind,text,posted_at,thread_url,dry_run,media_id) VALUES(?,'`+kind+`','',?,?,?,'m')`,
		agentID, stamp, threadURL, dryRun)
	if err != nil {
		t.Fatalf("inserting %s action for %q: %v", kind, agentID, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("reading inserted action id: %v", err)
	}
	return id
}

func metricAcceptanceSeedAction(t *testing.T, st *store.Store, agentID string, postedAt time.Time) int64 {
	t.Helper()
	id := metricAcceptanceAction(t, st, agentID, postedAt.Format(time.RFC3339Nano), "https://threads.example/own-post", false)
	if _, err := st.SeedMetricJobs(context.Background(), agentID, time.Unix(0, 0)); err != nil {
		t.Fatalf("seeding metric jobs for %q: %v", agentID, err)
	}
	return id
}

func metricAcceptanceSampleCount(t *testing.T, st *store.Store, agentID string, actionID int64) int {
	t.Helper()
	var count int
	if err := st.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM metrics WHERE agent_id=? AND action_id=?`, agentID, actionID).Scan(&count); err != nil {
		t.Fatalf("counting metric samples for action %d: %v", actionID, err)
	}
	return count
}

func metricAcceptanceOnlySample(t *testing.T, st *store.Store, actionID int64) (time.Time, store.MetricValues) {
	t.Helper()
	ctx := context.Background()
	rows, err := st.DB().QueryContext(ctx,
		`SELECT measured_at,views,likes,replies,reposts FROM metrics WHERE action_id=? ORDER BY measured_at`, actionID)
	if err != nil {
		t.Fatalf("reading metric samples for action %d: %v", actionID, err)
	}
	defer rows.Close()
	var samples []struct {
		measured string
		values   store.MetricValues
	}
	for rows.Next() {
		var sample struct {
			measured string
			values   store.MetricValues
		}
		if err := rows.Scan(&sample.measured, &sample.values.Views, &sample.values.Likes, &sample.values.Replies, &sample.values.Reposts); err != nil {
			t.Fatalf("scanning metric sample: %v", err)
		}
		samples = append(samples, sample)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating metric samples: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("action %d has %d metric samples, want exactly one", actionID, len(samples))
	}
	measured, err := time.Parse(time.RFC3339Nano, samples[0].measured)
	if err != nil {
		t.Fatalf("parsing measured_at %q: %v", samples[0].measured, err)
	}
	return measured, samples[0].values
}

func metricAcceptanceWindowHours(jobs []store.MetricJob) map[int]bool {
	windows := make(map[int]bool)
	for _, job := range jobs {
		windows[job.WindowHours] = true
	}
	return windows
}

func metricAcceptanceJobStateTables(t *testing.T, st *store.Store) []string {
	t.Helper()
	rows, err := st.DB().QueryContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type='table' AND name LIKE '%metric%' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("listing metric-related tables: %v", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scanning table name: %v", err)
		}
		if name != "metrics" {
			tables = append(tables, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating table names: %v", err)
	}
	return tables
}

func metricAcceptanceCreateTrigger(t *testing.T, st *store.Store, name, operation, table string) {
	t.Helper()
	stmt := fmt.Sprintf(`CREATE TRIGGER "%s" BEFORE %s ON "%s" BEGIN SELECT RAISE(ABORT, 'synthetic acceptance failure'); END`, name, operation, table)
	if _, err := st.DB().ExecContext(context.Background(), stmt); err != nil {
		t.Fatalf("creating failure trigger %s on %s: %v", name, table, err)
	}
}

func metricAcceptanceDropTrigger(t *testing.T, st *store.Store, name string) {
	t.Helper()
	if _, err := st.DB().ExecContext(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS "%s"`, name)); err != nil {
		t.Fatalf("dropping failure trigger %s: %v", name, err)
	}
}

func TestMetricJobsAcceptanceSeedExactWindowsConfirmedOnly(t *testing.T) {
	ctx := context.Background()
	st := metricAcceptanceStore(t)
	metricAcceptanceAgent(t, st, "agent-a")
	metricAcceptanceAgent(t, st, "agent-b")

	confirmed := metricAcceptanceAction(t, st, "agent-a", metricAcceptanceBase.Format(time.RFC3339Nano), "https://threads.example/a", false)
	metricAcceptanceActionKind(t, st, "agent-a", "reply", metricAcceptanceBase.Add(2*time.Hour).Format(time.RFC3339Nano), "https://threads.example/a-reply", false)
	metricAcceptanceAction(t, st, "agent-a", metricAcceptanceBase.Format(time.RFC3339Nano), "", false)
	metricAcceptanceAction(t, st, "agent-a", metricAcceptanceBase.Format(time.RFC3339Nano), "https://threads.example/dry", true)
	metricAcceptanceAction(t, st, "agent-b", metricAcceptanceBase.Format(time.RFC3339Nano), "https://threads.example/b", false)

	seeded, err := st.SeedMetricJobs(ctx, "agent-a", time.Unix(0, 0))
	if err != nil {
		t.Fatalf("SeedMetricJobs: %v", err)
	}
	if seeded != 6 {
		t.Fatalf("SeedMetricJobs seeded %d jobs, want 6 for two confirmed actions at three windows each", seeded)
	}
	again, err := st.SeedMetricJobs(ctx, "agent-a", time.Unix(0, 0))
	if err != nil {
		t.Fatalf("second SeedMetricJobs: %v", err)
	}
	if again != 0 {
		t.Fatalf("second SeedMetricJobs seeded %d jobs, want 0 for an idempotent seed", again)
	}

	due, ok, err := st.NextMetricDue(ctx, "agent-a")
	if err != nil {
		t.Fatalf("NextMetricDue: %v", err)
	}
	if !ok || !due.Equal(metricAcceptanceBase.Add(time.Hour)) {
		t.Fatalf("NextMetricDue = %v,%v, want %v,true", due, ok, metricAcceptanceBase.Add(time.Hour))
	}

	jobs, err := st.ClaimMetricJobs(ctx, "agent-a", metricAcceptanceBase.Add(26*time.Hour), time.Minute, 10)
	if err != nil {
		t.Fatalf("ClaimMetricJobs catch-up: %v", err)
	}
	if len(jobs) != 6 {
		t.Fatalf("ClaimMetricJobs returned %d jobs, want 6 coalesced windows across two actions: %+v", len(jobs), jobs)
	}
	byAction := make(map[int64][]store.MetricJob)
	for _, job := range jobs {
		byAction[job.ActionID] = append(byAction[job.ActionID], job)
		if job.AgentID != "agent-a" {
			t.Fatalf("seeded job belongs to agent %q, want agent-a", job.AgentID)
		}
		if job.ThreadURL == "" {
			t.Fatalf("seeded job lost its thread URL: %+v", job)
		}
	}
	postJobs := byAction[confirmed]
	if len(postJobs) != 3 {
		t.Fatalf("confirmed post has %d jobs, want 3: %+v", len(postJobs), postJobs)
	}
	for _, job := range postJobs {
		if !job.PostedAt.Equal(metricAcceptanceBase) {
			t.Fatalf("job PostedAt = %v, want original posted time %v", job.PostedAt, metricAcceptanceBase)
		}
		if job.ThreadURL != "https://threads.example/a" {
			t.Fatalf("job ThreadURL = %q, want original confirmed URL", job.ThreadURL)
		}
		if job.ClaimVersion < 1 {
			t.Fatalf("job ClaimVersion = %d, want a positive fencing version", job.ClaimVersion)
		}
	}
	windows := metricAcceptanceWindowHours(postJobs)
	for _, window := range []int{1, 6, 24} {
		if !windows[window] {
			t.Fatalf("post windows = %v, want window %dh", windows, window)
		}
	}
	for _, job := range postJobs {
		want := metricAcceptanceBase.Add(time.Duration(job.WindowHours) * time.Hour)
		if !job.DueAt.Equal(want) {
			t.Fatalf("window %dh DueAt = %v, want exact target %v", job.WindowHours, job.DueAt, want)
		}
	}

	if count := metricAcceptanceSampleCount(t, st, "agent-b", 0); count != 0 {
		t.Fatalf("agent-b samples = %d, want none from agent-a seed/claim", count)
	}
}

func TestMetricJobsAcceptanceSeedRejectsMalformedEligibleTimeAtomically(t *testing.T) {
	ctx := context.Background()
	st := metricAcceptanceStore(t)
	metricAcceptanceAgent(t, st, "agent-a")

	metricAcceptanceAction(t, st, "agent-a", metricAcceptanceBase.Format(time.RFC3339Nano), "https://threads.example/valid", false)
	malformed := metricAcceptanceAction(t, st, "agent-a", "not-a-timestamp", "https://threads.example/broken", false)

	if _, err := st.SeedMetricJobs(ctx, "agent-a", time.Unix(0, 0)); err == nil {
		t.Fatal("SeedMetricJobs accepted a malformed eligible posted_at, want an atomic failure")
	}
	if _, ok, err := st.NextMetricDue(ctx, "agent-a"); err != nil || ok {
		t.Fatalf("NextMetricDue after malformed seed = ok %v err %v, want no partial jobs", ok, err)
	}
	jobs, err := st.ClaimMetricJobs(ctx, "agent-a", metricAcceptanceBase.Add(72*time.Hour), time.Minute, 10)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("ClaimMetricJobs after malformed seed = %+v,%v, want no partial jobs", jobs, err)
	}

	if _, err := st.DB().ExecContext(ctx, `UPDATE actions SET dry_run=1 WHERE id=?`, malformed); err != nil {
		t.Fatalf("marking malformed action as dry run: %v", err)
	}
	seeded, err := st.SeedMetricJobs(ctx, "agent-a", time.Unix(0, 0))
	if err != nil {
		t.Fatalf("SeedMetricJobs with malformed dry-run action: %v", err)
	}
	if seeded != 3 {
		t.Fatalf("SeedMetricJobs seeded %d jobs, want 3 for the only confirmed action", seeded)
	}
}

func TestMetricJobsAcceptanceExactDueAndLeaseBoundaries(t *testing.T) {
	ctx := context.Background()
	st := metricAcceptanceStore(t)
	metricAcceptanceAgent(t, st, "agent-a")
	actionID := metricAcceptanceSeedAction(t, st, "agent-a", metricAcceptanceBase)

	lease := 5 * time.Minute
	due1 := metricAcceptanceBase.Add(time.Hour)
	due6 := metricAcceptanceBase.Add(6 * time.Hour)
	due24 := metricAcceptanceBase.Add(24 * time.Hour)

	if jobs, err := st.ClaimMetricJobs(ctx, "agent-a", due1.Add(-time.Nanosecond), lease, 10); err != nil || len(jobs) != 0 {
		t.Fatalf("claim one nanosecond before due = %+v,%v, want none", jobs, err)
	}
	jobs, err := st.ClaimMetricJobs(ctx, "agent-a", due1, lease, 10)
	if err != nil || len(jobs) != 1 || jobs[0].WindowHours != 1 || jobs[0].ActionID != actionID {
		t.Fatalf("claim exactly at 1h due = %+v,%v, want the 1h job", jobs, err)
	}
	firstVersion := jobs[0].ClaimVersion
	if next, ok, err := st.NextMetricDue(ctx, "agent-a"); err != nil || !ok || !next.Equal(due1.Add(lease)) {
		t.Fatalf("NextMetricDue during lease = %v,%v,%v, want lease expiry %v", next, ok, err, due1.Add(lease))
	}
	if jobs, err := st.ClaimMetricJobs(ctx, "agent-a", due1.Add(lease-time.Nanosecond), lease, 10); err != nil || len(jobs) != 0 {
		t.Fatalf("claim one nanosecond before lease expiry = %+v,%v, want none", jobs, err)
	}
	if _, err := st.CompleteMetricJobs(ctx, jobs, due1.Add(lease), store.MetricValues{Views: 1}); !errors.Is(err, store.ErrMetricClaim) {
		t.Fatalf("complete at exact lease expiry error = %v, want ErrMetricClaim", err)
	}
	reclaimed, err := st.ClaimMetricJobs(ctx, "agent-a", due1.Add(lease), lease, 10)
	if err != nil || len(reclaimed) != 1 || reclaimed[0].WindowHours != 1 {
		t.Fatalf("reclaim exactly at lease expiry = %+v,%v, want the 1h job", reclaimed, err)
	}
	if reclaimed[0].ClaimVersion <= firstVersion {
		t.Fatalf("reclaimed version = %d, want greater than stale version %d", reclaimed[0].ClaimVersion, firstVersion)
	}
	if ok, err := st.CompleteMetricJobs(ctx, reclaimed, due1.Add(lease+time.Second), store.MetricValues{Views: 7, Likes: 2}); err != nil || !ok {
		t.Fatalf("complete fresh reclaim = %v,%v, want true,nil", ok, err)
	}

	if next, ok, err := st.NextMetricDue(ctx, "agent-a"); err != nil || !ok || !next.Equal(due6) {
		t.Fatalf("NextMetricDue after 1h completion = %v,%v,%v, want %v", next, ok, err, due6)
	}
	jobs, err = st.ClaimMetricJobs(ctx, "agent-a", due6, lease, 10)
	if err != nil || len(jobs) != 1 || jobs[0].WindowHours != 6 {
		t.Fatalf("claim exactly at 6h due = %+v,%v, want the 6h job", jobs, err)
	}
	if ok, err := st.CompleteMetricJobs(ctx, jobs, due6.Add(time.Minute), store.MetricValues{Views: 9}); err != nil || !ok {
		t.Fatalf("complete 6h job = %v,%v", ok, err)
	}
	if next, ok, err := st.NextMetricDue(ctx, "agent-a"); err != nil || !ok || !next.Equal(due24) {
		t.Fatalf("NextMetricDue after 6h completion = %v,%v,%v, want %v", next, ok, err, due24)
	}
	jobs, err = st.ClaimMetricJobs(ctx, "agent-a", due24, lease, 10)
	if err != nil || len(jobs) != 1 || jobs[0].WindowHours != 24 {
		t.Fatalf("claim exactly at 24h due = %+v,%v, want the 24h job", jobs, err)
	}
	if ok, err := st.CompleteMetricJobs(ctx, jobs, due24.Add(time.Minute), store.MetricValues{Views: 11}); err != nil || !ok {
		t.Fatalf("complete 24h job = %v,%v", ok, err)
	}
	if next, ok, err := st.NextMetricDue(ctx, "agent-a"); err != nil || ok {
		t.Fatalf("NextMetricDue after all windows = %v,%v,%v, want no pending target", next, ok, err)
	}
}

func TestMetricJobsAcceptanceCoalescesDueWindowsIntoOneActualSample(t *testing.T) {
	ctx := context.Background()
	st := metricAcceptanceStore(t)
	metricAcceptanceAgent(t, st, "agent-a")
	actionID := metricAcceptanceSeedAction(t, st, "agent-a", metricAcceptanceBase)

	at6 := metricAcceptanceBase.Add(6 * time.Hour)
	jobs, err := st.ClaimMetricJobs(ctx, "agent-a", at6, 30*time.Minute, 10)
	if err != nil {
		t.Fatalf("ClaimMetricJobs at 6h: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("claim at 6h returned %d jobs, want the coalesced 1h and 6h jobs: %+v", len(jobs), jobs)
	}
	if windows := metricAcceptanceWindowHours(jobs); !windows[1] || !windows[6] || windows[24] {
		t.Fatalf("coalesced windows = %v, want {1,6} and not 24", windows)
	}

	measuredAt := at6.Add(17 * time.Minute)
	values := store.MetricValues{Views: 123, Likes: 17, Replies: 4, Reposts: 2}
	ok, err := st.CompleteMetricJobs(ctx, jobs, measuredAt, values)
	if err != nil || !ok {
		t.Fatalf("CompleteMetricJobs = %v,%v, want true,nil", ok, err)
	}
	storedAt, stored := metricAcceptanceOnlySample(t, st, actionID)
	if !storedAt.Equal(measuredAt) {
		t.Fatalf("measured_at = %v, want actual completion time %v (never a fabricated target time)", storedAt, measuredAt)
	}
	if stored != values {
		t.Fatalf("stored values = %+v, want %+v", stored, values)
	}
	if count := metricAcceptanceSampleCount(t, st, "agent-a", actionID); count != 1 {
		t.Fatalf("coalesced completion inserted %d samples, want one", count)
	}

	ok, err = st.CompleteMetricJobs(ctx, jobs, measuredAt.Add(time.Hour), store.MetricValues{Views: 999})
	if err != nil || ok {
		t.Fatalf("repeat CompleteMetricJobs = %v,%v, want false,nil for an already completed target", ok, err)
	}
	if count := metricAcceptanceSampleCount(t, st, "agent-a", actionID); count != 1 {
		t.Fatalf("repeat completion inserted another sample, count = %d", count)
	}
	if next, ok, err := st.NextMetricDue(ctx, "agent-a"); err != nil || !ok || !next.Equal(metricAcceptanceBase.Add(24*time.Hour)) {
		t.Fatalf("NextMetricDue after coalesced completion = %v,%v,%v, want the 24h target", next, ok, err)
	}
}

func TestMetricJobsAcceptanceActionAndAgentIsolation(t *testing.T) {
	ctx := context.Background()
	st := metricAcceptanceStore(t)
	metricAcceptanceAgent(t, st, "agent-a")
	metricAcceptanceAgent(t, st, "agent-b")

	actionA1 := metricAcceptanceAction(t, st, "agent-a", metricAcceptanceBase.Format(time.RFC3339Nano), "https://threads.example/a1", false)
	actionA2 := metricAcceptanceAction(t, st, "agent-a", metricAcceptanceBase.Format(time.RFC3339Nano), "https://threads.example/a2", false)
	actionB := metricAcceptanceAction(t, st, "agent-b", metricAcceptanceBase.Format(time.RFC3339Nano), "https://threads.example/b1", false)
	for _, agent := range []string{"agent-a", "agent-b"} {
		if _, err := st.SeedMetricJobs(ctx, agent, time.Unix(0, 0)); err != nil {
			t.Fatalf("SeedMetricJobs(%s, time.Unix(0, 0)): %v", agent, err)
		}
	}

	due := metricAcceptanceBase.Add(time.Hour)
	aJobs, err := st.ClaimMetricJobs(ctx, "agent-a", due, 10*time.Minute, 1)
	if err != nil || len(aJobs) != 1 {
		t.Fatalf("first agent-a claim = %+v,%v, want one action group", aJobs, err)
	}
	if aJobs[0].AgentID != "agent-a" {
		t.Fatalf("agent-a claim returned %+v", aJobs)
	}
	firstAction := aJobs[0].ActionID
	if firstAction != actionA1 && firstAction != actionA2 {
		t.Fatalf("agent-a claim returned action %d, want one of its own actions", firstAction)
	}
	bJobs, err := st.ClaimMetricJobs(ctx, "agent-b", due, time.Minute, 1)
	if err != nil || len(bJobs) != 1 || bJobs[0].AgentID != "agent-b" || bJobs[0].ActionID != actionB {
		t.Fatalf("agent-b claim = %+v,%v, want its own action", bJobs, err)
	}

	foreign := append([]store.MetricJob(nil), bJobs...)
	foreign[0].AgentID = "agent-a"
	if _, err := st.CompleteMetricJobs(ctx, foreign, due.Add(time.Minute), store.MetricValues{Views: 500}); !errors.Is(err, store.ErrMetricClaim) {
		t.Fatalf("foreign-agent completion error = %v, want ErrMetricClaim", err)
	}
	if count := metricAcceptanceSampleCount(t, st, "agent-b", actionB); count != 0 {
		t.Fatalf("foreign completion wrote %d samples for agent-b", count)
	}
	if err := st.DeferMetricJobs(ctx, foreign, due.Add(time.Minute), due.Add(time.Hour), "read_failed"); !errors.Is(err, store.ErrMetricClaim) {
		t.Fatalf("foreign-agent defer error = %v, want ErrMetricClaim", err)
	}

	otherActionJobs, err := st.ClaimMetricJobs(ctx, "agent-a", due, 10*time.Minute, 1)
	if err != nil || len(otherActionJobs) != 1 {
		t.Fatalf("second agent-a claim = %+v,%v, want the other action group", otherActionJobs, err)
	}
	if otherActionJobs[0].ActionID == firstAction {
		t.Fatalf("second agent-a claim reused action %d, want an unclaimed action", firstAction)
	}
	mixed := append(append([]store.MetricJob(nil), aJobs...), otherActionJobs...)
	if _, err := st.CompleteMetricJobs(ctx, mixed, due.Add(2*time.Minute), store.MetricValues{Views: 1}); !errors.Is(err, store.ErrMetricClaim) {
		t.Fatalf("mixed-action completion error = %v, want ErrMetricClaim", err)
	}
	if count := metricAcceptanceSampleCount(t, st, "agent-a", otherActionJobs[0].ActionID); count != 0 {
		t.Fatalf("mixed-action completion wrote %d samples", count)
	}
	if ok, err := st.CompleteMetricJobs(ctx, otherActionJobs, due.Add(3*time.Minute), store.MetricValues{Views: 2}); err != nil || !ok {
		t.Fatalf("complete isolated action group = %v,%v", ok, err)
	}
	if count := metricAcceptanceSampleCount(t, st, "agent-b", actionB); count != 0 {
		t.Fatalf("agent-a completion touched agent-b sample count = %d", count)
	}
	if next, ok, err := st.NextMetricDue(ctx, "agent-b"); err != nil || !ok || !next.Equal(due.Add(time.Minute)) {
		t.Fatalf("agent-b NextMetricDue = %v,%v,%v, want its untouched lease expiry %v", next, ok, err, due.Add(time.Minute))
	}
}

func TestMetricJobsAcceptanceConcurrentClaimsAreExclusive(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concurrent-metrics.db")
	const actions = 8
	const workers = 4

	seedStore, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("opening seed store: %v", err)
	}
	metricAcceptanceAgent(t, seedStore, "agent-a")
	wantActions := make(map[int64]bool)
	for i := 0; i < actions; i++ {
		wantActions[metricAcceptanceAction(t, seedStore, "agent-a", metricAcceptanceBase.Format(time.RFC3339Nano), fmt.Sprintf("https://threads.example/concurrent-%d", i), false)] = true
	}
	if _, err := seedStore.SeedMetricJobs(ctx, "agent-a", time.Unix(0, 0)); err != nil {
		t.Fatalf("seeding concurrent actions: %v", err)
	}
	if err := seedStore.Close(); err != nil {
		t.Fatalf("closing seed store: %v", err)
	}

	stores := make([]*store.Store, workers)
	for i := range stores {
		st, err := store.Open(ctx, path)
		if err != nil {
			t.Fatalf("opening worker store %d: %v", i, err)
		}
		st.DB().SetMaxOpenConns(1)
		if _, err := st.DB().ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
			t.Fatalf("setting busy timeout on worker store %d: %v", i, err)
		}
		stores[i] = st
		t.Cleanup(func() { _ = st.Close() })
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	claimed := make(map[int64]int)
	var claimErrors []error
	for _, st := range stores {
		wg.Add(1)
		go func(st *store.Store) {
			defer wg.Done()
			<-start
			for attempt := 0; attempt < 2000; attempt++ {
				jobs, err := st.ClaimMetricJobs(ctx, "agent-a", metricAcceptanceBase.Add(time.Hour), time.Minute, 1)
				if err != nil {
					// SQLite may report a transient busy error to one writer.
					runtime.Gosched()
					continue
				}
				if len(jobs) == 0 {
					return
				}
				mu.Lock()
				for _, job := range jobs {
					claimed[job.ActionID]++
				}
				mu.Unlock()
			}
			mu.Lock()
			claimErrors = append(claimErrors, errors.New("worker exhausted concurrent claim attempts"))
			mu.Unlock()
		}(st)
	}
	close(start)
	wg.Wait()

	if len(claimErrors) != 0 {
		t.Fatalf("concurrent claims failed: %v", claimErrors)
	}
	if len(claimed) != len(wantActions) {
		t.Fatalf("concurrent claims covered %d actions, want %d: %v", len(claimed), len(wantActions), claimed)
	}
	for actionID := range wantActions {
		if claimed[actionID] != 1 {
			t.Fatalf("action %d was claimed %d times, want exactly once", actionID, claimed[actionID])
		}
	}
	for actionID, count := range claimed {
		if count != 1 {
			t.Fatalf("action %d was claimed %d times, want exactly once", actionID, count)
		}
	}
}

func TestMetricJobsAcceptanceStaleFenceAndExpiryRecovery(t *testing.T) {
	ctx := context.Background()
	st := metricAcceptanceStore(t)
	metricAcceptanceAgent(t, st, "agent-a")
	actionID := metricAcceptanceSeedAction(t, st, "agent-a", metricAcceptanceBase)

	due := metricAcceptanceBase.Add(time.Hour)
	lease := 3 * time.Minute
	old, err := st.ClaimMetricJobs(ctx, "agent-a", due, lease, 10)
	if err != nil || len(old) != 1 {
		t.Fatalf("initial claim = %+v,%v", old, err)
	}
	fresh, err := st.ClaimMetricJobs(ctx, "agent-a", due.Add(lease), lease, 10)
	if err != nil || len(fresh) != 1 {
		t.Fatalf("reclaim after expiry = %+v,%v", fresh, err)
	}
	if fresh[0].ClaimVersion <= old[0].ClaimVersion {
		t.Fatalf("reclaimed version = %d, want greater than %d", fresh[0].ClaimVersion, old[0].ClaimVersion)
	}
	if _, err := st.CompleteMetricJobs(ctx, old, due.Add(lease+time.Second), store.MetricValues{Views: 999}); !errors.Is(err, store.ErrMetricClaim) {
		t.Fatalf("stale completion error = %v, want ErrMetricClaim", err)
	}
	if err := st.DeferMetricJobs(ctx, old, due.Add(lease+time.Second), due.Add(time.Hour), "read_failed"); !errors.Is(err, store.ErrMetricClaim) {
		t.Fatalf("stale defer error = %v, want ErrMetricClaim", err)
	}
	if count := metricAcceptanceSampleCount(t, st, "agent-a", actionID); count != 0 {
		t.Fatalf("stale operation wrote %d samples", count)
	}
	if ok, err := st.CompleteMetricJobs(ctx, fresh, due.Add(lease+2*time.Second), store.MetricValues{Views: 31}); err != nil || !ok {
		t.Fatalf("fresh completion after stale rejection = %v,%v", ok, err)
	}
	storedAt, stored := metricAcceptanceOnlySample(t, st, actionID)
	if !storedAt.Equal(due.Add(lease+2*time.Second)) || stored.Views != 31 {
		t.Fatalf("fresh sample = %v %+v, want measured %v views 31", storedAt, stored, due.Add(lease+2*time.Second))
	}
}

func TestMetricJobsAcceptanceCompleteRollsBackSampleAndCheckpoint(t *testing.T) {
	ctx := context.Background()
	st := metricAcceptanceStore(t)
	metricAcceptanceAgent(t, st, "agent-a")
	actionID := metricAcceptanceSeedAction(t, st, "agent-a", metricAcceptanceBase)

	at := metricAcceptanceBase.Add(25 * time.Hour)
	jobs, err := st.ClaimMetricJobs(ctx, "agent-a", at, 5*time.Minute, 10)
	if err != nil || len(jobs) != 3 {
		t.Fatalf("claim for rollback = %+v,%v, want three coalesced windows", jobs, err)
	}
	measured := at.Add(time.Minute)

	metricAcceptanceCreateTrigger(t, st, "acceptance_fail_sample_insert", "INSERT", "metrics")
	if _, err := st.CompleteMetricJobs(ctx, jobs, measured, store.MetricValues{Views: 12}); err == nil {
		t.Fatal("CompleteMetricJobs ignored a failing sample insert trigger")
	}
	if count := metricAcceptanceSampleCount(t, st, "agent-a", actionID); count != 0 {
		t.Fatalf("failed sample insert left %d samples", count)
	}
	metricAcceptanceDropTrigger(t, st, "acceptance_fail_sample_insert")

	jobTables := metricAcceptanceJobStateTables(t, st)
	if len(jobTables) == 0 {
		t.Fatal("schema has no metric job state table to fence completion")
	}
	var jobTriggers []string
	for tableIndex, table := range jobTables {
		for _, operation := range []string{"INSERT", "UPDATE", "DELETE"} {
			name := fmt.Sprintf("acceptance_fail_job_%d_%s", tableIndex, operation)
			metricAcceptanceCreateTrigger(t, st, name, operation, table)
			jobTriggers = append(jobTriggers, name)
		}
	}
	if _, err := st.CompleteMetricJobs(ctx, jobs, measured, store.MetricValues{Views: 12}); err == nil {
		t.Fatal("CompleteMetricJobs ignored a failing job-checkpoint trigger")
	}
	if count := metricAcceptanceSampleCount(t, st, "agent-a", actionID); count != 0 {
		t.Fatalf("failed checkpoint left %d samples, want the sample rolled back too", count)
	}
	for _, name := range jobTriggers {
		metricAcceptanceDropTrigger(t, st, name)
	}

	if ok, err := st.CompleteMetricJobs(ctx, jobs, measured, store.MetricValues{Views: 12, Likes: 3}); err != nil || !ok {
		t.Fatalf("completion retry after rollback = %v,%v, want true,nil because the claim stayed pending", ok, err)
	}
	storedAt, stored := metricAcceptanceOnlySample(t, st, actionID)
	if !storedAt.Equal(measured) || stored.Views != 12 || stored.Likes != 3 {
		t.Fatalf("retried sample = %v %+v, want measured %v views 12 likes 3", storedAt, stored, measured)
	}

	if count := metricAcceptanceSampleCount(t, st, "agent-a", actionID); count != 1 {
		t.Fatalf("successful completion sample count = %d, want one", count)
	}
}

func TestMetricJobsAcceptanceCompleteRejectsInvalidValuesAndScopes(t *testing.T) {
	ctx := context.Background()
	st := metricAcceptanceStore(t)
	metricAcceptanceAgent(t, st, "agent-a")
	metricAcceptanceAgent(t, st, "agent-b")
	actionID := metricAcceptanceSeedAction(t, st, "agent-a", metricAcceptanceBase)
	foreignAction := metricAcceptanceAction(t, st, "agent-b", metricAcceptanceBase.Format(time.RFC3339Nano), "https://threads.example/foreign", false)
	if _, err := st.SeedMetricJobs(ctx, "agent-b", time.Unix(0, 0)); err != nil {
		t.Fatalf("seeding foreign action: %v", err)
	}

	due := metricAcceptanceBase.Add(time.Hour)
	jobs, err := st.ClaimMetricJobs(ctx, "agent-a", due, 5*time.Minute, 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim for value validation = %+v,%v", jobs, err)
	}
	foreignJobs, err := st.ClaimMetricJobs(ctx, "agent-b", due, 5*time.Minute, 10)
	if err != nil || len(foreignJobs) != 1 || foreignJobs[0].ActionID != foreignAction {
		t.Fatalf("claiming foreign action = %+v,%v", foreignJobs, err)
	}

	for _, values := range []store.MetricValues{
		{Views: -1},
		{Likes: -1},
		{Replies: -1},
		{Reposts: -1},
	} {
		if _, err := st.CompleteMetricJobs(ctx, jobs, due.Add(time.Minute), values); err == nil {
			t.Fatalf("CompleteMetricJobs accepted negative counts %+v", values)
		}
	}
	if count := metricAcceptanceSampleCount(t, st, "agent-a", actionID); count != 0 {
		t.Fatalf("invalid values inserted %d samples", count)
	}
	foreignDefer := append([]store.MetricJob(nil), foreignJobs...)
	foreignDefer[0].AgentID = "agent-a"
	if err := st.DeferMetricJobs(ctx, foreignDefer, due.Add(time.Minute), due.Add(time.Hour), "read_failed"); !errors.Is(err, store.ErrMetricClaim) {
		t.Fatalf("defer foreign claim error = %v, want ErrMetricClaim", err)
	}
	if count := metricAcceptanceSampleCount(t, st, "agent-b", foreignAction); count != 0 {
		t.Fatalf("defer foreign claim inserted %d samples", count)
	}
	if ok, err := st.CompleteMetricJobs(ctx, jobs, due.Add(2*time.Minute), store.MetricValues{Views: 0}); err != nil || !ok {
		t.Fatalf("valid zero-valued completion = %v,%v, want true,nil", ok, err)
	}
}

func TestMetricJobsAcceptanceDeferRetriesAndValidates(t *testing.T) {
	validReasons := []string{
		"metrics_unavailable",
		"source_unavailable",
		"read_failed",
		"binding_mismatch",
		"auth_required",
		"cancelled",
		"persistence_failed",
		"measurement_failed",
	}
	for _, reason := range validReasons {
		reason := reason
		t.Run(reason, func(t *testing.T) {
			ctx := context.Background()
			st := metricAcceptanceStore(t)
			metricAcceptanceAgent(t, st, "agent-a")
			actionID := metricAcceptanceSeedAction(t, st, "agent-a", metricAcceptanceBase)
			due := metricAcceptanceBase.Add(time.Hour)
			jobs, err := st.ClaimMetricJobs(ctx, "agent-a", due, 5*time.Minute, 10)
			if err != nil || len(jobs) != 1 {
				t.Fatalf("claim before defer = %+v,%v", jobs, err)
			}
			now := due.Add(time.Minute)
			next := due.Add(5 * time.Minute)
			if err := st.DeferMetricJobs(ctx, jobs, now, next, reason); err != nil {
				t.Fatalf("DeferMetricJobs(%q): %v", reason, err)
			}
			if count := metricAcceptanceSampleCount(t, st, "agent-a", actionID); count != 0 {
				t.Fatalf("defer inserted %d samples", count)
			}
			if got, ok, err := st.NextMetricDue(ctx, "agent-a"); err != nil || !ok || !got.Equal(next) {
				t.Fatalf("NextMetricDue after defer = %v,%v,%v, want %v", got, ok, err, next)
			}
			if early, err := st.ClaimMetricJobs(ctx, "agent-a", next.Add(-time.Nanosecond), time.Minute, 10); err != nil || len(early) != 0 {
				t.Fatalf("claim one nanosecond before deferred retry = %+v,%v, want none", early, err)
			}
			if _, err := st.CompleteMetricJobs(ctx, jobs, next, store.MetricValues{Views: 1}); !errors.Is(err, store.ErrMetricClaim) {
				t.Fatalf("completing a deferred stale claim error = %v, want ErrMetricClaim", err)
			}
			retried, err := st.ClaimMetricJobs(ctx, "agent-a", next, time.Minute, 10)
			if err != nil || len(retried) != 1 {
				t.Fatalf("claim exactly at deferred retry = %+v,%v, want the retried job", retried, err)
			}
			if ok, err := st.CompleteMetricJobs(ctx, retried, next.Add(time.Second), store.MetricValues{Views: 4}); err != nil || !ok {
				t.Fatalf("complete after retry = %v,%v", ok, err)
			}
		})
	}

	t.Run("invalid input and state", func(t *testing.T) {
		ctx := context.Background()
		st := metricAcceptanceStore(t)
		metricAcceptanceAgent(t, st, "agent-a")
		actionID := metricAcceptanceSeedAction(t, st, "agent-a", metricAcceptanceBase)
		due := metricAcceptanceBase.Add(time.Hour)
		jobs, err := st.ClaimMetricJobs(ctx, "agent-a", due, 5*time.Minute, 10)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("claim before invalid defer = %+v,%v", jobs, err)
		}
		for _, tc := range []struct {
			name string
			now  time.Time
			next time.Time
			why  string
		}{
			{"next equals now", due, due, "read_failed"},
			{"next before now", due, due.Add(-time.Second), "read_failed"},
			{"unknown reason", due, due.Add(time.Hour), "https://threads.example/@someone raw text"},
			{"empty reason", due, due.Add(time.Hour), ""},
		} {
			if err := st.DeferMetricJobs(ctx, jobs, tc.now, tc.next, tc.why); err == nil {
				t.Fatalf("DeferMetricJobs accepted %s (reason %q)", tc.name, tc.why)
			}
		}
		if count := metricAcceptanceSampleCount(t, st, "agent-a", actionID); count != 0 {
			t.Fatalf("invalid defer inserted %d samples", count)
		}
		if next, ok, err := st.NextMetricDue(ctx, "agent-a"); err != nil || !ok || !next.Equal(due.Add(5*time.Minute)) {
			t.Fatalf("NextMetricDue after rejected defer = %v,%v,%v, want the still-live lease expiry", next, ok, err)
		}
		if ok, err := st.CompleteMetricJobs(ctx, jobs, due.Add(time.Minute), store.MetricValues{Views: 8}); err != nil || !ok {
			t.Fatalf("complete after rejected defer = %v,%v", ok, err)
		}
	})
}

func TestMetricJobsAcceptancePausedAndKilledAgentsAreRefused(t *testing.T) {
	ctx := context.Background()
	st := metricAcceptanceStore(t)
	metricAcceptanceAgent(t, st, "agent-a")
	metricAcceptanceSeedAction(t, st, "agent-a", metricAcceptanceBase)
	due := metricAcceptanceBase.Add(time.Hour)

	for _, status := range []string{"paused", "killed"} {
		status := status
		t.Run(status, func(t *testing.T) {
			if _, err := st.DB().ExecContext(ctx, `UPDATE agents SET status=? WHERE id='agent-a'`, status); err != nil {
				t.Fatalf("setting agent status %q: %v", status, err)
			}
			if jobs, _ := st.ClaimMetricJobs(ctx, "agent-a", due, time.Minute, 10); len(jobs) != 0 {
				t.Fatalf("%s agent claimed %d jobs, want none", status, len(jobs))
			}
			if _, err := st.BeginMetricWake(ctx, "agent-a", due); err == nil {
				t.Fatalf("%s agent opened a metric wake, want refusal", status)
			}
			var wakes int
			if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM wakes WHERE agent_id='agent-a'`).Scan(&wakes); err != nil {
				t.Fatalf("counting wakes: %v", err)
			}
			if wakes != 0 {
				t.Fatalf("%s agent created %d wakes, want none", status, wakes)
			}
		})
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE agents SET status='running' WHERE id='agent-a'`); err != nil {
		t.Fatalf("resuming agent: %v", err)
	}
	if jobs, err := st.ClaimMetricJobs(ctx, "agent-a", due, time.Minute, 10); err != nil || len(jobs) != 1 {
		t.Fatalf("running agent claim = %+v,%v, want the pending job", jobs, err)
	}
}

func TestMetricJobsAcceptanceMetricWakeIsStandaloneAudit(t *testing.T) {
	ctx := context.Background()
	st := metricAcceptanceStore(t)
	metricAcceptanceAgent(t, st, "agent-a")
	schedule := metricAcceptanceBase.Add(30 * time.Hour)
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE agents SET next_wake_at=? WHERE id='agent-a'`,
		schedule.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seeding agent note and schedule: %v", err)
	}

	at := metricAcceptanceBase
	wakeID, err := st.BeginMetricWake(ctx, "agent-a", at)
	if err != nil {
		t.Fatalf("BeginMetricWake: %v", err)
	}
	if wakeID <= 0 {
		t.Fatalf("BeginMetricWake returned id %d", wakeID)
	}
	var agentID, kind, started string
	if err := st.DB().QueryRowContext(ctx,
		`SELECT agent_id,kind,started_at FROM wakes WHERE id=?`, wakeID).
		Scan(&agentID, &kind, &started); err != nil {
		t.Fatalf("reading metric wake: %v", err)
	}
	if agentID != "agent-a" || kind != "metrics" {
		t.Fatalf("metric wake = agent %q kind %q, want agent-a kind metrics", agentID, kind)
	}
	startedAt, err := time.Parse(time.RFC3339Nano, started)
	if err != nil || !startedAt.Equal(at) {
		t.Fatalf("metric wake started_at = %q (%v), want %v", started, err, at)
	}

	state, err := st.AgentState(ctx, "agent-a")
	if err != nil {
		t.Fatalf("AgentState after metric wake: %v", err)
	}
	if !state.NextWake.Equal(schedule) {
		t.Fatalf("metric wake changed next_wake_at to %v, want %v", state.NextWake, schedule)
	}

	if _, err := st.BeginMetricWake(ctx, "agent-a", at.Add(time.Minute)); err == nil {
		t.Fatal("BeginMetricWake opened a second concurrent maintenance wake")
	}
	if err := st.FinishWake(ctx, wakeID, at.Add(2*time.Minute), 0, "completed"); err != nil {
		t.Fatalf("FinishWake on metric wake: %v", err)
	}
	state, err = st.AgentState(ctx, "agent-a")
	if err != nil || !state.NextWake.Equal(schedule) {
		t.Fatalf("agent state after finishing metric wake = %+v,%v", state, err)
	}

	secondID, err := st.BeginMetricWake(ctx, "agent-a", at.Add(3*time.Minute))
	if err != nil || secondID == wakeID {
		t.Fatalf("second sequential metric wake id = %d,%v, want a new wake", secondID, err)
	}
	if err := st.FinishWake(ctx, secondID, at.Add(4*time.Minute), 0, "completed"); err != nil {
		t.Fatalf("finishing second metric wake: %v", err)
	}

	agentWake, err := st.BeginWake(ctx, "agent-a", at.Add(5*time.Minute), at.Add(6*time.Minute))
	if err != nil {
		t.Fatalf("BeginWake after metric maintenance: %v", err)
	}
	var agentKind string
	if err := st.DB().QueryRowContext(ctx, `SELECT kind FROM wakes WHERE id=?`, agentWake).Scan(&agentKind); err != nil {
		t.Fatalf("reading agent wake kind: %v", err)
	}
	if agentKind != "agent" {
		t.Fatalf("ordinary wake kind = %q, want default agent", agentKind)
	}
}

func TestMetricJobsAcceptanceMissingCancelledAndClosedStore(t *testing.T) {
	ctx := context.Background()
	st := metricAcceptanceStore(t)
	metricAcceptanceAgent(t, st, "agent-a")

	if _, err := st.SeedMetricJobs(ctx, "", time.Unix(0, 0)); err == nil {
		t.Fatal("SeedMetricJobs accepted an empty agent id")
	}
	if _, err := st.ClaimMetricJobs(ctx, "", metricAcceptanceBase, time.Minute, 1); err == nil {
		t.Fatal("ClaimMetricJobs accepted an empty agent id")
	}
	if next, ok, err := st.NextMetricDue(ctx, ""); err != nil || ok || !next.IsZero() {
		t.Fatalf("NextMetricDue with empty agent = %v,%v,%v, want a safe empty result", next, ok, err)
	}
	if _, err := st.BeginMetricWake(ctx, "", metricAcceptanceBase); err == nil {
		t.Fatal("BeginMetricWake accepted an empty agent id")
	}

	if seeded, err := st.SeedMetricJobs(ctx, "agent-a", time.Unix(0, 0)); err != nil || seeded != 0 {
		t.Fatalf("seed with no actions = %d,%v, want 0,nil", seeded, err)
	}
	if jobs, err := st.ClaimMetricJobs(ctx, "agent-a", metricAcceptanceBase, time.Minute, 10); err != nil || len(jobs) != 0 {
		t.Fatalf("claim with no jobs = %+v,%v, want none", jobs, err)
	}
	if next, ok, err := st.NextMetricDue(ctx, "agent-a"); err != nil || ok || !next.IsZero() {
		t.Fatalf("NextMetricDue with no jobs = %v,%v,%v, want zero,false,nil", next, ok, err)
	}

	actionID := metricAcceptanceAction(t, st, "agent-a", metricAcceptanceBase.Format(time.RFC3339Nano), "https://threads.example/cancel", false)
	if _, err := st.SeedMetricJobs(ctx, "agent-a", time.Unix(0, 0)); err != nil {
		t.Fatalf("seeding cancellation fixture: %v", err)
	}
	due := metricAcceptanceBase.Add(time.Hour)
	jobs, err := st.ClaimMetricJobs(ctx, "agent-a", due, 5*time.Minute, 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claiming cancellation fixture = %+v,%v", jobs, err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := st.SeedMetricJobs(cancelled, "agent-a", time.Unix(0, 0)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled SeedMetricJobs error = %v, want context.Canceled", err)
	}
	if jobs, err := st.ClaimMetricJobs(cancelled, "agent-a", due, time.Minute, 10); !errors.Is(err, context.Canceled) || len(jobs) != 0 {
		t.Fatalf("cancelled ClaimMetricJobs = %+v,%v, want context.Canceled", jobs, err)
	}
	if _, ok, err := st.NextMetricDue(cancelled, "agent-a"); !errors.Is(err, context.Canceled) || ok {
		t.Fatalf("cancelled NextMetricDue error = %v, want context.Canceled", err)
	}
	if _, err := st.BeginMetricWake(cancelled, "agent-a", due); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled BeginMetricWake error = %v, want context.Canceled", err)
	}
	if _, err := st.CompleteMetricJobs(cancelled, jobs, due, store.MetricValues{Views: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled CompleteMetricJobs error = %v, want context.Canceled", err)
	}
	if err := st.DeferMetricJobs(cancelled, jobs, due, due.Add(time.Hour), "cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled DeferMetricJobs error = %v, want context.Canceled", err)
	}
	if count := metricAcceptanceSampleCount(t, st, "agent-a", actionID); count != 0 {
		t.Fatalf("cancelled calls inserted %d samples", count)
	}
	if ok, err := st.CompleteMetricJobs(ctx, jobs, due.Add(time.Minute), store.MetricValues{Views: 5}); err != nil || !ok {
		t.Fatalf("completion after cancellation = %v,%v, want the claim still usable", ok, err)
	}

	closedPath := filepath.Join(t.TempDir(), "closed-metrics.db")
	closedStore, err := store.Open(ctx, closedPath)
	if err != nil {
		t.Fatalf("opening store for close test: %v", err)
	}
	metricAcceptanceAgent(t, closedStore, "agent-a")
	metricAcceptanceAction(t, closedStore, "agent-a", metricAcceptanceBase.Format(time.RFC3339Nano), "https://threads.example/closed", false)
	if _, err := closedStore.SeedMetricJobs(ctx, "agent-a", time.Unix(0, 0)); err != nil {
		t.Fatalf("seeding closed-store fixture: %v", err)
	}
	closedJobs, err := closedStore.ClaimMetricJobs(ctx, "agent-a", due, time.Minute, 10)
	if err != nil || len(closedJobs) != 1 {
		t.Fatalf("claiming closed-store fixture = %+v,%v", closedJobs, err)
	}
	if err := closedStore.Close(); err != nil {
		t.Fatalf("closing store before closed-store assertions: %v", err)
	}
	if _, err := closedStore.SeedMetricJobs(ctx, "agent-a", time.Unix(0, 0)); err == nil {
		t.Fatal("SeedMetricJobs succeeded on a closed store")
	}
	if _, err := closedStore.ClaimMetricJobs(ctx, "agent-a", due, time.Minute, 1); err == nil {
		t.Fatal("ClaimMetricJobs succeeded on a closed store")
	}
	if _, _, err := closedStore.NextMetricDue(ctx, "agent-a"); err == nil {
		t.Fatal("NextMetricDue succeeded on a closed store")
	}
	if _, err := closedStore.BeginMetricWake(ctx, "agent-a", due); err == nil {
		t.Fatal("BeginMetricWake succeeded on a closed store")
	}
	if _, err := closedStore.CompleteMetricJobs(ctx, closedJobs, due.Add(time.Minute), store.MetricValues{Views: 1}); err == nil {
		t.Fatal("CompleteMetricJobs succeeded on a closed store")
	}
	if err := closedStore.DeferMetricJobs(ctx, closedJobs, due, due.Add(time.Hour), "read_failed"); err == nil {
		t.Fatal("DeferMetricJobs succeeded on a closed store")
	}
}

func TestMetricJobsAcceptanceSchemaExposesWakeKind(t *testing.T) {
	ctx := context.Background()
	st := metricAcceptanceStore(t)
	rows, err := st.DB().QueryContext(ctx, `PRAGMA table_info(wakes)`)
	if err != nil {
		t.Fatalf("reading wakes columns: %v", err)
	}
	defer rows.Close()
	var found bool
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scanning wakes column: %v", err)
		}
		if name == "kind" {
			found = true
			var defaultText string
			switch raw := defaultValue.(type) {
			case string:
				defaultText = raw
			case []byte:
				defaultText = string(raw)
			default:
				defaultText = fmt.Sprint(raw)
			}
			if strings.Trim(defaultText, "'\"") != "agent" {
				t.Fatalf("wakes.kind default = %v, want agent", defaultValue)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating wakes columns: %v", err)
	}
	if !found {
		t.Fatal("schema has no wakes.kind")
	}

	metricAcceptanceAgent(t, st, "agent-a")
	if _, err := st.BeginMetricWake(ctx, "agent-a", metricAcceptanceBase); err != nil {
		t.Fatalf("BeginMetricWake: %v", err)
	}
	var kinds int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wakes WHERE kind IN ('agent','metrics')`).Scan(&kinds); err != nil {
		t.Fatalf("counting valid wake kinds: %v", err)
	}
	if kinds != 1 {
		t.Fatalf("wake kind rows = %d, want 1", kinds)
	}
}
