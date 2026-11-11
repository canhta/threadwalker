// Package dashboard projects a bounded public view of local agent activity.
// Mutable content, credentials and unconfigured agent rows are never a view model.
package dashboard

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"math"
	"time"
)

const RowLimit = 20
const HypothesisLimit = 100
const AgentLimit = 100

type Page struct {
	GeneratedAt string
	Day         string
	Agents      []AgentView
}
type AgentView struct {
	ID             string
	Status         string
	DailyBudgetUSD float64
	SpentUSD       float64
	RemainingUSD   float64
	NextWake       string
	Wakes          []WakeView
	Actions        []ActionView
	Playbook       []HypothesisView
	Tools          []ToolView
}
type WakeView struct {
	ID        int64
	StartedAt string
	EndedAt   string
	EndReason string
	Kind      string
	Steps     int
	TokensIn  int64
	TokensOut int64
	CostUSD   float64
}
type ActionView struct {
	ID          int64
	Kind        string
	PublishedAt string
	URL         string
}
type HypothesisView struct {
	ID        int64
	Status    string
	N         int64
	Successes int64
}
type ToolView struct {
	ID      int64
	WakeID  int64
	Name    string
	At      string
	Outcome string
	Detail  string
}

// Agent contains only explicitly selected operator metadata. Username is used
// solely to prove publication-link ownership and never rendered independently.
type Agent struct {
	ID, Username   string
	DailyBudgetUSD float64
}
type Options struct {
	DB     *sql.DB
	Agents []Agent
	Now    func() time.Time
	Logger *slog.Logger
}
type Handler struct {
	db     *sql.DB
	agents []Agent
	now    func() time.Time
	logger *slog.Logger
}

func New(o Options) (*Handler, error) {
	if o.DB == nil || o.Now == nil || len(o.Agents) == 0 || len(o.Agents) > AgentLimit {
		return nil, errors.New("creating dashboard: database, clock and bounded agent selection required")
	}
	seen := map[string]bool{}
	for _, a := range o.Agents {
		if a.ID == "" || len(a.ID) > 128 || seen[a.ID] || !finite(a.DailyBudgetUSD) || a.DailyBudgetUSD <= 0 {
			return nil, errors.New("creating dashboard: invalid public agent configuration")
		}
		seen[a.ID] = true
	}
	return &Handler{db: o.DB, agents: append([]Agent(nil), o.Agents...), now: o.Now, logger: o.Logger}, nil
}

// Page uses a consistent read transaction and a fixed maximum number of rows per
// selected agent. A failed query returns no partial public page or raw error.
func (h *Handler) Page(ctx context.Context) (Page, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	now := h.now().UTC()
	p := Page{GeneratedAt: now.Format(time.RFC3339), Day: now.Format("2006-01-02"), Agents: []AgentView{}}
	tx, err := h.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Page{}, queryError(ctx)
	}
	defer tx.Rollback()
	for _, a := range h.agents {
		v, err := readAgent(ctx, tx, a, p.Day)
		if err != nil {
			return Page{}, queryError(ctx)
		}
		p.Agents = append(p.Agents, v)
	}
	if err = tx.Commit(); err != nil {
		return Page{}, queryError(ctx)
	}
	return p, nil
}
func queryError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(errors.New("reading dashboard: cancelled"), err)
	}
	return errors.New("reading dashboard: state unavailable")
}
func finite(n float64) bool { return n >= 0 && !math.IsNaN(n) && !math.IsInf(n, 0) }
func timestamp(s string) string {
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || v.Year() < 1 || v.Year() > 9999 {
		return ""
	}
	return v.UTC().Format(time.RFC3339)
}
func oneOf(s string, values ...string) string {
	for _, v := range values {
		if s == v {
			return v
		}
	}
	return "unknown"
}

func readAgent(ctx context.Context, tx *sql.Tx, a Agent, day string) (AgentView, error) {
	v := AgentView{ID: a.ID, Status: "not_started", DailyBudgetUSD: a.DailyBudgetUSD, RemainingUSD: a.DailyBudgetUSD}
	var status string
	var next sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT substr(status,1,32),substr(next_wake_at,1,64) FROM agents WHERE id=?`, a.ID).Scan(&status, &next)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	if err == nil {
		v.Status = oneOf(status, "running", "paused", "killed")
		v.NextWake = timestamp(next.String)
	}
	err = tx.QueryRowContext(ctx, `SELECT cost_usd FROM ledger WHERE agent_id=? AND day=?`, a.ID, day).Scan(&v.SpentUSD)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	if !finite(v.SpentUSD) {
		return v, errors.New("invalid usage")
	}
	v.RemainingUSD = math.Max(0, v.DailyBudgetUSD-v.SpentUSD)
	rows, err := tx.QueryContext(ctx, `SELECT id,substr(started_at,1,64),substr(ended_at,1,64),substr(end_reason,1,64),substr(kind,1,32),steps,tokens_in,tokens_out,cost_usd FROM wakes WHERE agent_id=? ORDER BY id DESC LIMIT ?`, a.ID, RowLimit)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var w WakeView
		var end sql.NullString
		if err = rows.Scan(&w.ID, &w.StartedAt, &end, &w.EndReason, &w.Kind, &w.Steps, &w.TokensIn, &w.TokensOut, &w.CostUSD); err != nil {
			rows.Close()
			return v, err
		}
		if w.ID <= 0 || w.Steps < 0 || w.TokensIn < 0 || w.TokensOut < 0 || !finite(w.CostUSD) {
			rows.Close()
			return v, errors.New("invalid wake metadata")
		}
		w.StartedAt = timestamp(w.StartedAt)
		w.EndedAt = timestamp(end.String)
		w.Kind = oneOf(w.Kind, "agent", "metrics")
		if !end.Valid {
			w.EndReason = "in_progress"
		} else {
			w.EndReason = oneOf(w.EndReason, "completed", "interrupted", "step_limit", "budget", "provider_error", "invalid_response", "cancelled", "accounting_error", "persistence_error", "stall_stop", "action_limit", "unknown_outcome", "logged_out", "captcha", "stopped")
		}
		v.Wakes = append(v.Wakes, w)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return v, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT id,kind,substr(posted_at,1,64),CASE WHEN length(thread_url)<=512 THEN thread_url ELSE '' END FROM actions WHERE agent_id=? AND dry_run=0 AND thread_url<>'' ORDER BY id DESC LIMIT ?`, a.ID, RowLimit)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var action ActionView
		var rawURL string
		if err = rows.Scan(&action.ID, &action.Kind, &action.PublishedAt, &rawURL); err != nil {
			rows.Close()
			return v, err
		}
		if action.ID <= 0 {
			rows.Close()
			return v, errors.New("invalid action metadata")
		}
		action.Kind = oneOf(action.Kind, "post", "reply")
		action.PublishedAt = timestamp(action.PublishedAt)
		action.URL = ownURL(rawURL, a.Username)
		v.Actions = append(v.Actions, action)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return v, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT id,substr(status,1,32),n,successes FROM playbook WHERE agent_id=? ORDER BY id DESC LIMIT ?`, a.ID, HypothesisLimit)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var hyp HypothesisView
		if err = rows.Scan(&hyp.ID, &hyp.Status, &hyp.N, &hyp.Successes); err != nil {
			rows.Close()
			return v, err
		}
		if hyp.ID <= 0 || hyp.N < 0 || hyp.Successes < 0 || hyp.Successes > hyp.N {
			rows.Close()
			return v, errors.New("invalid hypothesis metadata")
		}
		hyp.Status = oneOf(hyp.Status, "testing", "established", "rejected")
		v.Playbook = append(v.Playbook, hyp)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return v, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT id,wake_id,substr(tool,1,64),substr(at,1,64),CASE WHEN length(result_json)<=1024 THEN result_json ELSE '' END,blocked_by<>'' FROM tool_calls WHERE agent_id=? ORDER BY id DESC LIMIT ?`, a.ID, RowLimit)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var tool ToolView
		var result string
		var blocked bool
		if err = rows.Scan(&tool.ID, &tool.WakeID, &tool.Name, &tool.At, &result, &blocked); err != nil {
			rows.Close()
			return v, err
		}
		if tool.ID <= 0 || tool.WakeID < 0 {
			rows.Close()
			return v, errors.New("invalid tool metadata")
		}
		tool.Name = oneOf(tool.Name, "read_feed", "search", "read_post", "post", "reply", "read_own_metrics", "fetch_trends", "note_write", "note_search", "playbook_read", "playbook_propose", "notify_owner", "ask_owner", "mark_do_not_contact", "schedule_next_wake")
		tool.At = timestamp(tool.At)
		tool.Outcome, tool.Detail = toolSummary(tool.Name, result, blocked)
		v.Tools = append(v.Tools, tool)
	}
	err = rows.Err()
	rows.Close()
	return v, err
}
