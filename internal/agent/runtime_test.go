package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/canhta/threadwalker/internal/agent"
	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/llm"
	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/store"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWakeReadsWritesDryRunAndSchedules(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fixture := agent.NewOfflineFixture(func() time.Time { return now })
	r, err := agent.New(agent.Options{Store: st, Template: agent.OfflineTemplate(), Now: func() time.Time { return now }, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	wake, err := r.Wake(ctx)
	if err != nil || wake.EndReason != "completed" {
		t.Fatalf("%+v %v", wake, err)
	}
	state, err := st.AgentState(ctx, "synthetic-offline")
	if err != nil {
		t.Fatal(err)
	}
	if state.SelfNote != "Synthetic note for the next wake." || !state.NextWake.Equal(now.Add(time.Hour)) {
		t.Fatalf("%+v", state)
	}
	var count int
	st.DB().QueryRow(`SELECT COUNT(*) FROM actions WHERE dry_run=1`).Scan(&count)
	if count != 1 {
		t.Fatalf("actions=%d", count)
	}
	messages, err := r.Context(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(messages[1].Content, state.SelfNote) {
		t.Fatal("self-note absent in next context")
	}
	for _, m := range messages {
		if strings.Contains(m.Content, "offline-dummy-key") {
			t.Fatal("secret in context")
		}
	}
	observedCarryover := false
	fixture.Observe = func(messages []llm.Message) {
		if len(messages) > 1 && strings.Contains(messages[1].Content, state.SelfNote) {
			observedCarryover = true
		}
	}
	now = state.NextWake
	if _, err = r.Wake(ctx); err != nil {
		t.Fatal(err)
	}
	if !observedCarryover {
		t.Fatal("next wake did not receive prior self-note")
	}
}

func TestWakeStopReasonsAndScheduleDefaults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		calls  []agentCall
		steps  int
		reason string
		after  time.Duration
	}{
		{"default", nil, 30, "completed", time.Hour},
		{"step_limit", []agentCall{{"read_feed", `{}`}, {"read_own_metrics", `{}`}}, 1, "step_limit", time.Hour},
		{"stall_stop", []agentCall{{"note_search", `{"query":"synthetic"}`}, {"note_search", `{"query": "synthetic"}`}, {"note_search", `{"query":"synthetic"}`}}, 30, "stall_stop", time.Hour},
		{"schedule_min", []agentCall{{"schedule_next_wake", `{"at":"2025-01-01T00:00:00Z"}`}}, 30, "completed", 20 * time.Minute},
		{"schedule_max", []agentCall{{"schedule_next_wake", `{"at":"2027-01-01T00:00:00Z"}`}}, 30, "completed", 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			fixture := agent.NewOfflineFixture(func() time.Time { return now })
			fixture.Calls = []llm.ToolCall{}
			for _, c := range tc.calls {
				fixture.Calls = append(fixture.Calls, agent.SyntheticCall(c.name, c.args))
			}
			tmpl := agent.OfflineTemplate()
			tmpl.Limits.MaxStepsPerWake = tc.steps
			r, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: func() time.Time { return now }, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends})
			if err != nil {
				t.Fatal(err)
			}
			if err = r.Start(ctx); err != nil {
				t.Fatal(err)
			}
			wake, err := r.Wake(ctx)
			if err != nil || wake.EndReason != tc.reason {
				t.Fatalf("%+v %v", wake, err)
			}
			state, err := st.AgentState(ctx, tmpl.ID)
			if err != nil || !state.NextWake.Equal(now.Add(tc.after)) {
				t.Fatalf("%+v %v", state, err)
			}
			var reason string
			if err = st.DB().QueryRow(`SELECT end_reason FROM wakes WHERE id=?`, wake.ID).Scan(&reason); err != nil || reason != tc.reason {
				t.Fatalf("reason=%s err=%v", reason, err)
			}
		})
	}
}

type agentCall struct{ name, args string }

func TestSchedulerRestartDoesNotReplayAndHonorsOwnerControls(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "restart.db")
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fixture := agent.NewOfflineFixture(func() time.Time { return now })
	tmpl := agent.OfflineTemplate()
	st.EnsureAgent(ctx, tmpl.ID)
	id, err := st.BeginWake(ctx, tmpl.ID, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: func() time.Time { return now }, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Wake(ctx); err == nil {
		t.Fatal("interrupted wake was immediately rerun")
	}
	var reason string
	st.DB().QueryRow(`SELECT end_reason FROM wakes WHERE id=?`, id).Scan(&reason)
	if reason != "interrupted" {
		t.Fatal(reason)
	}
	now = now.Add(time.Hour)
	for _, status := range []string{"paused", "killed"} {
		st.DB().Exec(`UPDATE agents SET status=? WHERE id=?`, status, tmpl.ID)
		if _, err = r.Wake(ctx); err == nil {
			t.Fatal("owner control ignored")
		}
		if err = (agent.Scheduler{Agents: []*agent.Runtime{r}, Now: func() time.Time { return now }}).Run(ctx, true); err != nil {
			t.Fatal(err)
		}
	}
	st.DB().Exec(`UPDATE agents SET status='running' WHERE id=?`, tmpl.ID)
	if _, err = r.Wake(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	st.DB().QueryRow(`SELECT COUNT(*) FROM actions`).Scan(&count)
	if count != 1 {
		t.Fatalf("actions=%d", count)
	}
}

func TestEveryExternalToolResultIsUntrustedAndLogsAreRedacted(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "log.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fixture := agent.NewOfflineFixture(func() time.Time { return now })
	fixture.Calls = []llm.ToolCall{agent.SyntheticCall("read_feed", `{}`), agent.SyntheticCall("search", `{"query":"private query"}`), agent.SyntheticCall("read_post", `{"url":"https://synthetic.invalid/post"}`), agent.SyntheticCall("read_own_metrics", `{}`), agent.SyntheticCall("fetch_trends", `{}`), agent.SyntheticCall("note_write", `{"text":"private own note"}`)}
	seen := 0
	fixture.Observe = func(messages []llm.Message) {
		for _, m := range messages {
			if m.Role != "tool" {
				continue
			}
			var v struct {
				Untrusted bool            `json:"untrusted"`
				Source    string          `json:"source"`
				Data      json.RawMessage `json:"data"`
			}
			json.Unmarshal([]byte(m.Content), &v)
			if v.Source == "note_write" {
				continue
			}
			if seen < 5 {
				if !v.Untrusted || v.Source == "" || len(v.Data) == 0 {
					t.Fatalf("external tool result not wrapped: %s", m.Content)
				}
				seen++
			}
		}
	}
	var logs bytes.Buffer
	logger, _ := logging.New(&logs, "debug", "json")
	r, err := agent.New(agent.Options{Store: st, Template: agent.OfflineTemplate(), Now: func() time.Time { return now }, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	r.Start(ctx)
	if _, err = r.Wake(ctx); err != nil {
		t.Fatal(err)
	}
	if seen != 5 {
		t.Fatalf("external results=%d", seen)
	}
	for _, s := range []string{"offline-dummy-key", "private own note", "private query", "https://synthetic.invalid", "Synthetic offline trend"} {
		if strings.Contains(logs.String(), s) {
			t.Fatalf("log leaked %q", s)
		}
	}
	for _, event := range []string{"wake.started", "wake.finished", "tool.started", "tool.finished", "guard.checked", "audit.recorded", "context.built", "scheduler.scheduled", "note.written", "trends.fetched"} {
		if !strings.Contains(logs.String(), `"event":"`+event+`"`) {
			t.Errorf("missing %s", event)
		}
	}
}

func TestSchedulerWaitsAndSerializesConfiguredAgents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "scheduler.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	agents := []*agent.Runtime{}
	observed := []string{}
	for _, id := range []string{"first", "second"} {
		tmpl := agent.OfflineTemplate()
		tmpl.ID = id
		fixture := agent.NewOfflineFixture(func() time.Time { return now })
		fixture.Calls = []llm.ToolCall{}
		fixture.Observe = func(_ []llm.Message) {
			observed = append(observed, id)
			if len(observed) == 2 {
				cancel()
			}
		}
		r, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: func() time.Time { return now }, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends})
		if err != nil {
			t.Fatal(err)
		}
		agents = append(agents, r)
	}
	err = (agent.Scheduler{Agents: agents, Now: func() time.Time { return now }}).Run(ctx, false)
	if !errors.Is(err, context.Canceled) || len(observed) != 2 || observed[0] != "first" || observed[1] != "second" {
		t.Fatalf("observed=%v err=%v", observed, err)
	}
	// Fresh context: persisted future schedule must be honored before another wake.
	ctx = context.Background()
	waited := time.Duration(0)
	err = (agent.Scheduler{Agents: agents[:1], Now: func() time.Time { return now }, Wait: func(ctx context.Context, d time.Duration) error { now = now.Add(d); waited += d; return nil }}).Run(ctx, true)
	if err != nil || waited != time.Hour {
		t.Fatalf("waited=%s err=%v", waited, err)
	}
}

func TestRuntimeFailsClosedAndRecordsBudgetStops(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tmpl := agent.OfflineTemplate()
	opts := agent.Options{Store: st, Template: tmpl}
	for _, bad := range []agent.Options{{}, {Store: st}, {Store: st, Template: config.Template{ID: "a", DryRun: false}}, {Store: st, Template: config.Template{ID: "a", DryRun: true}}} {
		if _, err = agent.New(bad); err == nil {
			t.Fatal("bad runtime accepted")
		}
	}
	opts.Template.Limits.MinMinutesBetweenWakes = 1441
	if _, err = agent.New(opts); err == nil {
		t.Fatal("impossible minimum accepted")
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fixture := agent.NewOfflineFixture(func() time.Time { return now })
	r, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: func() time.Time { return now }, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends})
	if err != nil {
		t.Fatal(err)
	}
	r.Start(ctx)
	_, err = st.DB().Exec(`INSERT INTO ledger(agent_id,day,cost_usd) VALUES(?,?,?)`, tmpl.ID, "2026-01-01", 1)
	if err != nil {
		t.Fatal(err)
	}
	wake, err := r.Wake(ctx)
	if err != nil || wake.EndReason != "budget" {
		t.Fatalf("%+v %v", wake, err)
	}
	if err = (agent.Scheduler{}).Run(ctx, true); err == nil {
		t.Fatal("empty scheduler accepted")
	}
	if err = (agent.Scheduler{Agents: []*agent.Runtime{nil}}).Run(ctx, true); err == nil {
		t.Fatal("nil agent accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err = agent.Wait(cancelled, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = agent.Wait(ctx, 0); err != nil {
		t.Fatal(err)
	}
}
