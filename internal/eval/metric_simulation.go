package eval

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/metrics"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/tools"
	"github.com/canhta/threadz/internal/trends"
)

type MetricSimulationEvidence struct {
	Synthetic        bool                     `json:"synthetic"`
	TimingKind       string                   `json:"timing_kind"`
	LogicalHours     int                      `json:"logical_hours"`
	Restarts         int                      `json:"restarts"`
	ConfirmedActions int                      `json:"confirmed_actions"`
	ExpectedSamples  int                      `json:"expected_samples"`
	Samples          int                      `json:"samples"`
	CompletedTargets int                      `json:"completed_targets"`
	SourceReads      int                      `json:"source_reads"`
	FollowerReads    int                      `json:"follower_reads"`
	AuditReads       int                      `json:"audit_reads"`
	MaintenanceWakes int                      `json:"maintenance_wakes"`
	Observations     []MetricSimulationSample `json:"observations"`
}
type MetricSimulationSample struct {
	AgentID string `json:"agent_id"`
	store.MetricSample
}

// RunMetricSimulation is synthetic setup plus the real processor, guard,
// persistence and restart recovery. It reads no credentials and uses no network.
// The file DB must be isolated and empty. Logical time never certifies live soak.
func RunMetricSimulation(ctx context.Context, path string, hours int, logger *slog.Logger) (e MetricSimulationEvidence, runErr error) {
	e = MetricSimulationEvidence{Synthetic: true, TimingKind: "logical_time", LogicalHours: hours, Observations: []MetricSimulationSample{}}
	if hours < 48 || hours > 720 || path == "" || path == ":memory:" {
		return e, errors.New("metric simulation: isolated file database and 48..720 logical hours required")
	}
	st, err := store.Open(ctx, path)
	if err != nil {
		return e, fmt.Errorf("opening metric simulation: %w", err)
	}
	defer func() {
		if st != nil {
			if err := st.Close(); err != nil {
				runErr = errors.Join(runErr, fmt.Errorf("closing metric simulation: %w", err))
			}
		}
	}()
	count, err := st.AgentCount(ctx)
	if err != nil {
		return e, err
	}
	if count != 0 {
		return e, errors.New("metric simulation: empty database required")
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start
	clock := func() time.Time { return now }
	templates := []config.Template{}
	chosen := start.Add(time.Duration(hours+24) * time.Hour)
	for _, id := range []string{"synthetic-metrics-a", "synthetic-metrics-b"} {
		tmpl := config.Template{ID: id, DryRun: true, Limits: config.LimitsConfig{MinMinutesBetweenWakes: 15}}
		templates = append(templates, tmpl)
		if err = st.EnsureAgent(ctx, id); err != nil {
			return e, err
		}
		if err = st.SetNextWake(ctx, id, chosen); err != nil {
			return e, err
		}
	}
	source := threads.NewMemory()
	factory := func(context.Context, config.Template) (threads.Backend, error) {
		return source, nil
	}
	processors := []*metrics.Processor{}
	create := func() error {
		processors = nil
		for _, tmpl := range templates {
			p, err := metrics.New(metrics.Options{Embedder: offlineEmbedder(st, tmpl, clock, logger), Store: st, Template: tmpl, Now: clock, Logger: logger, ThreadsFactory: factory, FetchTrends: simulationTrends, FetchStudy: simulationStudy})
			if err != nil {
				return err
			}
			processors = append(processors, p)
		}
		return nil
	}
	if err = create(); err != nil {
		return e, err
	}
	insert := func(agentID string, posted time.Time, dry, confirmed bool) error {
		url := ""
		if confirmed {
			url = fmt.Sprintf("https://fixture.invalid/own/%d", e.ConfirmedActions+1)
		}
		if _, err := st.SeedAction(ctx, store.SeededAction{AgentID: agentID, Kind: "post", Text: "SYNTHETIC PRIVATE BODY", DryRun: dry, ThreadURL: url, MediaID: mediaOf(url), PostedAt: posted}); err != nil {
			return err
		}
		if !dry && confirmed {
			source.Actions[mediaOf(url)] = threads.Metrics{Views: 123, Likes: 7, Replies: 3, Reposts: 1}
			e.ConfirmedActions++
		}
		return nil
	}
	// Three already-overdue windows must coalesce into one current sample.
	if err = insert(templates[0].ID, start.Add(-26*time.Hour), false, true); err != nil {
		return e, err
	}
	for _, tmpl := range templates {
		if err = insert(tmpl.ID, start, true, true); err != nil {
			return e, err
		}
		if err = insert(tmpl.ID, start, false, false); err != nil {
			return e, err
		}
	}
	for hour := 0; hour <= hours; hour++ {
		if err = ctx.Err(); err != nil {
			return e, err
		}
		now = start.Add(time.Duration(hour) * time.Hour)
		if hour%24 == 0 && hour < hours {
			for _, tmpl := range templates {
				if err = insert(tmpl.ID, now, false, true); err != nil {
					return e, err
				}
			}
		}
		if hour == 24 {
			// Simulate a crash after one guarded read, before sample/checkpoint commit.
			if _, _, err = processors[0].NextDue(ctx); err != nil {
				return e, err
			}
			id, err := st.BeginMetricWake(ctx, templates[0].ID, now)
			if err != nil {
				return e, err
			}
			jobs, err := st.ClaimMetricJobs(ctx, templates[0].ID, now, metrics.DefaultLeaseDuration, 1)
			if err != nil || len(jobs) == 0 {
				return e, errors.New("metric simulation: missing crash claim")
			}
			d, err := tools.NewDispatcher(tools.Options{Scope: guard.Scope{Store: st, Template: templates[0], WakeID: id, Now: clock, Logger: logger}, Threads: source, FetchTrends: simulationTrends, FetchStudy: simulationStudy, Embedder: offlineEmbedder(st, templates[0], clock, logger)})
			if err != nil {
				return e, err
			}
			call := llm.ToolCall{ID: "synthetic-crash-read", Type: "function", Function: llm.CallFunction{Name: "read_own_metrics", Arguments: fmt.Sprintf(`{"action_id":%d}`, jobs[0].ActionID)}}
			_, readErr := d.Dispatch(ctx, call)
			closeErr := source.Close(ctx)
			if readErr != nil || closeErr != nil {
				return e, errors.New("metric simulation: guarded crash read failed")
			}
			if err = st.Close(); err != nil {
				return e, err
			}
			st = nil
			st, err = store.Open(ctx, path)
			if err != nil {
				return e, err
			}
			hour = 26
			now = start.Add(26 * time.Hour)
			for _, tmpl := range templates {
				ids, err := st.RecoverWakes(ctx, tmpl.ID, now, now.Add(time.Hour))
				if err != nil {
					return e, err
				}
				for _, id := range ids {
					logging.Emit(ctx, logger, slog.LevelInfo, logging.Record{Event: logging.MetricsFinished, AgentID: tmpl.ID, WakeID: id, Operation: "metrics", Outcome: "stopped", Reason: "interrupted"})
				}
			}
			e.Restarts++
			if err = create(); err != nil {
				return e, err
			}
		}
		for _, p := range processors {
			result, err := p.RunDue(ctx)
			if err != nil {
				return e, err
			}
			e.Samples += result.Samples
			if result.ClaimedJobs > 0 {
				e.FollowerReads++
			}
			again, err := p.RunDue(ctx)
			if err != nil || again.Samples != 0 || again.ClaimedJobs != 0 {
				return e, errors.New("metric simulation: duplicate target execution")
			}
		}
	}
	expectedTargets := 3
	for posted := 0; posted < hours; posted += 24 {
		for _, window := range []int{1, 6, 24} {
			if posted+window <= hours {
				expectedTargets += 2
			}
		}
	}
	e.ExpectedSamples = expectedTargets - 2
	e.SourceReads = source.Count(threads.OpReadOwnMetrics)
	tally, err := st.MetricTally(ctx, "")
	if err != nil {
		return e, err
	}
	e.CompletedTargets = tally.CompletedJobs
	names, err := st.ToolCallNames(ctx, "", 0)
	if err != nil {
		return e, err
	}
	for _, name := range names {
		if name == "read_own_metrics" {
			e.AuditReads++
		}
	}
	if e.MaintenanceWakes, err = st.MetricsWakeCount(ctx); err != nil {
		return e, err
	}
	if e.Samples != e.ExpectedSamples || tally.Samples != e.ExpectedSamples || e.CompletedTargets != expectedTargets || e.SourceReads != e.ExpectedSamples+1+e.FollowerReads || e.AuditReads != e.SourceReads {
		return e, errors.New("metric simulation: sample, target or guarded read counts failed")
	}
	for _, tmpl := range templates {
		state, err := st.AgentState(ctx, tmpl.ID)
		if err != nil || !state.NextWake.Equal(chosen) {
			return e, errors.New("metric simulation: maintenance changed agent state")
		}
	}
	samples, err := st.AllMetricSamples(ctx)
	if err != nil {
		return e, err
	}
	for _, m := range samples {
		e.Observations = append(e.Observations, MetricSimulationSample{AgentID: m.AgentID, MetricSample: m.MetricSample})
	}
	return e, nil
}

func simulationTrends(context.Context) ([]trends.Trend, error) { return nil, nil }

func simulationStudy(context.Context) (trends.StudyFeed, error) { return trends.StudyFeed{}, nil }
