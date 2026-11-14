package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/tools"
)

// GuardCase drives the production guard/dispatcher and real SQLite directly.
// This component suite uses synthetic data, exact logical time and zero tokens;
// profile/protocol labels are report dimensions, not provider calls.
type GuardCase struct {
	PostsPerDay      int         `json:"posts_per_day,omitempty"`
	RepliesPerDay    int         `json:"replies_per_day,omitempty"`
	RepliesPerUser7d int         `json:"replies_per_user_7d,omitempty"`
	SpacingSeconds   int         `json:"spacing_seconds,omitempty"`
	BlockedPhrases   []string    `json:"blocked_phrases,omitempty"`
	StopPhrases      []string    `json:"stop_phrases,omitempty"`
	SelfStatUnits    []string    `json:"self_stat_units,omitempty"`
	CheckConstructor bool        `json:"check_constructor,omitempty"`
	TrendAnchor      bool        `json:"trend_anchor,omitempty"`
	Steps            []GuardStep `json:"steps"`
	ExpectedActions  int         `json:"expected_actions"`
}
type GuardStep struct {
	OffsetNS        int64            `json:"offset_ns,omitempty"`
	Call            *llm.ToolCall    `json:"call,omitempty"`
	Status          string           `json:"status,omitempty"`
	MarkUsername    string           `json:"mark_username,omitempty"`
	Observe         *StopObservation `json:"observe,omitempty"`
	ExpectedBlocked string           `json:"expected_blocked,omitempty"`
	// NoteTrend marks a trend, search or feed item as read this wake.
	NoteTrend string `json:"note_trend,omitempty"`
}
type StopObservation struct {
	Username        string `json:"username"`
	Text            string `json:"text"`
	Addressed       bool   `json:"addressed"`
	ExpectedMatched bool   `json:"expected_matched"`
}

func runGuardCase(ctx context.Context, c Case, p Profile, repeat int, opts Options, captured io.Writer) (r CaseResult) {
	run, err := beginCase(&r, c, p, repeat, "guard", "completed", "guard_setup_failed", nil)
	if err != nil {
		return r
	}
	defer run.end()
	if err = run.Open(ctx, "guard.db"); err != nil {
		return r
	}
	st, logger, logs := run.Store, run.Logger, &run.Logs
	now := opts.Now().UTC()
	base := now
	tmpl := agent.OfflineTemplate()
	tmpl.ID = "synthetic-guard-eval"
	tmpl.Limits.PostsPerDay = 100
	tmpl.Limits.RepliesPerDay = 100
	tmpl.Limits.RepliesPerUser7d = 100
	gc := c.Guard
	if gc.PostsPerDay > 0 {
		tmpl.Limits.PostsPerDay = gc.PostsPerDay
	}
	if gc.RepliesPerDay > 0 {
		tmpl.Limits.RepliesPerDay = gc.RepliesPerDay
	}
	if gc.RepliesPerUser7d > 0 {
		tmpl.Limits.RepliesPerUser7d = gc.RepliesPerUser7d
	}
	if gc.SpacingSeconds > 0 {
		tmpl.Limits.MinSecondsBetweenActions = gc.SpacingSeconds
	}
	tmpl.Guard.BlockedPhrases = gc.BlockedPhrases
	tmpl.Guard.StopPhrases = gc.StopPhrases
	tmpl.Guard.SelfStatUnits = gc.SelfStatUnits
	tmpl.Guard.TrendAnchor = gc.TrendAnchor
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		return r
	}
	wakeID, err := st.BeginWake(ctx, tmpl.ID, now, now.Add(time.Hour))
	if err != nil {
		return r
	}
	toolOpts := tools.Options{Scope: guard.Scope{Store: st, Template: tmpl, WakeID: wakeID, Now: func() time.Time { return now }, Logger: logger}, Threads: threads.NewMemory(), FetchTrends: evalTrends}
	d, err := tools.NewDispatcher(toolOpts)
	if err != nil {
		return r
	}
	g := d.Guard()
	fail := run.fail
	if gc.CheckConstructor {
		emptyScope := toolOpts
		emptyScope.Store = nil
		emptyScope.Template.ID = ""
		emptyScope.WakeID = 0
		if _, err = tools.NewDispatcher(emptyScope); err == nil {
			return fail("dispatcher_bypass")
		}
	}
	known := map[string]bool{}
	for _, def := range d.Definitions() {
		known[def.Function.Name] = true
	}
	for i, step := range gc.Steps {
		now = base.Add(time.Duration(step.OffsetNS))
		r.Steps++
		if step.Status != "" {
			if err = st.SetAgentStatus(ctx, tmpl.ID, step.Status); err != nil {
				return fail("guard_state_failed")
			}
		}
		if step.MarkUsername != "" {
			if err = st.MarkDoNotContact(ctx, tmpl.ID, step.MarkUsername, "owner_request"); err != nil {
				return fail("contact_setup_failed")
			}
		}
		if step.Observe != nil {
			o := step.Observe
			matched, err := g.ObserveStopRequest(ctx, o.Username, o.Text, o.Addressed)
			if err != nil || matched != o.ExpectedMatched {
				return fail("stop_detection_mismatch")
			}
		}
		if step.NoteTrend != "" {
			g.NoteTrend(step.NoteTrend)
		}
		if step.Call == nil {
			continue
		}
		call := *step.Call
		call.ID = fmt.Sprintf("synthetic-guard-%d", i)
		if call.Function.Name == "reply" {
			var a struct {
				TargetURL string `json:"target_url"`
				TargetID  string `json:"target_id"`
			}
			_ = json.Unmarshal([]byte(call.Function.Arguments), &a)
			g.NoteRead(a.TargetID, a.TargetURL, "")
		}
		_, callErr := d.Dispatch(ctx, call)
		blocked, err := st.LastBlockedBy(ctx)
		if err != nil {
			return fail("guard_audit_missing")
		}
		if blocked != step.ExpectedBlocked || (callErr != nil) != (step.ExpectedBlocked != "") {
			return fail("guard_rule_mismatch")
		}
		toolName := "unknown"
		if known[call.Function.Name] {
			toolName = call.Function.Name
		}
		r.ToolCalls = append(r.ToolCalls, toolName)
	}
	if tally, err := st.ActionTally(ctx, tmpl.ID); err != nil || tally.Total != gc.ExpectedActions {
		return fail("guard_action_count_mismatch")
	}
	// Scan actual captured events and audit metadata for payload canaries, not just
	// mocked refusals. The shared typed logging contract remains the sole emitter.
	for _, step := range gc.Steps {
		values := []string{step.MarkUsername}
		if step.Call != nil {
			var a map[string]any
			_ = json.Unmarshal([]byte(step.Call.Function.Arguments), &a)
			for _, key := range []string{"text", "target_username", "target_url", "username"} {
				if v, ok := a[key].(string); ok {
					values = append(values, v)
				}
			}
		}
		if step.Observe != nil {
			values = append(values, step.Observe.Username, step.Observe.Text)
		}
		for _, v := range values {
			if len(v) > 3 && strings.Contains(logs.String(), v) {
				return fail("guard_log_payload_leak")
			}
		}
	}
	if captured != nil {
		if _, err = captured.Write(logs.Bytes()); err != nil {
			return fail("guard_trace_failed")
		}
	}
	r.Passed, r.Reason = matches(c.Expect, r, "")
	return r
}
