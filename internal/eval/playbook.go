package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/metrics"
	"github.com/canhta/threadz/internal/playbook"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/tools"
)

func validPlaybookScenario(s string) bool {
	switch s {
	case "mutation", "outcomes", "revision":
		return true
	}
	for _, c := range DefaultPlaybookSimulationConfigs() {
		if s == "simulation_"+c.Name {
			return true
		}
	}
	return false
}
func playbookFixtureDigest(cases []Case) string {
	for _, c := range cases {
		if c.Kind == "playbook" {
			b, _ := json.Marshal(DefaultPlaybookSimulationConfigs())
			h := sha256.Sum256(append([]byte("playbook-collection:bayes:age24..30h:kind:revision:"), b...))
			return hex.EncodeToString(h[:])
		}
	}
	return ""
}

// taggedPosts is the number of tagged posts a component scenario makes; the
// decision needs playbook.MinActions of them.
const taggedPosts = playbook.MinActions + 2

// Component cases use the actual guarded tools, SQLite, metric processor and
// decision rule. Simulations run the production posterior, decision and Thompson
// code with fixed seeds.

func runPlaybookCase(ctx context.Context, c Case, p Profile, repeat int, opts Options, captured io.Writer) (r CaseResult) {
	run, err := beginCase(&r, c, p, repeat, "playbook", "completed", "playbook_setup_failed", nil)
	if err != nil {
		return r
	}
	defer run.end()
	fail := run.fail
	if strings.HasPrefix(c.Scenario, "simulation_") {
		for _, cfg := range DefaultPlaybookSimulationConfigs() {
			if c.Scenario != "simulation_"+cfg.Name {
				continue
			}
			sim, e := RunPlaybookSimulation(cfg)
			if e != nil {
				return fail("playbook_simulation_failed")
			}
			r.Simulation = &sim
			if cfg.Lift == 0 && sim.FalseEstablishedRunRate > 0.05 {
				return fail("playbook_null_false_positive")
			}
			r.Passed, r.Reason = matches(c.Expect, r, "")
			return r
		}
		return fail("playbook_scenario_unknown")
	}
	e := run.Open(ctx, "playbook.db")
	if e != nil {
		return r
	}
	st, logger, logs := run.Store, run.Logger, &run.Logs
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
	if c.Scenario != "mutation" {
		if e = seedPlaybookControls(ctx, st, tmpl.ID, now); e != nil {
			return r
		}
	}
	wake, e := st.BeginWake(ctx, tmpl.ID, now, now.Add(time.Hour))
	if e != nil {
		return r
	}
	source := threads.NewMemory()
	source.Hook = func(_ context.Context, c threads.Call) error {
		if c.Op == threads.OpPost {
			url := fmt.Sprintf("https://fixture.invalid/post/%d", source.Count(threads.OpPost))
			source.Results[threads.OpPost] = url
			source.Actions[threads.SyntheticID(url)] = threads.Metrics{Views: 1000, Likes: 90, Replies: 10}
		}
		return nil
	}
	d, e := tools.NewDispatcher(tools.Options{Scope: guard.Scope{Store: st, Template: tmpl, WakeID: wake, Now: clock, Logger: logger, AllowLive: true}, Threads: source, FetchTrends: evalTrends, FetchStudy: evalStudy, Embedder: offlineEmbedder(st, tmpl, clock, logger)})
	if e != nil {
		return r
	}
	dispatch := func(name, args string) (string, error) {
		r.ToolCalls = append(r.ToolCalls, name)
		return d.Dispatch(ctx, llm.ToolCall{ID: fmt.Sprintf("local-%d", len(r.ToolCalls)), Type: "function", Function: llm.CallFunction{Name: name, Arguments: args}})
	}
	if c.Scenario == "mutation" {
		for _, args := range []string{`{"text":"rewrite","status":"established"}`, `{"text":"rewrite","n":50}`, `{"entries":[]}`, `{"text":"rewrite","delete":true}`, `{"text":"rewrite","hypothesis_id":0}`} {
			if _, e = dispatch("playbook_propose", args); e == nil {
				return fail("playbook_mutation_escaped")
			}
		}
		for i := 0; i < store.MaxTestingHypotheses-1; i++ {
			if _, e = dispatch("playbook_propose", fmt.Sprintf(`{"text":"testing %d"}`, i)); e != nil {
				return fail("playbook_propose_failed")
			}
		}
		if _, e = dispatch("playbook_propose", `{"text":"over the cap"}`); e == nil {
			return fail("playbook_cap_escaped")
		}
		out, e := dispatch("playbook_read", `{}`)
		if e != nil || strings.Contains(out, "FOREIGN") {
			return fail("playbook_read_scope")
		}
		entries, e = st.PlaybookEntries(ctx, tmpl.ID, 100)
		if e != nil || len(entries) != store.MaxTestingHypotheses {
			return fail("playbook_entry_count")
		}
		for _, entry := range entries {
			if entry.N != 0 || entry.Status != "testing" {
				return fail("playbook_tool_evidence")
			}
		}
	} else {
		for i := 0; i < taggedPosts; i++ {
			now = base.Add(time.Duration(i) * time.Minute)
			if _, e = dispatch("post", fmt.Sprintf(`{"text":"private%d","hypothesis_ids":[%d]}`, i, id)); e != nil {
				return fail("playbook_tagged_post_failed")
			}
		}
		if c.Scenario == "revision" {
			if _, e = dispatch("playbook_propose", fmt.Sprintf(`{"text":"AMENDED PRIVATE IDEA","hypothesis_id":%d}`, id)); e != nil {
				return fail("playbook_amendment_failed")
			}
		}
		if e = st.FinishWake(ctx, wake, now, 50, "completed"); e != nil {
			return r
		}
		now = base.Add(25 * time.Hour)
		proc, e := metrics.New(metrics.Options{Embedder: offlineEmbedder(st, tmpl, clock, logger), Store: st, Template: tmpl, Now: clock, Logger: logger, BatchSize: 100, ThreadsFactory: func(context.Context, config.Template) (threads.Backend, error) { return source, nil }, FetchTrends: evalTrends, FetchStudy: evalStudy})
		if e != nil {
			return r
		}
		got, e := proc.RunDue(ctx)
		if e != nil || got.Samples != taggedPosts {
			return fail("playbook_metric_pipeline_failed")
		}
		r.MetricSamples = got.Samples
		r.MetricTargets = 3 * taggedPosts
		r.LogicalHours = 25
		entries, e = st.PlaybookEntries(ctx, tmpl.ID, 100)
		if e != nil || len(entries) != 1 {
			return r
		}
		if c.Scenario == "outcomes" {
			if entries[0].N != playbook.MinActions || entries[0].Status != "established" || !strings.Contains(logs.String(), logging.PlaybookDecided) {
				return fail("playbook_decision_failed")
			}
		} else if entries[0].N != 0 || entries[0].Revision != 2 || entries[0].Status != "testing" {
			return fail("playbook_revision_leak")
		}
		if e = playbook.Process(ctx, st, tmpl.ID, tmpl.MinEffect, now, logger); e != nil {
			return fail("playbook_reconcile_failed")
		}
		if count, e := st.PlaybookOutcomeCount(ctx, tmpl.ID); e != nil || count != taggedPosts {
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

func seedPlaybookControls(ctx context.Context, st *store.Store, agentID string, now time.Time) error {
	posts := make([]store.SeededPost, 20)
	for i := range posts {
		posted := now.Add(-72*time.Hour + time.Duration(i)*time.Minute)
		posts[i] = store.SeededPost{Text: "PRIVATE CONTROL", URL: "https://fixture.invalid/control", PostedAt: posted, Measured: posted.Add(24 * time.Hour), Likes: 5, Lift: 0}
	}
	return st.SeedMeasuredPosts(ctx, agentID, posts)
}
