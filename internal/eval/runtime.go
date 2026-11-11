package eval

import (
	"bytes"
	"context"
	"encoding/json"
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

// Runtime cases share suite identities, report comparison and accounting with
// client evals, but execute the production guarded dispatcher and wake lifecycle.
// All data and services are synthetic; there is no live-mode implementation.
func runRuntimeCase(ctx context.Context, c Case, p Profile, repeat int, opts Options) (r CaseResult) {
	started := time.Now()
	r = CaseResult{CaseID: c.ID, ProfileID: p.ID, ProviderID: p.ProviderID, Model: p.Config.Model, Protocol: p.Config.Protocol, Repeat: repeat, EndReason: "runtime_error", Reason: "runtime_setup_failed", ToolCalls: []string{}}
	defer func() { r.DurationMS = float64(time.Since(started).Nanoseconds()) / 1e6 }()
	dir, err := os.MkdirTemp("", "threadwalker-runtime-eval-*")
	if err != nil {
		return r
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "eval.db")
	st, err := store.Open(ctx, path)
	if err != nil {
		return r
	}
	defer func() {
		if st != nil {
			st.Close()
		}
	}()
	now := opts.Now()
	tmpl := agent.OfflineTemplate()
	tmpl.LLM = p.Config
	tmpl.LLM.BaseURL = "https://fixture.invalid"
	tmpl.LLM.APIKey = config.NewSecret("eval-runtime-dummy-key")
	steps, _, usd := effectiveLimits(opts, c.Limits)
	tmpl.Limits.MaxStepsPerWake = steps
	tmpl.Limits.MaxUSDPerDay = usd
	fixture := agent.NewOfflineFixture(func() time.Time { return now })
	fixture.Calls = append([]llm.ToolCall{}, c.Runtime.Calls...)
	wrapped := 0
	invalidEnvelope := false
	fixture.Observe = func(messages []llm.Message) {
		for _, m := range messages {
			if m.Role != "tool" {
				continue
			}
			var envelope struct {
				Untrusted bool            `json:"untrusted"`
				Source    string          `json:"source"`
				Data      json.RawMessage `json:"data"`
			}
			if json.Unmarshal([]byte(m.Content), &envelope) != nil {
				invalidEnvelope = true
				continue
			}
			if !envelope.Untrusted || envelope.Source == "" || len(envelope.Data) == 0 {
				invalidEnvelope = true
			}
			wrapped++
		}
	}
	var logs bytes.Buffer
	logger, _ := logging.New(&logs, "debug", "json")
	create := func() (*agent.Runtime, error) {
		return agent.New(agent.Options{Store: st, Template: tmpl, Now: func() time.Time { return now }, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends, Logger: logger})
	}
	runtime, err := create()
	if err != nil {
		return r
	}
	if err = runtime.Start(ctx); err != nil {
		return r
	}
	if c.Runtime.Restart {
		id, err := st.BeginWake(ctx, tmpl.ID, now, now.Add(time.Hour))
		if err != nil {
			return r
		}
		g, err := guard.New(guard.Options{Store: st, Template: tmpl, WakeID: id, Now: func() time.Time { return now }, Logger: logger})
		if err != nil {
			return r
		}
		d, err := tools.NewDispatcher(g, tools.Options{Store: st, Template: tmpl, WakeID: id, Now: func() time.Time { return now }, Logger: logger, FetchTrends: fixture.Trends})
		if err != nil {
			return r
		}
		call := agent.SyntheticCall("post", `{"text":"Synthetic interrupted action."}`)
		call.ID = "synthetic-crash"
		if _, err = d.Dispatch(ctx, call); err != nil {
			return r
		}
		if st.Close() != nil {
			return r
		}
		st, err = store.Open(ctx, path)
		if err != nil {
			return r
		}
		runtime, err = create()
		if err != nil {
			return r
		}
		if err = runtime.Start(ctx); err != nil {
			return r
		}
		var reason string
		if err = st.DB().QueryRowContext(ctx, `SELECT end_reason FROM wakes WHERE id=?`, id).Scan(&reason); err != nil || reason != "interrupted" {
			r.Reason = "recovery_failed"
			return r
		}
		if _, err = runtime.Wake(ctx); err == nil {
			r.Reason = "interrupted_replayed"
			return r
		}
		state, err := st.AgentState(ctx, tmpl.ID)
		if err != nil || !state.NextWake.After(now) {
			r.Reason = "recovery_schedule_failed"
			return r
		}
		now = state.NextWake
	}
	wake, err := runtime.Wake(ctx)
	if err != nil {
		r.Reason = "runtime_failed"
		return r
	}
	r.Steps = wake.Steps
	r.EndReason = wake.EndReason
	rows, err := st.DB().QueryContext(ctx, `SELECT tool FROM tool_calls WHERE wake_id=? ORDER BY id`, wake.ID)
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
	if err = st.DB().QueryRowContext(ctx, `SELECT tokens_in,tokens_out,cost_usd FROM wakes WHERE id=?`, wake.ID).Scan(&r.TokensIn, &r.TokensOut, &r.CostUSD); err != nil {
		return r
	}
	r.Passed, r.Reason = matches(c.Expect, r, "")
	if !r.Passed {
		return r
	}
	fail := func(reason string) CaseResult { r.Passed = false; r.Reason = reason; return r }
	state, err := st.AgentState(ctx, tmpl.ID)
	if err != nil {
		return fail("state_failed")
	}
	if !state.NextWake.Equal(now.Add(time.Duration(c.Runtime.ExpectedNextMinutes) * time.Minute)) {
		return fail("schedule_mismatch")
	}
	var actions int
	if err = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM actions WHERE dry_run=1`).Scan(&actions); err != nil || actions != c.Runtime.ExpectedActions {
		return fail("dry_run_actions_mismatch")
	}
	if c.Runtime.ExpectedNote != "" {
		messages, err := runtime.Context(ctx)
		if err != nil || state.SelfNote != c.Runtime.ExpectedNote || !strings.Contains(messages[1].Content, c.Runtime.ExpectedNote) {
			return fail("self_note_missing")
		}
	}
	if c.Runtime.CheckUntrusted && (wrapped != len(c.Runtime.Calls) || invalidEnvelope) {
		return fail("untrusted_envelope_missing")
	}
	for _, s := range []string{"eval-runtime-dummy-key", "Synthetic interrupted action.", "https://synthetic.invalid", c.Runtime.ExpectedNote} {
		if s != "" && strings.Contains(logs.String(), s) {
			return fail("log_payload_leak")
		}
	}
	if !strings.Contains(logs.String(), `"event":"wake.started"`) || !(strings.Contains(logs.String(), `"event":"wake.finished"`) || strings.Contains(logs.String(), `"event":"wake.stopped"`)) {
		return fail("lifecycle_logs_missing")
	}
	return r
}
