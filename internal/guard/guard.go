// Package guard owns the mandatory policy, audit and pre-effect intent boundary.
package guard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/store"
)

type Options struct {
	Store    *store.Store
	Template config.Template
	WakeID   int64
	Now      func() time.Time
	Logger   *slog.Logger
}
type Guard struct {
	opts        Options
	stopPhrases []string
}

// Attempt contains data used only for bounded audit metadata. Arguments are
// never persisted: even malformed calls may contain external text or secrets.
type Attempt struct {
	Tool, Arguments, BlockedBy string
	// ReservedActionID receives the committed intent ID before effect executes.
	// The dispatcher uses it to report the action without inserting a second row.
	ReservedActionID *int64
}

var ErrAudit = errors.New("tool audit failed")
var ErrBlocked = errors.New("tool blocked")

// ErrActionLimit stops this wake after a daily action quota refuses an intent.
// The agent stays running; its next wake can use the next UTC day quota.
var ErrActionLimit = errors.New("daily action limit")

type actionLimitError struct{ rule string }

func (e *actionLimitError) Error() string {
	return "executing guarded tool: " + e.rule + ": daily action limit"
}
func (e *actionLimitError) Unwrap() error           { return errors.Join(ErrBlocked, ErrActionLimit) }
func (e *actionLimitError) TerminalToolError() bool { return true }

type auditError struct{ cause error }

func (e *auditError) Error() string           { return "recording tool audit: failed" }
func (e *auditError) Unwrap() error           { return errors.Join(ErrAudit, e.cause) }
func (e *auditError) TerminalToolError() bool { return true }

func New(opts Options) (*Guard, error) {
	if opts.Store == nil || opts.Template.ID == "" || opts.WakeID <= 0 || !opts.Template.DryRun {
		return nil, errors.New("configuring guard: store, agent, wake and dry run required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	phrases := opts.Template.Guard.StopPhrases
	if phrases == nil {
		phrases = []string{"stop", "stop replying", "do not contact", "don’t contact", "don't contact", "leave me alone", "đừng trả lời", "đừng liên hệ", "ngừng trả lời", "dừng trả lời", "đừng làm phiền"}
	}
	for _, phrase := range phrases {
		if strings.TrimSpace(phrase) == "" {
			return nil, errors.New("configuring guard: empty stop phrase")
		}
	}
	opts.Template.Persona.ForbiddenPhrases = append([]string(nil), opts.Template.Persona.ForbiddenPhrases...)
	return &Guard{opts: opts, stopPhrases: append([]string(nil), phrases...)}, nil
}

// Matches prevents accidentally auditing one scope while mutating another.
func (g *Guard) Matches(st *store.Store, agentID string, wakeID int64) bool {
	return g != nil && g.opts.Store == st && g.opts.Template.ID == agentID && g.opts.WakeID == wakeID
}
func (g *Guard) event(ctx context.Context, event, outcome, reason, tool string) {
	if tool == "unknown" {
		tool = ""
	}
	logging.Emit(ctx, g.opts.Logger, slog.LevelInfo, logging.Record{Event: event, AgentID: g.opts.Template.ID, WakeID: g.opts.WakeID, Tool: tool, Operation: "tool_guard", Outcome: outcome, Reason: reason})
}

// Execute commits an attempt row before invoking effect. An unfinished audit row
// deliberately survives crashes; callers must never replay it. Result audit
// contains sizes/outcomes, never external bodies or profiles of strangers.
func (g *Guard) Execute(ctx context.Context, a Attempt, effect func() (string, error)) (string, error) {
	a.Tool = safeTool(a.Tool)
	if effect == nil {
		return "", fmt.Errorf("executing guarded tool: %w", ErrBlocked)
	}
	reason := a.BlockedBy
	if reason == "" && a.Tool == "unknown" {
		reason = "unknown_tool"
	}
	var running, open bool
	err := g.opts.Store.DB().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=? AND status='running'), EXISTS(SELECT 1 FROM wakes WHERE id=? AND agent_id=? AND ended_at IS NULL)`, g.opts.Template.ID, g.opts.WakeID, g.opts.Template.ID).Scan(&running, &open)
	if err != nil {
		g.event(ctx, "guard.checked", "error", "state_read_failed", a.Tool)
		return "", &auditError{err}
	}
	if reason == "" {
		if !running {
			reason = "agent_not_running"
		} else if !open {
			reason = "wake_not_open"
		}
	}
	var intent *actionIntent
	if reason == "" {
		intent, reason = g.content(a)
	}
	now := g.opts.Now().UTC()
	var object map[string]json.RawMessage
	valid := json.Unmarshal([]byte(a.Arguments), &object) == nil && object != nil
	metadata, _ := json.Marshal(struct {
		Bytes       int  `json:"bytes"`
		ValidObject bool `json:"valid_object"`
	}{len(a.Arguments), valid})
	row, err := g.opts.Store.DB().ExecContext(ctx, `INSERT INTO tool_calls(agent_id,wake_id,tool,args_json,blocked_by,at) VALUES(?,?,?,?,?,?)`, g.opts.Template.ID, g.opts.WakeID, a.Tool, string(metadata), reason, now.Format(time.RFC3339Nano))
	if err != nil {
		g.event(ctx, "audit.recorded", "error", "audit_insert_failed", a.Tool)
		return "", &auditError{err}
	}
	id, err := row.LastInsertId()
	if err != nil {
		return "", &auditError{err}
	}
	g.event(ctx, "audit.recorded", "success", "attempt_recorded", a.Tool)
	if reason == "" && intent != nil {
		var reserved int64
		reserved, reason, err = g.reserve(ctx, a, intent, id, now)
		if err != nil {
			g.event(ctx, "guard.checked", "error", "reservation_failed", a.Tool)
			return "", &auditError{err}
		}
		if a.ReservedActionID != nil {
			*a.ReservedActionID = reserved
		}
	}
	if reason != "" {
		g.event(ctx, "guard.checked", "blocked", reason, a.Tool)
		g.event(ctx, "tool.blocked", "blocked", reason, a.Tool)
		if reason == "posts_per_day" || reason == "replies_per_day" {
			return "", &actionLimitError{rule: reason}
		}
		return "", fmt.Errorf("executing guarded tool: %s: %w", reason, ErrBlocked)
	}
	g.event(ctx, "guard.checked", "allowed", "policy_allowed", a.Tool)
	output, effectErr := effect()
	outcome := "success"
	if effectErr != nil {
		outcome = "error"
	}
	result, _ := json.Marshal(struct {
		Outcome string `json:"outcome"`
		Bytes   int    `json:"bytes"`
	}{outcome, len(output)})
	if _, err := g.opts.Store.DB().ExecContext(ctx, `UPDATE tool_calls SET result_json=? WHERE id=?`, string(result), id); err != nil {
		g.event(ctx, "audit.recorded", "error", "audit_update_failed", a.Tool)
		return "", &auditError{err}
	}
	g.event(ctx, "audit.recorded", "success", "result_recorded", a.Tool)
	return output, effectErr
}

func safeTool(name string) string {
	switch name {
	case "note_write", "note_search", "schedule_next_wake", "fetch_trends", "read_feed", "search", "read_post", "post", "reply", "read_own_metrics", "mark_do_not_contact", "notify_owner", "ask_owner":
		return name
	default:
		return "unknown"
	}
}
