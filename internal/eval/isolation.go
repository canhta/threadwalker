package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/tools"
)

func validIsolationScenario(s string) bool {
	switch s {
	case "data", "controls", "own_accounts", "shared_links", "logical_soak":
		return true
	}
	return false
}
func runIsolationCase(ctx context.Context, c Case, p Profile, repeat int, opts Options) CaseResult {
	return runIsolationCaptured(ctx, c, p, repeat, opts, nil)
}

// Component evidence is collected from actual scoped rows and guarded calls.
// All fixtures are synthetic; profile labels do not imply provider comparison.
func runIsolationCaptured(ctx context.Context, c Case, p Profile, repeat int, opts Options, capture io.Writer) (r CaseResult) {
	run, err := beginCase(&r, c, p, repeat, "isolation", "completed", "isolation_setup_failed", capture)
	if err != nil {
		return r
	}
	defer run.end()
	fail := run.fail
	logger, logs, dir := run.Logger, &run.Logs, run.Dir
	dbPath := run.Path("isolation.db")
	if c.Scenario == "logical_soak" {
		e, err := agent.RunIsolationSoak(ctx, dbPath, 48, logger)
		if err != nil || !e.Serial || !e.StateIsolated || !e.ContextIsolated || !e.BudgetIsolated || e.MaintenanceWakes == 0 {
			return fail("logical_isolation_failed")
		}
		r.LogicalHours = e.LogicalHours
		r.Agents = e.Agents
	}
	if err = run.Open(ctx, "isolation.db"); err != nil {
		return r
	}
	st := run.Store
	now := opts.Now().UTC()
	clock := func() time.Time { return now }
	templates := []config.Template{agent.OfflineTemplate(), agent.OfflineTemplate()}
	private := []string{"PRIVATE_ISOLATION_A", "PRIVATE_ISOLATION_B"}
	users := []string{"fixture_account_a", "fixture_account_b"}
	runtimes := []*agent.Runtime{}
	dispatchers := []*tools.Dispatcher{}
	guards := []*guard.Guard{}
	wakes := []int64{}
	if c.Scenario != "logical_soak" {
		for i := range templates {
			tmpl := &templates[i]
			tmpl.ID = []string{"synthetic-isolation-a", "synthetic-isolation-b"}[i]
			tmpl.Threads.Username = users[i]
			tmpl.Threads.ProfileDir = filepath.Join(dir, tmpl.ID)
			tmpl.LLM.Protocol = p.Config.Protocol
			fixture := agent.NewOfflineFixture(clock)
			fixture.Calls = []llm.ToolCall{agent.SyntheticCall("fetch_trends", `{}`)}
			if _, err = st.SeedAction(ctx, store.SeededAction{AgentID: tmpl.ID, Kind: "post", Text: private[i], DryRun: tmpl.DryRun, PostedAt: now.Add(-time.Hour)}); err != nil {
				return r
			}
			runtime, err := agent.New(agent.Options{Store: st, Template: *tmpl, Now: clock, Logger: logger, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends})
			if err != nil {
				return r
			}
			if err = runtime.Start(ctx); err != nil {
				return r
			}
			runtimes = append(runtimes, runtime)
			if c.Scenario == "controls" {
				continue
			}
			id, err := st.BeginWake(ctx, tmpl.ID, now, now.Add(time.Hour))
			if err != nil {
				return r
			}
			wakes = append(wakes, id)
			d, err := tools.NewDispatcher(tools.Options{Scope: guard.Scope{Store: st, Template: *tmpl, OwnUsernames: users, WakeID: id, Now: clock, Logger: logger}, Threads: threads.NewMemory(), FetchTrends: fixture.Trends})
			if err != nil {
				return r
			}
			dispatchers = append(dispatchers, d)
			guards = append(guards, d.Guard())
		}
	}
	call := func(i int, name string, args any) (string, error) {
		call := agent.SyntheticCall(name, string(isolationJSON(args)))
		call.ID = fmt.Sprintf("synthetic-local-%d", len(r.ToolCalls)+1)
		return dispatchers[i].Dispatch(ctx, call)
	}
	// replyTo is a reply to a post agent i has read: the post is stored under its ref first.
	replyTo := func(i int, postURL string) map[string]string {
		return map[string]string{"text": "Synthetic reply", "target": guards[i].NotePost(ctx, threads.Post{ID: "fixture-id", URL: postURL})}
	}
	switch c.Scenario {
	case "data":
		if _, err = guards[0].ObserveStopRequest(ctx, "fixture_stranger", "stop", true); err != nil {
			return r
		}
		if _, err = call(0, "reply", replyTo(0, "https://threads.net/@fixture_stranger/post/1")); !errors.Is(err, guard.ErrBlocked) {
			return fail("contact_refusal_missing")
		}
		if _, err = call(1, "reply", replyTo(1, "https://threads.net/@fixture_stranger/post/1")); err != nil {
			return fail("contact_cross_agent")
		}
		if err = st.RecordLLMUsage(ctx, templates[0].ID, wakes[0], now.Format("2006-01-02"), store.LLMUsage{TokensIn: 100, TokensOut: 10, CostUSD: 0.25}); err != nil {
			return r
		}
		if err = st.RecordLLMUsage(ctx, templates[1].ID, wakes[0], now.Format("2006-01-02"), store.LLMUsage{CostUSD: 0.1}); err == nil {
			return fail("foreign_wake_accounted")
		}
		for i, runtime := range runtimes {
			messages, err := runtime.Context(ctx, "agent")
			if err != nil {
				return r
			}
			if strings.Contains(messages[1].Content, private[1-i]) {
				return fail("context_cross_agent")
			}
			var state struct {
				RemainingUSD float64 `json:"remaining_usd"`
			}
			// The data JSON leads the message; the text sections follow it.
			if json.NewDecoder(strings.NewReader(messages[1].Content)).Decode(&state) != nil {
				return r
			}
			expected := 1.0
			if i == 0 {
				expected = 0.75
			}
			if math.Abs(state.RemainingUSD-expected) > 1e-4 {
				return fail("ledger_cross_agent")
			}
		}
	case "controls":
		id, err := st.BeginWake(ctx, templates[0].ID, now, now)
		if err != nil {
			return r
		}
		if err = st.RecordLLMUsage(ctx, templates[0].ID, id, now.Format("2006-01-02"), store.LLMUsage{CostUSD: 1}); err != nil {
			return r
		}
		if err = st.FinishWake(ctx, id, now, 0, "completed"); err != nil {
			return r
		}
		if err = st.OwnerControl(ctx, templates[0].ID, "pause", "pause-fixture", "Synthetic owner pause", now); err != nil {
			return r
		}
		scheduler := agent.Scheduler{Agents: runtimes, Now: clock, Wait: func(context.Context, time.Duration) error { return errors.New("unexpected eval wait") }}
		if err = scheduler.Run(ctx, true); err != nil {
			return fail("paused_agent_blocked_peer")
		}
		if err = st.OwnerControl(ctx, templates[0].ID, "resume", "resume-fixture", "Synthetic owner resume", now); err != nil {
			return r
		}
		if err = scheduler.Run(ctx, true); err != nil {
			return fail("budget_agent_blocked_peer")
		}
		now = now.Add(time.Hour)
		// The budget-stopped agent waits for the next UTC day, so only its peer is due.
		if err = scheduler.Run(ctx, true); err != nil {
			return r
		}
		ua, err := st.LLMUsageForDay(ctx, templates[0].ID, now.Format("2006-01-02"))
		if err != nil {
			return r
		}
		ub, err := st.LLMUsageForDay(ctx, templates[1].ID, now.Format("2006-01-02"))
		if err != nil {
			return r
		}
		if ua.CostUSD != 1 || ub.CostUSD <= 0 {
			return fail("state_budget_cross_agent")
		}
	case "own_accounts":
		for _, args := range []map[string]string{
			replyTo(0, "https://threads.net/@fixture_account_b/post/1"),
			replyTo(0, "https://www.threads.com/@fixture_account_b/post/1"),
			{"text": "Mention @fixture_account_b"},
			{"text": "https://threads.net/fixture_account_b"},
		} {
			tool := "post"
			if args["target"] != "" {
				tool = "reply"
			}
			if _, err = call(0, tool, args); !errors.Is(err, guard.ErrBlocked) || !strings.Contains(err.Error(), "own_account") {
				return fail("own_account_allowed")
			}
		}
		// A confirmed own URL also blocks disguised author arguments on opaque paths.
		if _, err = st.SeedAction(ctx, store.SeededAction{AgentID: templates[1].ID, Kind: "post", Text: "synthetic confirmed fixture", DryRun: templates[1].DryRun, ThreadURL: "https://threads.net/@fixture_stranger/post/opaque-publication", PostedAt: now}); err != nil {
			return r
		}
		if _, err = call(0, "reply", replyTo(0, "https://threads.net/@fixture_stranger/post/opaque-publication?alias=1")); !errors.Is(err, guard.ErrBlocked) {
			return fail("own_publication_allowed")
		}
	case "shared_links":
		// An uncertain effect still owns its committed promotion intent.
		_, err = guards[0].Execute(ctx, guard.Attempt{Tool: "post", Arguments: `{"text":"https://example.invalid/article?a=1&b=2"}`}, func() (string, error) { return "", errors.New("synthetic uncertain effect") })
		if err == nil {
			return r
		}
		if _, err = call(1, "post", map[string]string{"text": "http://www.EXAMPLE.invalid:80/article/?b=2&a=1#fragment"}); !errors.Is(err, guard.ErrBlocked) || !strings.Contains(err.Error(), "shared_promotion_link") {
			return fail("shared_link_allowed")
		}
		if _, err = call(1, "post", map[string]string{"text": "https://example.invalid/article?a=2&b=2"}); err != nil {
			return fail("independent_link_blocked")
		}
	}
	// Scope/accounting metadata are collected independently from their actual
	// per-agent rows. Private fixture markers never become report fields.
	if len(r.Agents) == 0 {
		for _, t := range templates {
			a := agent.IsolationAgentEvidence{AgentID: t.ID}
			totals, err := st.WakeTotals(ctx, t.ID, 0)
			if err != nil {
				return r
			}
			a.TokensIn, a.TokensOut, a.CostUSD, a.CompletedWakes, a.BudgetStops = totals.TokensIn, totals.TokensOut, totals.CostUSD, totals.Completed, totals.Budget
			tally, err := st.ActionTally(ctx, t.ID)
			if err != nil {
				return r
			}
			a.DryRunActions = tally.DryRun
			r.Agents = append(r.Agents, a)
		}
	}
	for _, a := range r.Agents {
		r.TokensIn += a.TokensIn
		r.TokensOut += a.TokensOut
		r.CostUSD += a.CostUSD
	}
	if run.loadToolCalls(ctx, "", 0) != nil {
		return r
	}
	r.Steps = len(r.ToolCalls)
	for _, payload := range []string{private[0], private[1], "offline-dummy-key", "fixture_account_a", "fixture_account_b", "https://", "Synthetic permitted reply"} {
		if strings.Contains(logs.String(), payload) {
			return fail("isolation_log_payload_leak")
		}
	}
	r.Passed, r.Reason = matches(c.Expect, r, "")
	return r
}
func isolationJSON(v any) []byte { b, _ := json.Marshal(v); return b }
