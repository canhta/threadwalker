package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/canhta/threadwalker/internal/store"
	"github.com/canhta/threadwalker/internal/tools"
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
	started := time.Now()
	r = CaseResult{CaseID: c.ID, ProfileID: p.ID, ProviderID: p.ProviderID, Model: p.Config.Model, Protocol: p.Config.Protocol, Repeat: repeat, EndReason: "completed", Reason: "isolation_setup_failed", ToolCalls: []string{}}
	defer func() { r.DurationMS = float64(time.Since(started).Nanoseconds()) / 1e6 }()
	fail := func(reason string) CaseResult { r.Passed = false; r.Reason = reason; return r }
	dir, err := os.MkdirTemp("", "threadwalker-isolation-eval-*")
	if err != nil {
		return r
	}
	defer os.RemoveAll(dir)
	var logs bytes.Buffer
	writer := io.Writer(&logs)
	if capture != nil {
		writer = io.MultiWriter(writer, capture)
	}
	logger, _ := logging.New(writer, "debug", "json")
	dbPath := filepath.Join(dir, "isolation.db")
	if c.IsolationScenario == "logical_soak" {
		e, err := agent.RunIsolationSoak(ctx, dbPath, 48, logger)
		if err != nil || !e.Serial || !e.StateIsolated || !e.ContextIsolated || !e.BudgetIsolated || e.MaintenanceWakes == 0 {
			return fail("logical_isolation_failed")
		}
		r.LogicalHours = e.LogicalHours
		r.Agents = e.Agents
	}
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return r
	}
	defer st.Close()
	now := opts.Now().UTC()
	clock := func() time.Time { return now }
	templates := []config.Template{agent.OfflineTemplate(), agent.OfflineTemplate()}
	private := []string{"PRIVATE_ISOLATION_A", "PRIVATE_ISOLATION_B"}
	users := []string{"fixture_account_a", "fixture_account_b"}
	runtimes := []*agent.Runtime{}
	dispatchers := []*tools.Dispatcher{}
	guards := []*guard.Guard{}
	wakes := []int64{}
	if c.IsolationScenario != "logical_soak" {
		for i := range templates {
			tmpl := &templates[i]
			tmpl.ID = []string{"synthetic-isolation-a", "synthetic-isolation-b"}[i]
			tmpl.Threads.Username = users[i]
			tmpl.Threads.ProfileDir = filepath.Join(dir, tmpl.ID)
			tmpl.SeedPlaybook = []string{private[i]}
			tmpl.LLM.Protocol = p.Config.Protocol
			fixture := agent.NewOfflineFixture(clock)
			fixture.Calls = []llm.ToolCall{agent.SyntheticCall("note_write", string(isolationJSON(map[string]string{"text": private[i]})))}
			runtime, err := agent.New(agent.Options{Store: st, Template: *tmpl, Now: clock, Logger: logger, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends})
			if err != nil {
				return r
			}
			if err = runtime.Start(ctx); err != nil {
				return r
			}
			runtimes = append(runtimes, runtime)
			if c.IsolationScenario == "controls" {
				continue
			}
			id, err := st.BeginWake(ctx, tmpl.ID, now, now.Add(time.Hour))
			if err != nil {
				return r
			}
			wakes = append(wakes, id)
			g, err := guard.New(guard.Options{Store: st, Template: *tmpl, OwnUsernames: users, WakeID: id, Now: clock, Logger: logger})
			if err != nil {
				return r
			}
			guards = append(guards, g)
			d, err := tools.NewDispatcher(g, tools.Options{Store: st, Template: *tmpl, WakeID: id, Now: clock, Logger: logger})
			if err != nil {
				return r
			}
			dispatchers = append(dispatchers, d)
		}
	}
	call := func(i int, name string, args any) (string, error) {
		call := agent.SyntheticCall(name, string(isolationJSON(args)))
		call.ID = fmt.Sprintf("synthetic-local-%d", len(r.ToolCalls)+1)
		return dispatchers[i].Dispatch(ctx, call)
	}
	switch c.IsolationScenario {
	case "data":
		for i := range templates {
			if _, err = call(i, "note_write", map[string]string{"text": private[i]}); err != nil {
				return r
			}
			output, err := call(i, "note_search", map[string]string{"query": "PRIVATE"})
			if err != nil || !strings.Contains(output, private[i]) || strings.Contains(output, private[1-i]) {
				return fail("notes_cross_agent")
			}
			output, err = call(i, "playbook_read", map[string]any{})
			if err != nil || !strings.Contains(output, private[i]) || strings.Contains(output, private[1-i]) {
				return fail("playbook_cross_agent")
			}
		}
		if _, err = guards[0].ObserveStopRequest(ctx, "fixture_stranger", "stop", true); err != nil {
			return r
		}
		reply := map[string]string{"text": "Synthetic permitted reply", "target_username": "fixture_stranger", "target_url": "https://threads.net/@fixture_stranger/post/1"}
		if _, err = call(0, "reply", reply); !errors.Is(err, guard.ErrBlocked) {
			return fail("contact_refusal_missing")
		}
		if _, err = call(1, "reply", reply); err != nil {
			return fail("contact_cross_agent")
		}
		if err = st.RecordLLMUsage(ctx, templates[0].ID, wakes[0], now.Format("2006-01-02"), store.LLMUsage{TokensIn: 100, TokensOut: 10, CostUSD: 0.25}); err != nil {
			return r
		}
		if err = st.RecordLLMUsage(ctx, templates[1].ID, wakes[0], now.Format("2006-01-02"), store.LLMUsage{CostUSD: 0.1}); err == nil {
			return fail("foreign_wake_accounted")
		}
		for i, runtime := range runtimes {
			messages, err := runtime.Context(ctx)
			if err != nil {
				return r
			}
			if strings.Contains(messages[1].Content, private[1-i]) {
				return fail("context_cross_agent")
			}
			var state struct {
				RemainingUSD float64 `json:"remaining_usd"`
			}
			if json.Unmarshal([]byte(messages[1].Content), &state) != nil {
				return r
			}
			expected := 1.0
			if i == 0 {
				expected = 0.75
			}
			if state.RemainingUSD != expected {
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
		if err = scheduler.Run(ctx, true); err != nil {
			return r
		}
		if err = scheduler.Run(ctx, true); err != nil {
			return r
		}
		a, err := st.AgentState(ctx, templates[0].ID)
		if err != nil {
			return r
		}
		b, err := st.AgentState(ctx, templates[1].ID)
		if err != nil {
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
		if a.SelfNote != "" || b.SelfNote != private[1] || ua.CostUSD != 1 || ub.CostUSD <= 0 {
			return fail("state_budget_cross_agent")
		}
	case "own_accounts":
		for _, args := range []map[string]string{
			{"text": "Synthetic reply", "target_username": "@FIXTURE_ACCOUNT_B", "target_url": "https://threads.net/@fixture_account_b/post/1"},
			{"text": "Synthetic reply", "target_username": "fixture_stranger", "target_url": "https://www.threads.com/@fixture_account_b/post/1"},
			{"text": "Mention @fixture_account_b"},
			{"text": "https://threads.net/fixture_account_b"},
		} {
			tool := "post"
			if args["target_url"] != "" {
				tool = "reply"
			}
			if _, err = call(0, tool, args); !errors.Is(err, guard.ErrBlocked) || !strings.Contains(err.Error(), "own_account") {
				return fail("own_account_allowed")
			}
		}
		// A confirmed own URL also blocks disguised author arguments on opaque paths.
		if _, err = st.DB().ExecContext(ctx, `INSERT INTO actions(agent_id,kind,text,posted_at,thread_url) VALUES(?,'post','synthetic confirmed fixture',?,?)`, templates[1].ID, now.Format(time.RFC3339Nano), "https://threads.net/opaque-publication"); err != nil {
			return r
		}
		if _, err = call(0, "reply", map[string]string{"text": "Synthetic opaque reply", "target_username": "fixture_stranger", "target_url": "https://threads.net/opaque-publication?alias=1"}); !errors.Is(err, guard.ErrBlocked) {
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
			if err = st.DB().QueryRowContext(ctx, `SELECT COALESCE(SUM(tokens_in),0),COALESCE(SUM(tokens_out),0),COALESCE(SUM(cost_usd),0),COUNT(CASE WHEN end_reason='completed' THEN 1 END),COUNT(CASE WHEN end_reason='budget' THEN 1 END) FROM wakes WHERE agent_id=?`, t.ID).Scan(&a.TokensIn, &a.TokensOut, &a.CostUSD, &a.CompletedWakes, &a.BudgetStops); err != nil {
				return r
			}
			if err = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM actions WHERE agent_id=? AND dry_run=1`, t.ID).Scan(&a.DryRunActions); err != nil {
				return r
			}
			r.Agents = append(r.Agents, a)
		}
	}
	for _, a := range r.Agents {
		r.TokensIn += a.TokensIn
		r.TokensOut += a.TokensOut
		r.CostUSD += a.CostUSD
	}
	rows, err := st.DB().QueryContext(ctx, `SELECT tool FROM tool_calls ORDER BY id`)
	if err != nil {
		return r
	}
	for rows.Next() {
		var tool string
		if err = rows.Scan(&tool); err != nil {
			rows.Close()
			return r
		}
		r.ToolCalls = append(r.ToolCalls, tool)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
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
