package metrics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/guard"
	"github.com/canhta/threadwalker/internal/llm"
	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/store"
	"github.com/canhta/threadwalker/internal/threads"
	"github.com/canhta/threadwalker/internal/tools"
)

type SimulationEvidence struct {
	Version          string             `json:"version"`
	Synthetic        bool               `json:"synthetic"`
	TimingKind       string             `json:"timing_kind"`
	LogicalHours     int                `json:"logical_hours"`
	Restarts         int                `json:"restarts"`
	ConfirmedActions int                `json:"confirmed_actions"`
	ExpectedSamples  int                `json:"expected_samples"`
	Samples          int                `json:"samples"`
	CompletedTargets int                `json:"completed_targets"`
	SourceReads      int                `json:"source_reads"`
	AuditReads       int                `json:"audit_reads"`
	MaintenanceWakes int                `json:"maintenance_wakes"`
	Observations     []SimulationSample `json:"observations"`
}
type SimulationSample struct {
	AgentID string `json:"agent_id"`
	store.MetricSample
}

// RunOfflineSimulation is synthetic setup plus the real processor, guard,
// persistence and restart recovery. It reads no credentials and uses no network.
// The file DB must be isolated and empty. Logical time never certifies live soak.
func RunOfflineSimulation(ctx context.Context, path string, hours int, logger *slog.Logger) (e SimulationEvidence, runErr error) {
	e = SimulationEvidence{Version: "v1", Synthetic: true, TimingKind: "logical_time", LogicalHours: hours, Observations: []SimulationSample{}}
	if hours < 48 || hours > 720 || path == "" || path == ":memory:" {
		return e, errors.New("metric simulation: isolated file database and 48..720 logical hours required")
	}
	st, err := store.Open(ctx, path)
	if err != nil {
		return e, metricError("opening metric simulation", err)
	}
	defer func() {
		if st != nil {
			if err := st.Close(); err != nil {
				runErr = errors.Join(runErr, metricError("closing metric simulation", err))
			}
		}
	}()
	var count int
	if err = st.DB().QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM agents)+(SELECT COUNT(*) FROM actions)+(SELECT COUNT(*) FROM metrics)`).Scan(&count); err != nil {
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
	reads := 0
	factory := func(context.Context, config.Template) (threads.Backend, error) {
		return &simulationSource{reads: &reads}, nil
	}
	processors := []*Processor{}
	create := func() error {
		processors = nil
		for _, tmpl := range templates {
			p, err := New(Options{Store: st, Template: tmpl, Now: clock, Logger: logger, ThreadsFactory: factory})
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
	insert := func(agentID string, posted time.Time, dry int, confirmed bool) error {
		url := ""
		if confirmed {
			url = fmt.Sprintf("https://fixture.invalid/own/%d", e.ConfirmedActions+1)
		}
		if _, err := st.DB().ExecContext(ctx, `INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url) VALUES(?,'post','SYNTHETIC PRIVATE BODY',?,?,?)`, agentID, dry, posted.Format(time.RFC3339Nano), url); err != nil {
			return err
		}
		if dry == 0 && confirmed {
			e.ConfirmedActions++
		}
		return nil
	}
	// Three already-overdue windows must coalesce into one current sample.
	if err = insert(templates[0].ID, start.Add(-26*time.Hour), 0, true); err != nil {
		return e, err
	}
	for _, tmpl := range templates {
		if err = insert(tmpl.ID, start, 1, true); err != nil {
			return e, err
		}
		if err = insert(tmpl.ID, start, 0, false); err != nil {
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
				if err = insert(tmpl.ID, now, 0, true); err != nil {
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
			jobs, err := st.ClaimMetricJobs(ctx, templates[0].ID, now, DefaultLeaseDuration, 1)
			if err != nil || len(jobs) == 0 {
				return e, errors.New("metric simulation: missing crash claim")
			}
			source, _ := factory(ctx, templates[0])
			g, err := guard.New(guard.Options{Store: st, Template: templates[0], WakeID: id, Now: clock, Logger: logger})
			if err != nil {
				return e, err
			}
			d, err := tools.NewDispatcher(g, tools.Options{Store: st, Template: templates[0], WakeID: id, Now: clock, Logger: logger, Threads: source})
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
	e.SourceReads = reads
	if err = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM metric_jobs WHERE completed_at IS NOT NULL`).Scan(&e.CompletedTargets); err != nil {
		return e, err
	}
	var stored int
	if err = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM metrics`).Scan(&stored); err != nil {
		return e, err
	}
	if err = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM tool_calls WHERE tool='read_own_metrics'`).Scan(&e.AuditReads); err != nil {
		return e, err
	}
	if err = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM wakes WHERE kind='metrics'`).Scan(&e.MaintenanceWakes); err != nil {
		return e, err
	}
	if e.Samples != e.ExpectedSamples || stored != e.ExpectedSamples || e.CompletedTargets != expectedTargets || e.SourceReads != e.ExpectedSamples+1 || e.AuditReads != e.SourceReads {
		return e, errors.New("metric simulation: sample, target or guarded read counts failed")
	}
	for _, tmpl := range templates {
		state, err := st.AgentState(ctx, tmpl.ID)
		if err != nil || !state.NextWake.Equal(chosen) || state.SelfNote != "" {
			return e, errors.New("metric simulation: maintenance changed agent state")
		}
	}
	rows, err := st.DB().QueryContext(ctx, `SELECT agent_id,action_id,measured_at,views,likes,replies,reposts FROM metrics ORDER BY measured_at,agent_id,action_id`)
	if err != nil {
		return e, err
	}
	defer rows.Close()
	for rows.Next() {
		var s SimulationSample
		var at string
		if err = rows.Scan(&s.AgentID, &s.ActionID, &at, &s.Views, &s.Likes, &s.Replies, &s.Reposts); err != nil {
			return e, err
		}
		s.MeasuredAt, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return e, errors.New("metric simulation: invalid measured timestamp")
		}
		e.Observations = append(e.Observations, s)
	}
	return e, rows.Err()
}

type simulationSource struct{ reads *int }

func (*simulationSource) ReadFeed(context.Context) (threads.Page, error) {
	return threads.Page{}, threads.ErrUnknownPage
}
func (*simulationSource) Search(context.Context, string) (threads.Page, error) {
	return threads.Page{}, threads.ErrUnknownPage
}
func (*simulationSource) ReadPost(context.Context, string) (threads.Page, error) {
	return threads.Page{}, threads.ErrUnknownPage
}
func (*simulationSource) ReadOwnMetrics(context.Context) (threads.Metrics, error) {
	return threads.Metrics{}, threads.ErrMetricsUnavailable
}
func (s *simulationSource) ReadActionMetrics(ctx context.Context, _ string) (threads.Metrics, error) {
	if err := ctx.Err(); err != nil {
		return threads.Metrics{}, err
	}
	*s.reads++
	return threads.Metrics{Views: 123, Likes: 7, Replies: 3, Reposts: 1}, nil
}
func (*simulationSource) Post(context.Context, string) (string, error) {
	return "", errors.New("simulation writes disabled")
}
func (*simulationSource) Reply(context.Context, string, string) (string, error) {
	return "", errors.New("simulation writes disabled")
}
func (*simulationSource) Close(context.Context) error { return nil }
