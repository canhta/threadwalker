package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/canhta/threadwalker/internal/agent"
	"github.com/canhta/threadwalker/internal/guard"
	"github.com/canhta/threadwalker/internal/llm"
	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/store"
	"github.com/canhta/threadwalker/internal/tools"
)

// GuardCase drives the production guard/dispatcher and real SQLite directly.
// This component suite uses synthetic data, exact logical time and zero tokens;
// profile/protocol labels are report dimensions, not provider calls.
type GuardCase struct {
	PostsPerDay      int         `json:"posts_per_day,omitempty"`
	RepliesPerDay    int         `json:"replies_per_day,omitempty"`
	RepliesPerUser7d int         `json:"replies_per_user_7d,omitempty"`
	SpacingSeconds   int         `json:"spacing_seconds,omitempty"`
	ForbiddenPhrases []string    `json:"forbidden_phrases,omitempty"`
	StopPhrases      []string    `json:"stop_phrases,omitempty"`
	CheckConstructor bool        `json:"check_constructor,omitempty"`
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
}
type StopObservation struct {
	Username        string `json:"username"`
	Text            string `json:"text"`
	Addressed       bool   `json:"addressed"`
	ExpectedMatched bool   `json:"expected_matched"`
}

func runGuardCase(ctx context.Context, c Case, p Profile, repeat int, opts Options, captured io.Writer) (r CaseResult) {
	started := time.Now()
	r = CaseResult{CaseID: c.ID, ProfileID: p.ID, ProviderID: p.ProviderID, Model: p.Config.Model, Protocol: p.Config.Protocol, Repeat: repeat, Reason: "guard_setup_failed", EndReason: "completed", ToolCalls: []string{}}
	defer func() { r.DurationMS = float64(time.Since(started).Nanoseconds()) / 1e6 }()
	dir, err := os.MkdirTemp("", "threadwalker-guard-eval-*")
	if err != nil {
		return r
	}
	defer os.RemoveAll(dir)
	st, err := store.Open(ctx, filepath.Join(dir, "guard.db"))
	if err != nil {
		return r
	}
	defer st.Close()
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
	tmpl.Persona.ForbiddenPhrases = gc.ForbiddenPhrases
	tmpl.Guard.StopPhrases = gc.StopPhrases
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		return r
	}
	wakeID, err := st.BeginWake(ctx, tmpl.ID, now, now.Add(time.Hour))
	if err != nil {
		return r
	}
	var logs bytes.Buffer
	logger, _ := logging.New(&logs, "debug", "json")
	g, err := guard.New(guard.Options{Store: st, Template: tmpl, WakeID: wakeID, Now: func() time.Time { return now }, Logger: logger})
	if err != nil {
		return r
	}
	toolOpts := tools.Options{Store: st, Template: tmpl, WakeID: wakeID, Now: func() time.Time { return now }, Logger: logger, FetchTrends: func(context.Context) ([]tools.Trend, error) { return []tools.Trend{}, nil }}
	d, err := tools.NewDispatcher(g, toolOpts)
	if err != nil {
		return r
	}
	fail := func(reason string) CaseResult { r.Passed = false; r.Reason = reason; return r }
	if gc.CheckConstructor {
		if _, err = tools.NewDispatcher(nil, toolOpts); err == nil {
			return fail("dispatcher_bypass")
		}
		if _, err = tools.NewDispatcher(&guard.Guard{}, toolOpts); err == nil {
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
			if _, err = st.DB().ExecContext(ctx, `UPDATE agents SET status=? WHERE id=?`, step.Status, tmpl.ID); err != nil {
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
		if step.Call == nil {
			continue
		}
		call := *step.Call
		call.ID = fmt.Sprintf("synthetic-guard-%d", i)
		_, callErr := d.Dispatch(ctx, call)
		var blocked string
		if err = st.DB().QueryRowContext(ctx, `SELECT blocked_by FROM tool_calls ORDER BY id DESC LIMIT 1`).Scan(&blocked); err != nil {
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
	var count int
	if err = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM actions WHERE agent_id=?`, tmpl.ID).Scan(&count); err != nil || count != gc.ExpectedActions {
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
