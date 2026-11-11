package eval

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/canhta/threadwalker/internal/agent"
	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/metrics"
	"github.com/canhta/threadwalker/internal/store"
	"github.com/canhta/threadwalker/internal/threads"
)

// Metric component cases exercise actual jobs and guarded reads. Profile labels
// let these results share reports; no model or network request is made.
func runMetricsCase(ctx context.Context, c Case, p Profile, repeat int, opts Options) (r CaseResult) {
	started := time.Now()
	r = CaseResult{CaseID: c.ID, ProfileID: p.ID, ProviderID: p.ProviderID, Model: p.Config.Model, Protocol: p.Config.Protocol, Repeat: repeat, EndReason: "completed", Reason: "metrics_setup_failed", ToolCalls: []string{}}
	defer func() { r.DurationMS = float64(time.Since(started).Nanoseconds()) / 1e6 }()
	fail := func(reason string) CaseResult { r.Passed = false; r.Reason = reason; return r }
	dir, err := os.MkdirTemp("", "threadwalker-metrics-eval-*")
	if err != nil {
		return r
	}
	defer os.RemoveAll(dir)
	var logs bytes.Buffer
	logger, _ := logging.New(&logs, "debug", "json")
	path := filepath.Join(dir, "metrics.db")
	if c.MetricsScenario == "logical_soak" {
		e, err := metrics.RunOfflineSimulation(ctx, path, 72, logger)
		if err != nil || e.Samples != 19 || e.CompletedTargets != 21 || e.Restarts != 1 || e.SourceReads != 20 {
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
	st, err := store.Open(ctx, path)
	if err != nil {
		return r
	}
	defer func() {
		if st != nil {
			st.Close()
		}
	}()
	start := opts.Now().UTC().Truncate(time.Hour)
	now := start
	clock := func() time.Time { return now }
	tmpl := agent.OfflineTemplate()
	tmpl.ID = "synthetic-metrics"
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		return r
	}
	insert := func(id int64, agentID string, dry int, url string, posted time.Time) bool {
		_, err := st.DB().ExecContext(ctx, `INSERT INTO actions(id,agent_id,kind,text,dry_run,posted_at,thread_url) VALUES(?,?,'post','PRIVATE METRIC BODY',?,?,?)`, id, agentID, dry, posted.Format(time.RFC3339Nano), url)
		return err == nil
	}
	const ownURL = "https://fixture.invalid/own/one"
	if !insert(1, tmpl.ID, 0, ownURL, start) || !insert(2, "foreign-agent", 0, "https://fixture.invalid/foreign", start) || !insert(3, tmpl.ID, 1, "https://fixture.invalid/dry", start) || !insert(4, tmpl.ID, 0, "", start) {
		return r
	}
	source := &evalMetricSource{values: map[string]threads.Metrics{ownURL: {Views: 42, Likes: 3, Replies: 2, Reposts: 1}}}
	processor := func() (*metrics.Processor, error) {
		return metrics.New(metrics.Options{Store: st, Template: tmpl, Now: clock, Logger: logger, ThreadsFactory: func(context.Context, config.Template) (threads.Backend, error) { return source, nil }})
	}
	proc, err := processor()
	if err != nil {
		return r
	}
	now = start.Add(time.Hour)
	expectedSamples, expectedTargets := 1, 1
	switch c.MetricsScenario {
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
		if err = st.Close(); err != nil {
			return r
		}
		st = nil
		st, err = store.Open(ctx, path)
		if err != nil {
			return r
		}
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
		if !insert(5, tmpl.ID, 0, second, start) {
			return r
		}
		source.values[second] = threads.Metrics{Views: 84, Likes: 6}
		got, e := proc.RunDue(ctx)
		if e != nil || got.Samples != 2 {
			return fail("metric_isolation_failed")
		}
		expectedSamples, expectedTargets = 2, 2
		r.LogicalHours = 1
	case "cancelled":
		cancelled, cancel := context.WithCancel(ctx)
		defer cancel()
		source.read = func() error { cancel(); return context.Canceled }
		got, e := proc.RunDue(cancelled)
		if !errors.Is(e, context.Canceled) || got.Samples != 0 || got.DeferredJobs != 1 || source.closes != 1 {
			return fail("metric_cancellation_failed")
		}
		expectedSamples, expectedTargets = 0, 0
		r.EndReason = "cancelled"
		r.LogicalHours = 1
	case "missing":
		source.read = func() error { return errors.Join(threads.ErrMetricsUnavailable, errors.New("PRIVATE METRIC FAILURE")) }
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
		if _, err = st.DB().ExecContext(ctx, `CREATE TRIGGER synthetic_metric_failure BEFORE INSERT ON metrics BEGIN SELECT RAISE(ABORT,'PRIVATE METRIC FAILURE'); END`); err != nil {
			return r
		}
		got, e := proc.RunDue(ctx)
		if e == nil || got.Samples != 0 {
			return fail("metric_db_failure_hidden")
		}
		var n int
		if st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM metrics`).Scan(&n) != nil || n != 0 {
			return fail("metric_partial_sample")
		}
		if st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM metric_jobs WHERE completed_at IS NOT NULL`).Scan(&n) != nil || n != 0 {
			return fail("metric_partial_checkpoint")
		}
		if _, err = st.DB().ExecContext(ctx, `DROP TRIGGER synthetic_metric_failure`); err != nil {
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
	if st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM metrics`).Scan(&r.MetricSamples) != nil || r.MetricSamples != expectedSamples {
		return fail("metric_sample_count_failed")
	}
	if st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM metric_jobs WHERE completed_at IS NOT NULL`).Scan(&r.MetricTargets) != nil || r.MetricTargets != expectedTargets {
		return fail("metric_checkpoint_count_failed")
	}
	var foreign int
	if st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM metrics WHERE agent_id<>? OR action_id IN (2,3,4)`, tmpl.ID).Scan(&foreign) != nil || foreign != 0 {
		return fail("metric_foreign_sample")
	}
	rows, err := st.DB().QueryContext(ctx, `SELECT tool FROM tool_calls ORDER BY id`)
	if err != nil {
		return r
	}
	for rows.Next() {
		var name string
		if rows.Scan(&name) != nil {
			rows.Close()
			return r
		}
		r.ToolCalls = append(r.ToolCalls, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
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

type evalMetricSource struct {
	values map[string]threads.Metrics
	read   func() error
	closes int
}

func (*evalMetricSource) ReadFeed(context.Context) (threads.Page, error) {
	return threads.Page{}, threads.ErrUnknownPage
}
func (*evalMetricSource) Search(context.Context, string) (threads.Page, error) {
	return threads.Page{}, threads.ErrUnknownPage
}
func (*evalMetricSource) ReadPost(context.Context, string) (threads.Page, error) {
	return threads.Page{}, threads.ErrUnknownPage
}
func (*evalMetricSource) ReadOwnMetrics(context.Context) (threads.Metrics, error) {
	return threads.Metrics{}, threads.ErrMetricsUnavailable
}
func (s *evalMetricSource) ReadActionMetrics(ctx context.Context, url string) (threads.Metrics, error) {
	if s.read != nil {
		if err := s.read(); err != nil {
			return threads.Metrics{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return threads.Metrics{}, err
	}
	v, ok := s.values[url]
	if !ok {
		return threads.Metrics{}, threads.ErrMetricsUnavailable
	}
	return v, nil
}
func (*evalMetricSource) Post(context.Context, string) (string, error) {
	return "", errors.New("eval writes disabled")
}
func (*evalMetricSource) Reply(context.Context, string, string) (string, error) {
	return "", errors.New("eval writes disabled")
}
func (s *evalMetricSource) Close(context.Context) error { s.closes++; return nil }
