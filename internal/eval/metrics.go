package eval

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/metrics"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
)

// Metric component cases exercise actual jobs and guarded reads. Profile labels
// let these results share reports; no model or network request is made.
func runMetricsCase(ctx context.Context, c Case, p Profile, repeat int, opts Options) (r CaseResult) {
	run, err := beginCase(&r, c, p, repeat, "metrics", "completed", "metrics_setup_failed", nil)
	if err != nil {
		return r
	}
	defer run.end()
	fail := run.fail
	logger, logs := run.Logger, &run.Logs
	if c.Scenario == "logical_soak" {
		e, err := RunMetricSimulation(ctx, run.Path("metrics.db"), 72, logger)
		if err != nil || e.Samples != 19 || e.CompletedTargets != 21 || e.Restarts != 1 || e.SourceReads != 20+e.FollowerReads {
			return fail("metric_simulation_failed")
		}
		r.LogicalHours = e.LogicalHours
		r.MetricSamples = e.Samples
		r.MetricTargets = e.CompletedTargets
		for range e.AuditReads {
			r.ToolCalls = append(r.ToolCalls, "read_own_metrics")
		}
		r.Passed, r.Reason = matches(c.Expect, r, "")
		return r
	}
	if err = run.Open(ctx, "metrics.db"); err != nil {
		return r
	}
	st := run.Store
	start := opts.Now().UTC().Truncate(time.Hour)
	now := start
	clock := func() time.Time { return now }
	tmpl := agent.OfflineTemplate()
	tmpl.ID = "synthetic-metrics"
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		return r
	}
	insert := func(id int64, agentID string, dry bool, url string, posted time.Time) bool {
		_, err := st.SeedAction(ctx, store.SeededAction{ID: id, AgentID: agentID, Kind: "post", Text: "PRIVATE METRIC BODY", DryRun: dry, ThreadURL: url, MediaID: mediaOf(url), PostedAt: posted})
		return err == nil
	}
	const ownURL = "https://fixture.invalid/own/one"
	if !insert(1, tmpl.ID, false, ownURL, start) || !insert(2, "foreign-agent", false, "https://fixture.invalid/foreign", start) || !insert(3, tmpl.ID, true, "https://fixture.invalid/dry", start) || !insert(4, tmpl.ID, false, "", start) {
		return r
	}
	source := threads.NewMemory()
	source.Actions[mediaOf(ownURL)] = threads.Metrics{Views: 42, Likes: 3, Replies: 2, Reposts: 1}
	processor := func() (*metrics.Processor, error) {
		return metrics.New(metrics.Options{Store: st, Template: tmpl, Now: clock, Logger: logger, ThreadsFactory: func(context.Context, config.Template) (threads.Backend, error) { return source, nil }, FetchTrends: evalTrends})
	}
	proc, err := processor()
	if err != nil {
		return r
	}
	now = start.Add(time.Hour)
	expectedSamples, expectedTargets := 1, 1
	switch c.Scenario {
	case "windows":
		for _, h := range []int{1, 6, 24} {
			now = start.Add(time.Duration(h) * time.Hour)
			got, e := proc.RunDue(ctx)
			if e != nil || got.Samples != 1 || got.ClaimedJobs != 1 {
				return fail("metric_window_failed")
			}
		}
		expectedSamples, expectedTargets = 3, 3
		r.LogicalHours = 24
	case "catchup_restart":
		if _, _, err = proc.NextDue(ctx); err != nil {
			return r
		}
		if err = run.Open(ctx, "metrics.db"); err != nil {
			return r
		}
		st = run.Store
		proc, err = processor()
		if err != nil {
			return r
		}
		now = start.Add(25 * time.Hour)
		got, e := proc.RunDue(ctx)
		if e != nil || got.Samples != 1 || got.ClaimedJobs != 3 {
			return fail("metric_catchup_failed")
		}
		samples, e := st.RecentMetricSamples(ctx, tmpl.ID, 10)
		if e != nil || len(samples) != 1 || !samples[0].MeasuredAt.Equal(now) {
			return fail("metric_catchup_fabricated_time")
		}
		expectedTargets = 3
		r.LogicalHours = 25
	case "isolation":
		const second = "https://fixture.invalid/own/two"
		if !insert(5, tmpl.ID, false, second, start) {
			return r
		}
		source.Actions[mediaOf(second)] = threads.Metrics{Views: 84, Likes: 6}
		got, e := proc.RunDue(ctx)
		if e != nil || got.Samples != 2 {
			return fail("metric_isolation_failed")
		}
		expectedSamples, expectedTargets = 2, 2
		r.LogicalHours = 1
	case "cancelled":
		cancelled, cancel := context.WithCancel(ctx)
		defer cancel()
		source.Errors[threads.OpReadOwnMetrics] = context.Canceled
		source.Hook = func(context.Context, threads.Call) error { cancel(); return nil }
		got, e := proc.RunDue(cancelled)
		if !errors.Is(e, context.Canceled) || got.Samples != 0 || got.DeferredJobs != 1 || source.Count(threads.OpClose) != 1 {
			return fail("metric_cancellation_failed")
		}
		expectedSamples, expectedTargets = 0, 0
		r.EndReason = "cancelled"
		r.LogicalHours = 1
	case "missing":
		source.Errors[threads.OpReadOwnMetrics] = errors.Join(threads.ErrMetricsUnavailable, errors.New("PRIVATE METRIC FAILURE"))
		got, e := proc.RunDue(ctx)
		if e != nil || got.Samples != 0 || got.DeferredJobs != 1 {
			return fail("metric_missing_fabricated")
		}
		next, ok, e := proc.NextDue(ctx)
		if e != nil || !ok || !next.Equal(now.Add(metrics.DefaultRetryDelay)) {
			return fail("metric_retry_target_failed")
		}
		expectedSamples, expectedTargets = 0, 0
		r.LogicalHours = 1
	case "db_failure":
		if err = st.FailMetricInserts(ctx, true); err != nil {
			return r
		}
		got, e := proc.RunDue(ctx)
		if e == nil || got.Samples != 0 {
			return fail("metric_db_failure_hidden")
		}
		tally, err := st.MetricTally(ctx, tmpl.ID)
		if err != nil || tally.Samples != 0 {
			return fail("metric_partial_sample")
		}
		if tally.CompletedJobs != 0 {
			return fail("metric_partial_checkpoint")
		}
		if err = st.FailMetricInserts(ctx, false); err != nil {
			return r
		}
		now = now.Add(metrics.DefaultRetryDelay)
		got, e = proc.RunDue(ctx)
		if e != nil || got.Samples != 1 {
			return fail("metric_db_retry_failed")
		}
		r.EndReason = "persistence_error"
		r.LogicalHours = 1
	}
	// Completed targets must never be re-read. Deferred cases retain retry times.
	again, err := proc.RunDue(ctx)
	if err != nil || again.Samples != 0 || again.ClaimedJobs != 0 {
		return fail("metric_duplicate_execution")
	}
	tally, err := st.MetricTally(ctx, tmpl.ID, 2, 3, 4)
	if err != nil || tally.Samples != expectedSamples {
		return fail("metric_sample_count_failed")
	}
	r.MetricSamples = tally.Samples
	if tally.CompletedJobs != expectedTargets {
		return fail("metric_checkpoint_count_failed")
	}
	r.MetricTargets = tally.CompletedJobs
	if tally.Foreign != 0 {
		return fail("metric_foreign_sample")
	}
	if run.loadToolCalls(ctx, "", 0) != nil {
		return r
	}
	for _, private := range []string{"https://fixture.invalid", "PRIVATE METRIC BODY", "PRIVATE METRIC FAILURE"} {
		if strings.Contains(logs.String(), private) {
			return fail("metric_log_payload_leak")
		}
	}
	if !strings.Contains(logs.String(), logging.GuardChecked) || !strings.Contains(logs.String(), logging.MetricsFinished) {
		return fail("metric_logs_missing")
	}
	r.Passed, r.Reason = matches(c.Expect, r, "")
	return r
}

// mediaOf is the media id of a synthetic confirmed URL; an unconfirmed action has none.
func mediaOf(url string) string {
	if url == "" {
		return ""
	}
	return threads.SyntheticID(url)
}
