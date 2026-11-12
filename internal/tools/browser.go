package tools

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/threads"
)

// BrowserOwner is optional; disabled owner delivery still retains a local notice.
type BrowserOwner interface {
	BrowserStopped(context.Context, string, string, string) error
}

type browserStop struct {
	reason string
	cause  error
}

func (e *browserStop) Error() string           { return "browser stopped: " + e.reason }
func (e *browserStop) Unwrap() error           { return e.cause }
func (e *browserStop) TerminalToolError() bool { return true }

func (d *Dispatcher) browserRead(ctx context.Context, operation string, a arguments) (data any, err error) {
	started := d.opts.Now()
	defer func() { err = d.browserResult(ctx, operation, started, err) }()
	var page threads.Page
	switch operation {
	case "read_feed":
		page, err = d.opts.Threads.ReadFeed(ctx)
	case "search":
		page, err = d.opts.Threads.Search(ctx, a.Query)
	case "read_post":
		page, err = d.opts.Threads.ReadPost(ctx, a.URL)
	case "read_own_metrics":
		if a.ActionID > 0 {
			return d.actionMetrics(ctx, a.ActionID)
		}
		return d.opts.Threads.ReadOwnMetrics(ctx)
	}
	if err != nil {
		return nil, err
	}
	if operation == "read_post" {
		for _, post := range page.Posts {
			if post.IsReply {
				if _, err = d.guard.ObserveStopRequest(ctx, post.Username, post.Text, post.AddressedToAgent); err != nil {
					return nil, err
				}
			}
		}
	}
	return page, nil
}

// ActionMetrics retains the local action binding in the untrusted result. The
// processor verifies it before committing code-owned outcomes.
type ActionMetrics struct {
	ActionID int64 `json:"action_id"`
	threads.Metrics
}

func (d *Dispatcher) actionMetrics(ctx context.Context, actionID int64) (any, error) {
	var threadURL string
	err := d.opts.Store.DB().QueryRowContext(ctx, `SELECT thread_url FROM actions WHERE id=? AND agent_id=? AND dry_run=0 AND kind IN ('post','reply') AND thread_url<>''`, actionID, d.opts.Template.ID).Scan(&threadURL)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && strings.TrimSpace(threadURL) == "") {
		return nil, threads.ErrMetricsUnavailable
	}
	if err != nil {
		return nil, fmt.Errorf("reading own metric action: %w", err)
	}
	reader, ok := d.opts.Threads.(threads.ActionMetricsReader)
	if !ok {
		return nil, threads.ErrMetricsUnavailable
	}
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	values, err := reader.ReadActionMetrics(call, threadURL)
	if err != nil {
		return nil, err
	}
	for _, count := range []int64{values.Views, values.Likes, values.Replies, values.Reposts} {
		if count < 0 || count > 1<<53 {
			return nil, threads.ErrMetricsUnavailable
		}
	}
	return ActionMetrics{ActionID: actionID, Metrics: values}, nil
}

func (d *Dispatcher) browserAction(ctx context.Context, operation string, a arguments) (data any, err error) {
	started := d.opts.Now()
	defer func() { err = d.browserResult(ctx, operation, started, err) }()
	var confirmed string
	if operation == "post" {
		confirmed, err = d.opts.Threads.Post(ctx, a.Text)
	} else {
		confirmed, err = d.opts.Threads.Reply(ctx, a.TargetURL, a.Text)
	}
	if err != nil {
		return nil, err
	}
	if confirmed == "" {
		return nil, threads.ErrUnknownOutcome
	}
	for _, secret := range d.secrets {
		if strings.Contains(confirmed, secret) {
			return nil, threads.ErrUnknownOutcome
		}
	}
	// The pre-effect intent remains even if storing the acknowledgement fails.
	// Ending the wake is mandatory: another model turn could duplicate the effect.
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	row, e := d.opts.Store.DB().ExecContext(finalCtx, `UPDATE actions SET thread_url=? WHERE id=? AND agent_id=? AND dry_run=0 AND thread_url=''`, confirmed, a.ReservedActionID, d.opts.Template.ID)
	if e != nil {
		return nil, threads.ErrUnknownOutcome
	}
	n, e := row.RowsAffected()
	if e != nil || n != 1 {
		return nil, threads.ErrUnknownOutcome
	}
	return map[string]any{"dry_run": false, "action_id": a.ReservedActionID, "thread_url": confirmed}, nil
}

func (d *Dispatcher) browserResult(ctx context.Context, operation string, started time.Time, err error) error {
	reason, outcome := "completed", "success"
	if err != nil {
		outcome, reason = "error", "operation_failed"
		switch {
		case errors.Is(err, threads.ErrLoggedOut):
			reason = "logged_out"
		case errors.Is(err, threads.ErrCaptcha):
			reason = "captcha"
		case errors.Is(err, threads.ErrUnknownOutcome):
			reason = "unknown_outcome"
		case errors.Is(err, threads.ErrUnknownPage):
			reason = "unknown_page"
		case errors.Is(err, threads.ErrMalformedPage):
			reason = "malformed_page"
		case errors.Is(err, threads.ErrMetricsUnavailable):
			reason = "metrics_unavailable"
		case errors.Is(err, threads.ErrLiveWriteUnqualified):
			reason = "live_write_unqualified"
		case errors.Is(err, context.Canceled):
			reason = "cancelled"
		case errors.Is(err, context.DeadlineExceeded):
			reason = "timeout"
		}
	}
	logging.Emit(context.WithoutCancel(ctx), d.opts.Logger, slog.LevelInfo, logging.Record{Event: logging.BrowserOperationFinished, AgentID: d.opts.Template.ID, WakeID: d.opts.WakeID, Operation: operation, Tool: operation, Outcome: outcome, Reason: reason, DurationMS: d.opts.Now().Sub(started).Milliseconds()})
	if reason == "logged_out" || reason == "captcha" {
		finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		pauseErr := d.opts.Store.PauseOwnerAgent(finalCtx, d.opts.Template.ID)
		event := fmt.Sprintf("wake-%d", d.opts.WakeID)
		var noticeErr error
		owner, ok := d.opts.Owner.(BrowserOwner)
		if ok {
			noticeErr = owner.BrowserStopped(finalCtx, d.opts.Template.ID, reason, event)
		}
		if !ok || noticeErr != nil {
			text := "Browser logged out; agent paused."
			if reason == "captcha" {
				text = "Browser captcha detected; agent paused."
			}
			_, pendingErr := d.opts.Store.QueueOwner(finalCtx, d.opts.Template.ID, "notice", text, "notice:"+reason+":"+event, d.opts.Now())
			noticeErr = errors.Join(noticeErr, pendingErr)
		}
		d.stopped = &browserStop{reason: reason, cause: errors.Join(err, pauseErr, noticeErr)}
		return d.stopped
	}
	if reason == "unknown_outcome" || reason == "cancelled" || reason == "timeout" {
		d.stopped = &browserStop{reason: reason, cause: err}
		return d.stopped
	}
	if err != nil {
		return &safeBrowserError{operation: operation, reason: reason, cause: err}
	}
	return nil
}

type safeBrowserError struct {
	operation, reason string
	cause             error
}

func (e *safeBrowserError) Error() string { return "browser " + e.operation + ": " + e.reason }
func (e *safeBrowserError) Unwrap() error { return e.cause }

func (d *Dispatcher) redact(data any) any {
	switch v := data.(type) {
	case string:
		for _, secret := range d.secrets {
			v = strings.ReplaceAll(v, secret, "[REDACTED]")
		}
		return v
	case []any:
		for i := range v {
			v[i] = d.redact(v[i])
		}
		return v
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, value := range v {
			out[d.redact(key).(string)] = d.redact(value)
		}
		return out
	default:
		return data
	}
}
