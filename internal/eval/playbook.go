package eval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/canhta/threadwalker/internal/agent"
	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/guard"
	"github.com/canhta/threadwalker/internal/llm"
	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/metrics"
	"github.com/canhta/threadwalker/internal/playbook"
	"github.com/canhta/threadwalker/internal/store"
	"github.com/canhta/threadwalker/internal/threads"
	"github.com/canhta/threadwalker/internal/tools"
)

func validPlaybookScenario(s string) bool {
	switch s {
	case "mutation", "outcomes", "revision", "drift":
		return true
	}
	for _, c := range playbook.DefaultSimulationConfigs() {
		if s == "simulation_"+c.Name {
			return true
		}
	}
	return false
}
func playbookFixtureDigest(cases []Case) string {
	for _, c := range cases {
		if c.PlaybookScenario != "" {
			b, _ := json.Marshal(playbook.DefaultSimulationConfigs())
			h := sha256.Sum256(append([]byte("playbook-collection-v1:controls300:age24..30h:history30d:kind:revision:"), b...))
			return hex.EncodeToString(h[:])
		}
	}
	return ""
}

// Component cases use the actual guarded tools, SQLite, metric processor and
// gate. Simulations execute the same pure score/baseline/gate with fixed seeds.
func runPlaybookCase(ctx context.Context, c Case, p Profile, repeat int, opts Options, captured io.Writer) (r CaseResult) {
	started := time.Now()
	r = CaseResult{CaseID: c.ID, ProfileID: p.ID, ProviderID: p.ProviderID, Model: p.Config.Model, Protocol: p.Config.Protocol, Repeat: repeat, EndReason: "completed", Reason: "playbook_setup_failed", ToolCalls: []string{}}
	defer func() { r.DurationMS = float64(time.Since(started).Nanoseconds()) / 1e6 }()
	fail := func(reason string) CaseResult { r.Passed = false; r.Reason = reason; return r }
	if strings.HasPrefix(c.PlaybookScenario, "simulation_") {
		for _, cfg := range playbook.DefaultSimulationConfigs() {
			if c.PlaybookScenario != "simulation_"+cfg.Name {
				continue
			}
			sim, e := playbook.RunSimulation(cfg)
			if e != nil {
				return fail("playbook_simulation_failed")
			}
			r.Simulation = &sim
			r.LogicalHours = cfg.HorizonDays * 24
			if cfg.EffectMultiplier == cfg.ControlMultiplier && sim.EstablishedRunFraction >= 0.05 {
				return fail("playbook_null_false_positive")
			}
			r.Passed, r.Reason = matches(c.Expect, r, "")
			return r
		}
		return fail("playbook_scenario_unknown")
	}
	dir, e := os.MkdirTemp("", "threadwalker-playbook-eval-*")
	if e != nil {
		return r
	}
	defer os.RemoveAll(dir)
	st, e := store.Open(ctx, filepath.Join(dir, "playbook.db"))
	if e != nil {
		return r
	}
	defer st.Close()
	var logs bytes.Buffer
	logger, _ := logging.New(&logs, "debug", "json")
	now := opts.Now().UTC()
	base := now
	clock := func() time.Time { return now }
	tmpl := agent.OfflineTemplate()
	tmpl.ID = "synthetic-playbook"
	tmpl.DryRun = false
	tmpl.Limits.PostsPerDay = 2000
	tmpl.Limits.MinSecondsBetweenActions = 1
	if e = st.EnsureAgent(ctx, tmpl.ID); e != nil {
		return r
	}
	if e = st.EnsureAgent(ctx, "foreign"); e != nil {
		return r
	}
	if _, e = st.InitializePlaybook(ctx, tmpl.ID, []string{"PRIVATE HYPOTHESIS"}, now); e != nil {
		return r
	}
	entries, e := st.PlaybookEntries(ctx, tmpl.ID, 100)
	if e != nil || len(entries) != 1 {
		return r
	}
	id := entries[0].ID
	if _, e = st.InitializePlaybook(ctx, "foreign", []string{"FOREIGN HYPOTHESIS"}, now); e != nil {
		return r
	}
	if c.PlaybookScenario != "mutation" {
		if e = seedPlaybookControls(ctx, st, tmpl.ID, now); e != nil {
			return r
		}
	}
	wake, e := st.BeginWake(ctx, tmpl.ID, now, now.Add(time.Hour))
	if e != nil {
		return r
	}
	source := &evalPlaybookSource{evalMetricSource: evalMetricSource{values: map[string]threads.Metrics{}}}
	g, e := guard.New(guard.Options{Store: st, Template: tmpl, WakeID: wake, Now: clock, Logger: logger, AllowLive: true})
	if e != nil {
		return r
	}
	d, e := tools.NewDispatcher(g, tools.Options{Store: st, Template: tmpl, WakeID: wake, Now: clock, Logger: logger, AllowLive: true, Threads: source})
	if e != nil {
		return r
	}
	dispatch := func(name, args string) (string, error) {
		r.ToolCalls = append(r.ToolCalls, name)
		return d.Dispatch(ctx, llm.ToolCall{ID: fmt.Sprintf("local-%d", len(r.ToolCalls)), Type: "function", Function: llm.CallFunction{Name: name, Arguments: args}})
	}
	if c.PlaybookScenario == "mutation" {
		for _, args := range []string{`{"text":"rewrite","status":"established"}`, `{"text":"rewrite","n":50}`, `{"entries":[]}`, `{"text":"rewrite","delete":true}`, `{"text":"rewrite","hypothesis_id":0}`} {
			if _, e = dispatch("playbook_propose", args); e == nil {
				return fail("playbook_mutation_escaped")
			}
		}
		for i := 0; i < 4; i++ {
			if _, e = dispatch("playbook_propose", fmt.Sprintf(`{"text":"testing %d"}`, i)); e != nil {
				return fail("playbook_propose_failed")
			}
		}
		if _, e = dispatch("playbook_propose", `{"text":"sixth"}`); e == nil {
			return fail("playbook_cap_escaped")
		}
		out, e := dispatch("playbook_read", `{}`)
		if e != nil || strings.Contains(out, "FOREIGN") {
			return fail("playbook_read_scope")
		}
		entries, e = st.PlaybookEntries(ctx, tmpl.ID, 100)
		if e != nil || len(entries) != 5 {
			return fail("playbook_entry_count")
		}
		for _, entry := range entries {
			if entry.N != 0 || entry.Successes != 0 || entry.Status != "testing" {
				return fail("playbook_tool_evidence")
			}
		}
	} else {
		for i := 0; i < 50; i++ {
			now = base.Add(time.Duration(i) * time.Minute)
			if _, e = dispatch("post", fmt.Sprintf(`{"text":"PRIVATE POST %d","hypothesis_ids":[%d]}`, i, id)); e != nil {
				return fail("playbook_tagged_post_failed")
			}
		}
		if c.PlaybookScenario == "revision" {
			if _, e = dispatch("playbook_propose", fmt.Sprintf(`{"text":"AMENDED PRIVATE IDEA","hypothesis_id":%d}`, id)); e != nil {
				return fail("playbook_amendment_failed")
			}
		}
		controlLikes := 10
		if c.PlaybookScenario == "drift" {
			controlLikes = 90
		}
		if e = seedPlaybookCurrentControls(ctx, st, tmpl.ID, base.Add(10*time.Minute), controlLikes); e != nil {
			return r
		}
		if e = st.FinishWake(ctx, wake, now, 50, "completed"); e != nil {
			return r
		}
		now = base.Add(25 * time.Hour)
		proc, e := metrics.New(metrics.Options{Store: st, Template: tmpl, Now: clock, Logger: logger, BatchSize: 100, ThreadsFactory: func(context.Context, config.Template) (threads.Backend, error) { return source, nil }})
		if e != nil {
			return r
		}
		got, e := proc.RunDue(ctx)
		if e != nil || got.Samples != 50 {
			return fail("playbook_metric_pipeline_failed")
		}
		r.MetricSamples = got.Samples
		r.MetricTargets = 150
		r.LogicalHours = 25
		entries, e = st.PlaybookEntries(ctx, tmpl.ID, 100)
		if e != nil || len(entries) != 1 {
			return r
		}
		if c.PlaybookScenario == "outcomes" {
			if entries[0].N != 50 || entries[0].Successes != 50 || entries[0].Status != "established" || !strings.Contains(logs.String(), logging.PlaybookDecided) {
				return fail("playbook_gate_decision_failed")
			}
		} else if c.PlaybookScenario == "drift" {
			if entries[0].Status != "testing" || entries[0].N != 50 {
				return fail("playbook_drift_promoted")
			}
		} else if entries[0].N != 0 || entries[0].Revision != 2 || entries[0].Status != "testing" {
			return fail("playbook_revision_leak")
		}
		if e = playbook.Process(ctx, st, tmpl.ID, now, logger); e != nil {
			return fail("playbook_reconcile_failed")
		}
		var count int
		if e = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM playbook_outcomes WHERE agent_id=?`, tmpl.ID).Scan(&count); e != nil || count != 50 {
			return fail("playbook_duplicate_outcome")
		}
	}
	for _, private := range []string{"PRIVATE", "FOREIGN", "fixture.invalid"} {
		if strings.Contains(logs.String(), private) {
			return fail("playbook_log_payload_leak")
		}
	}
	foreign, e := st.PlaybookEntries(ctx, "foreign", 100)
	if e != nil || foreign[0].N != 0 {
		return fail("playbook_agent_leak")
	}
	if captured != nil {
		_, _ = captured.Write(logs.Bytes())
	}
	r.Passed, r.Reason = matches(c.Expect, r, "")
	return r
}

type evalPlaybookSource struct {
	evalMetricSource
	posts int
}

func (s *evalPlaybookSource) Post(ctx context.Context, _ string) (string, error) {
	if e := ctx.Err(); e != nil {
		return "", e
	}
	s.posts++
	url := fmt.Sprintf("https://fixture.invalid/post/%d", s.posts)
	s.values[url] = threads.Metrics{Views: 100, Likes: 90}
	return url, nil
}

func seedPlaybookControls(ctx context.Context, st *store.Store, agentID string, now time.Time) error {
	tx, e := st.DB().BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for i := 0; i < 300; i++ {
		posted := now.Add(-72*time.Hour + time.Duration(i)*time.Minute)
		at := posted.Add(24 * time.Hour)
		likes := 10
		if i >= 100 && i < 200 {
			likes = 5
		}
		if i >= 200 {
			likes = 20
		}
		result, e := tx.ExecContext(ctx, `INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url) VALUES(?,'post','PRIVATE CONTROL',0,?,'https://fixture.invalid/control')`, agentID, posted.Format(time.RFC3339Nano))
		if e != nil {
			return e
		}
		id, e := result.LastInsertId()
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO metrics(agent_id,action_id,measured_at,views,likes) VALUES(?,?,?,100,?)`, agentID, id, at.Format(time.RFC3339Nano), likes); e != nil {
			return e
		}
		for _, h := range []int{1, 6, 24} {
			due := posted.Add(time.Duration(h) * time.Hour).Format("2006-01-02T15:04:05.000000000Z")
			if _, e = tx.ExecContext(ctx, `INSERT INTO metric_jobs(agent_id,action_id,window_hours,due_at,not_before,completed_at) VALUES(?,?,?,?,?,?)`, agentID, id, h, due, due, at.Format(time.RFC3339Nano)); e != nil {
				return e
			}
		}
	}
	return tx.Commit()
}

func seedPlaybookCurrentControls(ctx context.Context, st *store.Store, agentID string, posted time.Time, likes int) error {
	tx, e := st.DB().BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for i := 0; i < 200; i++ {
		at := posted.Add(time.Duration(i) * time.Second)
		measured := at.Add(24 * time.Hour)
		result, e := tx.ExecContext(ctx, `INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url) VALUES(?,'post','PRIVATE CURRENT CONTROL',0,?,'https://fixture.invalid/current')`, agentID, at.Format(time.RFC3339Nano))
		if e != nil {
			return e
		}
		id, e := result.LastInsertId()
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO metrics(agent_id,action_id,measured_at,views,likes) VALUES(?,?,?,100,?)`, agentID, id, measured.Format(time.RFC3339Nano), likes); e != nil {
			return e
		}
		for _, h := range []int{1, 6, 24} {
			due := at.Add(time.Duration(h) * time.Hour).Format("2006-01-02T15:04:05.000000000Z")
			if _, e = tx.ExecContext(ctx, `INSERT INTO metric_jobs(agent_id,action_id,window_hours,due_at,not_before,completed_at) VALUES(?,?,?,?,?,?)`, agentID, id, h, due, due, measured.Format(time.RFC3339Nano)); e != nil {
				return e
			}
		}
	}
	return tx.Commit()
}

// Only bounded synthetic counts/rates enter shared reports. Reject malformed
// persisted summaries rather than accepting an impossible evidence claim.
func validSimulationMetadata(r playbook.SimulationReport) bool {
	total := r.Config.Runs * r.Config.Hypotheses
	if r.Version != playbook.SimulationVersion || !reportText(r.Config.Name) || r.Config.Runs < 1 || r.Config.Runs > 10000 || r.Config.Hypotheses < 1 || r.Config.Hypotheses > 5 || r.Config.HorizonDays < 1 || r.Config.HorizonDays > 1095 || r.EstablishedRuns < 0 || r.EstablishedRuns > r.Config.Runs || r.NullEstablishedRuns < 0 || r.NullEstablishedRuns > r.EstablishedRuns || r.EstablishedHypotheses < 0 || r.RejectedHypotheses < 0 || r.PendingHypotheses < 0 || r.UndetectedHypotheses != r.RejectedHypotheses+r.PendingHypotheses || r.EstablishedHypotheses+r.RejectedHypotheses+r.PendingHypotheses != total || len(r.DetectionDays) != r.EstablishedHypotheses || r.EstimatedPaidCalls != 0 || r.EstimatedPaidUSD != 0 {
		return false
	}
	for _, v := range []float64{r.EstablishedRunFraction, r.ControlSuccessRate, r.BaselineSuccessRate, r.TestSuccessRate} {
		if !finiteNonnegative(v) || v > 1 {
			return false
		}
	}
	if !finiteNonnegative(r.BaselineWarmupDays) || r.MeasuredControlOutcomes < 0 || r.UnavailableControlLooks < 0 || r.MeasuredTestOutcomes < 0 || r.CensoredOutcomes < 0 || r.PendingOutcomes < 0 || r.SyntheticActions < 0 {
		return false
	}
	expectedLooks := [...]int64{50, 100, 200, 400, 800, 1600}
	if len(r.LookCounts) != len(expectedLooks) {
		return false
	}
	var unavailable int64
	for i, look := range r.LookCounts {
		if look.N != expectedLooks[i] || look.Attempted < 0 || look.Unavailable < 0 || look.Unavailable > look.Attempted {
			return false
		}
		unavailable += look.Unavailable
	}
	if unavailable != r.UnavailableControlLooks {
		return false
	}
	for _, d := range r.DetectionDays {
		if d < 1 || d > r.Config.HorizonDays {
			return false
		}
	}
	return true
}
