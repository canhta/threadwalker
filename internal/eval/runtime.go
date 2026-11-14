package eval

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/tools"
)

// Runtime cases share suite identities, report comparison and accounting with
// client evals, but execute the production guarded dispatcher and wake lifecycle.
// All data and services are synthetic; there is no live-mode implementation.
func runRuntimeCase(ctx context.Context, c Case, p Profile, repeat int, opts Options) (r CaseResult) {
	run, err := beginCase(&r, c, p, repeat, "runtime", "runtime_error", "runtime_setup_failed", nil)
	if err != nil {
		return r
	}
	defer run.end()
	if err = run.Open(ctx, "eval.db"); err != nil {
		return r
	}
	st, logger, logs := run.Store, run.Logger, &run.Logs
	now := opts.Now()
	tmpl := agent.OfflineTemplate()
	tmpl.LLM = p.Config
	tmpl.LLM.BaseURL = "https://fixture.invalid"
	tmpl.LLM.APIKey = config.NewSecret("eval-runtime-dummy-key")
	steps, _, usd := effectiveLimits(opts, c.Limits)
	tmpl.Limits.MaxStepsPerWake = steps
	tmpl.Limits.MaxUSDPerDay = usd
	tmpl.Guard.BlockedPhrases = c.Runtime.BlockedPhrases
	fixture := agent.NewOfflineFixture(func() time.Time { return now })
	fixture.Calls = append([]llm.ToolCall{}, c.Runtime.Calls...)
	wrapped := 0
	invalidEnvelope := false
	sawBlocked := false
	fixture.Observe = func(messages []llm.Message) {
		wrapped = 0
		for _, m := range messages {
			if m.Role != "tool" {
				continue
			}
			if c.Runtime.ExpectedBlocked != "" && strings.Contains(m.Content, `"blocked":"`+c.Runtime.ExpectedBlocked+`"`) {
				sawBlocked = true
				continue
			}
			var envelope struct {
				Untrusted bool            `json:"untrusted"`
				Source    string          `json:"source"`
				Data      json.RawMessage `json:"data"`
			}
			if json.NewDecoder(strings.NewReader(m.Content)).Decode(&envelope) != nil {
				invalidEnvelope = true
				continue
			}
			if !envelope.Untrusted || envelope.Source == "" || len(envelope.Data) == 0 {
				invalidEnvelope = true
			}
			wrapped++
		}
	}
	create := func() (*agent.Runtime, error) {
		return agent.New(agent.Options{Store: st, Template: tmpl, Now: func() time.Time { return now }, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends, FetchStudy: fixture.Study, Logger: logger})
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
		d, err := tools.NewDispatcher(tools.Options{Scope: guard.Scope{Store: st, Template: tmpl, WakeID: id, Now: func() time.Time { return now }, Logger: logger}, Threads: threads.NewMemory(), FetchTrends: fixture.Trends, FetchStudy: fixture.Study, Embedder: offlineEmbedder(st, tmpl, func() time.Time { return now }, logger)})
		if err != nil {
			return r
		}
		call := agent.SyntheticCall("post", `{"text":"Synthetic interrupted action."}`)
		call.ID = "synthetic-crash"
		if _, err = d.Dispatch(ctx, call); err != nil {
			return r
		}
		if err = run.Open(ctx, "eval.db"); err != nil {
			return r
		}
		st = run.Store
		runtime, err = create()
		if err != nil {
			return r
		}
		if err = runtime.Start(ctx); err != nil {
			return r
		}
		if reason, err := st.WakeEndReason(ctx, id); err != nil || reason != "interrupted" {
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
	if err = run.loadToolCalls(ctx, "", wake.ID); err != nil {
		return r
	}
	totals, err := st.WakeTotals(ctx, "", wake.ID)
	if err != nil {
		return r
	}
	r.TokensIn, r.TokensOut, r.CostUSD = totals.TokensIn, totals.TokensOut, totals.CostUSD
	r.Passed, r.Reason = matches(c.Expect, r, "")
	if !r.Passed {
		return r
	}
	fail := run.fail
	state, err := st.AgentState(ctx, tmpl.ID)
	if err != nil {
		return fail("state_failed")
	}
	if !state.NextWake.Equal(now.Add(time.Duration(c.Runtime.ExpectedNextMinutes) * time.Minute)) {
		return fail("schedule_mismatch")
	}
	if tally, err := st.ActionTally(ctx, ""); err != nil || tally.DryRun != c.Runtime.ExpectedActions {
		return fail("dry_run_actions_mismatch")
	}
	if c.Runtime.ExpectedNote != "" {
		messages, err := runtime.Context(ctx, "agent")
		topics, topicsErr := st.TopicList(ctx, tmpl.ID, tmpl.DryRun)
		if err != nil || topicsErr != nil || len(topics) != 1 || topics[0].Description != c.Runtime.ExpectedNote || !strings.Contains(messages[1].Content, c.Runtime.ExpectedNote) {
			return fail("topic_note_missing")
		}
	}
	if c.Runtime.ExpectedBlocked != "" && !sawBlocked {
		return fail("blocked_result_missing")
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
