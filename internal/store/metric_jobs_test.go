package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
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
	r, err := s.DB().Exec(`INSERT INTO actions(agent_id,kind,text,posted_at,thread_url,dry_run,media_id) VALUES(?,'post','',?,?,?,'m')`, agent, stamp, url, dry)
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
	if n, err := s.SeedMetricJobs(ctx, "a", time.Unix(0, 0)); err != nil || n != 3 {
		t.Fatalf("seed %d %v", n, err)
	}
	if n, err := s.SeedMetricJobs(ctx, "a", time.Unix(0, 0)); err != nil || n != 0 {
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
	if _, err := s.SeedMetricJobs(ctx, "a", time.Unix(0, 0)); err != nil {
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
	summary, err := s.OwnerSummary(ctx, "a", false, at, at.Add(48*time.Hour), "")
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

func metricCoreClaimAll(t *testing.T, s *store.Store) ([]store.MetricJob, time.Time) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC)
	metricCoreAction(t, s, "a", at.Format(time.RFC3339Nano), "https://example.test/own", false)
	if _, err := s.SeedMetricJobs(ctx, "a", time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	now := at.Add(25 * time.Hour)
	jobs, err := s.ClaimMetricJobs(ctx, "a", now, 5*time.Minute, 1)
	if err != nil || len(jobs) != 3 {
		t.Fatalf("claims %+v %v", jobs, err)
	}
	return jobs, now
}

func TestMetricCoreRejectsForgedPartialExpiredAndInvalidSamples(t *testing.T) {
	cases := []struct {
		name    string
		change  func([]store.MetricJob) []store.MetricJob
		advance time.Duration
		value   store.MetricValues
	}{
		{name: "partial", change: func(j []store.MetricJob) []store.MetricJob { return j[:1] }},
		{name: "duplicate", change: func(j []store.MetricJob) []store.MetricJob { return []store.MetricJob{j[0], j[0]} }},
		{name: "foreign agent", change: func(j []store.MetricJob) []store.MetricJob { j[0].AgentID = "b"; return j }},
		{name: "foreign action", change: func(j []store.MetricJob) []store.MetricJob { j[0].ActionID++; return j }},
		{name: "forged url", change: func(j []store.MetricJob) []store.MetricJob { j[0].ThreadURL = "https://example.test/foreign"; return j }},
		{name: "forged target", change: func(j []store.MetricJob) []store.MetricJob { j[0].DueAt = j[0].DueAt.Add(time.Nanosecond); return j }},
		{name: "forged posted", change: func(j []store.MetricJob) []store.MetricJob {
			j[0].PostedAt = j[0].PostedAt.Add(time.Nanosecond)
			return j
		}},
		{name: "forged version", change: func(j []store.MetricJob) []store.MetricJob {
			for i := range j {
				j[i].ClaimVersion++
			}
			return j
		}},
		{name: "invalid window", change: func(j []store.MetricJob) []store.MetricJob { j[0].WindowHours = 2; return j }},
		{name: "no jobs", change: func(j []store.MetricJob) []store.MetricJob { return nil }},
		{name: "expired", advance: 5 * time.Minute},
		{name: "before claim", advance: -time.Nanosecond},
		{name: "negative", value: store.MetricValues{Replies: -1}},
		{name: "oversized", value: store.MetricValues{Views: 1<<53 + 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := metricCoreStore(t)
			jobs, now := metricCoreClaimAll(t, s)
			original := append([]store.MetricJob(nil), jobs...)
			if tc.change != nil {
				jobs = tc.change(jobs)
			}
			if ok, err := s.CompleteMetricJobs(context.Background(), jobs, now.Add(tc.advance), tc.value); err == nil || ok {
				t.Fatalf("invalid completion %v %v", ok, err)
			}
			if ok, err := s.CompleteMetricJobs(context.Background(), original, now.Add(time.Nanosecond), store.MetricValues{Likes: 5}); err != nil || !ok {
				t.Fatalf("original claim damaged %v %v", ok, err)
			}
		})
	}
}

func TestMetricCorePersistenceFailureRollsBackSampleAndAllCheckpoints(t *testing.T) {
	s := metricCoreStore(t)
	jobs, now := metricCoreClaimAll(t, s)
	ctx := context.Background()
	if _, err := s.DB().Exec(`CREATE TRIGGER reject_metric_checkpoint BEFORE UPDATE ON metric_jobs WHEN NEW.window_hours=6 AND NEW.completed_at IS NOT NULL BEGIN SELECT RAISE(ABORT,'injected_checkpoint_failure'); END`); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.CompleteMetricJobs(ctx, jobs, now, store.MetricValues{Views: 42}); err == nil || ok {
		t.Fatalf("failure hidden %v %v", ok, err)
	}
	summary, err := s.OwnerSummary(ctx, "a", false, now.Add(-26*time.Hour), now.Add(time.Hour), "")
	if err != nil || summary.MetricSamples != 0 {
		t.Fatalf("partial sample %+v %v", summary, err)
	}
	if _, err = s.DB().Exec(`DROP TRIGGER reject_metric_checkpoint`); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.CompleteMetricJobs(ctx, jobs, now.Add(time.Nanosecond), store.MetricValues{Views: 43}); err != nil || !ok {
		t.Fatalf("checkpoint survived rollback %v %v", ok, err)
	}
}

func TestMetricCoreCancelledPersistenceDoesNotConsumeClaim(t *testing.T) {
	s := metricCoreStore(t)
	jobs, now := metricCoreClaimAll(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ok, err := s.CompleteMetricJobs(ctx, jobs, now, store.MetricValues{Views: 1}); !errors.Is(err, context.Canceled) || ok {
		t.Fatalf("cancelled complete %v %v", ok, err)
	}
	if err := s.DeferMetricJobs(ctx, jobs, now, now.Add(time.Hour), "cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled defer %v", err)
	}
	if ok, err := s.CompleteMetricJobs(context.Background(), jobs, now, store.MetricValues{Views: 2}); err != nil || !ok {
		t.Fatalf("claim consumed %v %v", ok, err)
	}
}

func TestMetricCoreDeferralIsAtomicSafeAndExact(t *testing.T) {
	s := metricCoreStore(t)
	jobs, now := metricCoreClaimAll(t, s)
	ctx := context.Background()
	retry := now.Add(5 * time.Minute)
	for _, reason := range []string{"", "external username", "https://example.test/private", "canhta"} {
		if err := s.DeferMetricJobs(ctx, jobs, now, retry, reason); err == nil {
			t.Fatalf("unsafe reason accepted %q", reason)
		}
	}
	if err := s.DeferMetricJobs(ctx, jobs, now, now, "read_failed"); err == nil {
		t.Fatal("nonfuture retry")
	}
	if _, err := s.DB().Exec(`CREATE TRIGGER reject_metric_defer BEFORE UPDATE ON metric_jobs WHEN NEW.window_hours=6 AND NEW.reason<>'' BEGIN SELECT RAISE(ABORT,'injected_defer_failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeferMetricJobs(ctx, jobs, now, retry, "read_failed"); err == nil {
		t.Fatal("deferral failure hidden")
	}
	if _, err := s.DB().Exec(`DROP TRIGGER reject_metric_defer`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeferMetricJobs(ctx, jobs, now, retry, "read_failed"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeferMetricJobs(ctx, jobs, now, retry, "read_failed"); !errors.Is(err, store.ErrMetricClaim) {
		t.Fatalf("released fence accepted %v", err)
	}
	due, ok, err := s.NextMetricDue(ctx, "a")
	if err != nil || !ok || !due.Equal(retry) {
		t.Fatalf("retry due %v %v %v", due, ok, err)
	}
	early, err := s.ClaimMetricJobs(ctx, "a", retry.Add(-time.Nanosecond), time.Minute, 1)
	if err != nil || len(early) != 0 {
		t.Fatalf("early retry %+v %v", early, err)
	}
	fresh, err := s.ClaimMetricJobs(ctx, "a", retry, time.Minute, 1)
	if err != nil || len(fresh) != 3 || fresh[0].ClaimVersion <= jobs[0].ClaimVersion {
		t.Fatalf("retry fence %+v %v", fresh, err)
	}
	if ok, err := s.CompleteMetricJobs(ctx, fresh, retry, store.MetricValues{}); err != nil || !ok {
		t.Fatalf("retry completion %v %v", ok, err)
	}
}

func TestMetricCoreMalformedPublishedTimeFailsWithoutPartialSeed(t *testing.T) {
	for _, stamp := range []string{"", "unknown", "2026-13-01T00:00:00Z", "0001-01-01T00:00:00Z", "9999-12-31T12:00:00Z", "2026-01-01T00:00:00.1234567891Z", "2026-01-01T00:00:00+24:00", "2026-01-01T0:00:00Z", "2026-01-01T00:00:00,1Z"} {
		t.Run(stamp, func(t *testing.T) {
			s := metricCoreStore(t)
			ctx := context.Background()
			metricCoreAction(t, s, "a", "2026-01-01T00:00:00Z", "https://example.test/good", false)
			metricCoreAction(t, s, "a", stamp, "https://example.test/bad", false)
			if n, err := s.SeedMetricJobs(ctx, "a", time.Unix(0, 0)); err == nil || n != 0 {
				t.Fatalf("seed %d %v", n, err)
			}
			if _, ok, err := s.NextMetricDue(ctx, "a"); err != nil || ok {
				t.Fatalf("partial seed %v %v", ok, err)
			}
		})
	}
}

func TestMetricCoreDeterministicActionLimitAndIndependentAgents(t *testing.T) {
	s := metricCoreStore(t)
	ctx := context.Background()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	later := metricCoreAction(t, s, "a", at.Add(time.Minute).Format(time.RFC3339Nano), "https://example.test/later", false)
	earlier := metricCoreAction(t, s, "a", at.Format(time.RFC3339Nano), "https://example.test/earlier", false)
	tie := metricCoreAction(t, s, "a", at.Format(time.RFC3339Nano), "https://example.test/tie", false)
	other := metricCoreAction(t, s, "b", at.Format(time.RFC3339Nano), "https://example.test/other", false)
	for _, agent := range []string{"a", "b"} {
		if _, err := s.SeedMetricJobs(ctx, agent, time.Unix(0, 0)); err != nil {
			t.Fatal(err)
		}
	}
	now := at.Add(48 * time.Hour)
	for _, want := range []int64{earlier, tie, later} {
		jobs, err := s.ClaimMetricJobs(ctx, "a", now, time.Minute, 1)
		if err != nil || len(jobs) != 3 || jobs[0].ActionID != want {
			t.Fatalf("deterministic group want %d got %+v %v", want, jobs, err)
		}
	}
	jobs, err := s.ClaimMetricJobs(ctx, "b", now, time.Minute, 1)
	if err != nil || len(jobs) != 3 || jobs[0].ActionID != other {
		t.Fatalf("other agent %+v %v", jobs, err)
	}
}

func TestMetricCoreConcurrentClaimsAndCompletions(t *testing.T) {
	s := metricCoreStore(t)
	ctx := context.Background()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	metricCoreAction(t, s, "a", at.Format(time.RFC3339Nano), "https://example.test/own", false)
	if _, err := s.SeedMetricJobs(ctx, "a", time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	now := at.Add(48 * time.Hour)
	start := make(chan struct{})
	results := make(chan []store.MetricJob, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			<-start
			jobs, err := s.ClaimMetricJobs(ctx, "a", now, time.Minute, 1)
			results <- jobs
			errs <- err
		}()
	}
	close(start)
	var claimed []store.MetricJob
	for i := 0; i < 8; i++ {
		jobs := <-results
		if len(jobs) > 0 {
			claimed = append(claimed, jobs...)
		}
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if len(claimed) != 3 {
		t.Fatalf("duplicate claim %+v", claimed)
	}
	begin := make(chan struct{})
	completed := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		go func() {
			<-begin
			ok, err := s.CompleteMetricJobs(ctx, claimed, now, store.MetricValues{Views: 10})
			completed <- ok
			errs <- err
		}()
	}
	close(begin)
	count := 0
	for i := 0; i < 8; i++ {
		if <-completed {
			count++
		}
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if count != 1 {
		t.Fatalf("committed %d samples", count)
	}
	summary, err := s.OwnerSummary(ctx, "a", false, at, at.Add(72*time.Hour), "")
	if err != nil || summary.MetricSamples != 1 || summary.Views != 10 {
		t.Fatalf("samples %+v %v", summary, err)
	}
}

func TestMetricCoreValidTimestampZonesAndDistantNanoseconds(t *testing.T) {
	for _, stamp := range []string{"2026-01-01T07:00:00.123456789+07:00", "2400-01-01T00:00:00.123456789Z", "2026-01-01T00:00:00.100000000+00:00"} {
		t.Run(stamp, func(t *testing.T) {
			s := metricCoreStore(t)
			ctx := context.Background()
			at, err := time.Parse(time.RFC3339Nano, stamp)
			if err != nil {
				t.Fatal(err)
			}
			metricCoreAction(t, s, "a", stamp, "https://example.test/own", false)
			if n, err := s.SeedMetricJobs(ctx, "a", time.Unix(0, 0)); err != nil || n != 3 {
				t.Fatalf("seed %d %v", n, err)
			}
			due, ok, err := s.NextMetricDue(ctx, "a")
			if err != nil || !ok || !due.Equal(at.Add(time.Hour)) {
				t.Fatalf("due %v %v %v", due, ok, err)
			}
			early, err := s.ClaimMetricJobs(ctx, "a", due.Add(-time.Nanosecond), time.Minute, 1)
			if err != nil || len(early) != 0 {
				t.Fatalf("early %+v %v", early, err)
			}
			jobs, err := s.ClaimMetricJobs(ctx, "a", due, time.Minute, 1)
			if err != nil || len(jobs) != 1 {
				t.Fatalf("boundary %+v %v", jobs, err)
			}
		})
	}
}

func TestMetricCorePausedAgentCannotClaimOrComplete(t *testing.T) {
	s := metricCoreStore(t)
	jobs, now := metricCoreClaimAll(t, s)
	ctx := context.Background()
	if err := s.PauseOwnerAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.NextMetricDue(ctx, "a"); err != nil || ok {
		t.Fatalf("paused due %v %v", ok, err)
	}
	if claimed, err := s.ClaimMetricJobs(ctx, "a", now.Add(5*time.Minute), time.Minute, 1); err != nil || len(claimed) != 0 {
		t.Fatalf("paused claim %+v %v", claimed, err)
	}
	if _, err := s.CompleteMetricJobs(ctx, jobs, now, store.MetricValues{Views: 1}); !errors.Is(err, store.ErrMetricClaim) {
		t.Fatalf("paused commit %v", err)
	}
	if err := s.DeferMetricJobs(ctx, jobs, now, now.Add(time.Hour), "auth_required"); err != nil {
		t.Fatalf("paused release %v", err)
	}
}

func TestMetricCoreNextDueRespectsActionWideLease(t *testing.T) {
	s := metricCoreStore(t)
	ctx := context.Background()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	metricCoreAction(t, s, "a", at.Format(time.RFC3339Nano), "https://example.test/own", false)
	if _, err := s.SeedMetricJobs(ctx, "a", time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	// The first window is held while the second becomes due. A second worker
	// must wait for the whole action lease, then read once for both windows.
	jobs, err := s.ClaimMetricJobs(ctx, "a", at.Add(time.Hour), 6*time.Hour, 1)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim %+v %v", jobs, err)
	}
	due, ok, err := s.NextMetricDue(ctx, "a")
	if err != nil || !ok || !due.Equal(at.Add(7*time.Hour)) {
		t.Fatalf("lease due %v %v %v", due, ok, err)
	}
	if claimed, err := s.ClaimMetricJobs(ctx, "a", at.Add(6*time.Hour), time.Minute, 1); err != nil || len(claimed) != 0 {
		t.Fatalf("overlap %+v %v", claimed, err)
	}
	recovered, err := s.ClaimMetricJobs(ctx, "a", due, time.Minute, 1)
	if err != nil || len(recovered) != 2 {
		t.Fatalf("recovered %+v %v", recovered, err)
	}
	if ok, err := s.CompleteMetricJobs(ctx, recovered, due, store.MetricValues{Views: 20}); err != nil || !ok {
		t.Fatalf("complete %v %v", ok, err)
	}
	due, ok, err = s.NextMetricDue(ctx, "a")
	if err != nil || !ok || !due.Equal(at.Add(24*time.Hour)) {
		t.Fatalf("remaining due %v %v %v", due, ok, err)
	}
}

func TestPendingMetricJobsCountsOnlyConfirmedOpenTargets(t *testing.T) {
	s := metricCoreStore(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	posted := now.Add(-time.Hour).Format(time.RFC3339Nano)
	metricCoreAction(t, s, "a", posted, "https://www.threads.com/@a/post/1", false)
	metricCoreAction(t, s, "a", posted, "https://www.threads.com/@a/post/2", true)
	seeded, err := s.SeedMetricJobs(ctx, "a", now)
	if err != nil || seeded == 0 {
		t.Fatalf("seeded=%d err=%v", seeded, err)
	}
	if n, err := s.PendingMetricJobs(ctx, "a"); err != nil || n != seeded {
		t.Fatalf("pending=%d seeded=%d err=%v", n, seeded, err)
	}
	if n, err := s.PendingMetricJobs(ctx, "b"); err != nil || n != 0 {
		t.Fatalf("other agent pending=%d err=%v", n, err)
	}
}
