package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/threads"
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
	case "read_topic":
		page, err = d.opts.Threads.ReadTopic(ctx, a.Text("url"), a.Text("query"))
	case "search":
		page, err = d.opts.Threads.Search(ctx, a.Text("query"), a.Bool("recent"))
	case "profile":
		return d.opts.Threads.ProfileLookup(ctx, a.Text("username"))
	case "profile_posts":
		page, err = d.opts.Threads.ProfilePosts(ctx, a.Text("username"))
	case "read_post":
		page, err = d.opts.Threads.ReadPost(ctx, a.Text("id"))
	case "read_replies":
		page, err = d.opts.Threads.ReadReplies(ctx, a.Text("id"))
	case "read_own_metrics":
		if a.Int("action_id") > 0 {
			return d.actionMetrics(ctx, a.Int("action_id"))
		}
		own, err := d.opts.Threads.ReadOwnMetrics(ctx)
		if err == nil && own.Followers != nil {
			// A failed write only leaves the older follower figure on the facts sheet.
			_ = d.opts.Store.RecordFollowers(ctx, d.opts.Template.ID, *own.Followers, started)
		}
		return own, err
	}
	if err != nil {
		return nil, err
	}
	if err = d.observeStops(ctx, operation, page); err != nil {
		return nil, err
	}
	return page, nil
}

// checkTopic refuses a read_topic call that names both or neither argument, a
// url the wake has not read, or a fourth read in a normal wake. Its refusals
// reach the model as blocked hints, unlike a browser failure's generic error.
func (d *Dispatcher) checkTopic(a *arguments) error {
	topicURL, query := a.Text("url"), a.Text("query")
	if (topicURL == "") == (query == "") {
		return &guard.BlockedError{Reason: "topic_args"}
	}
	if topicURL != "" {
		seen, ok := d.guard.Trend(topicURL)
		if !ok {
			return &guard.BlockedError{Reason: "topic_args"}
		}
		// Only a Threads topic link opens; a news link would fail in the
		// browser and still use up a read.
		if !threads.IsTopicURL(seen) {
			return &guard.BlockedError{Reason: "topic_args"}
		}
		a.Args = a.Args.WithText("url", seen)
	}
	if d.opts.Phase == guard.PhaseAgent {
		d.topicReads++ // Dispatch already holds d.mu
		if d.topicReads > MaxTopicReadsPerWake {
			return &guard.BlockedError{Reason: "limit_per_wake", Detail: "read_topic"}
		}
	}
	return nil
}

func (d *Dispatcher) observeStops(ctx context.Context, operation string, page threads.Page) error {
	if operation != "read_post" && operation != "read_replies" {
		return nil
	}
	for _, post := range page.Posts {
		if !post.IsReply && operation != "read_replies" {
			continue
		}
		if _, err := d.guard.ObserveStopRequest(ctx, post.Username, post.Text, post.AddressedToAgent); err != nil {
			return err
		}
	}
	return nil
}

// ActionMetrics retains the local action binding in the untrusted result. The
// processor verifies it before committing code-owned outcomes. AuthorReplied is
// nil unless the action is a reply whose target author's answer could be read.
type ActionMetrics struct {
	ActionID int64 `json:"action_id"`
	threads.Metrics
	AuthorReplied *bool `json:"author_replied,omitempty"`
}

func (d *Dispatcher) actionMetrics(ctx context.Context, actionID int64) (any, error) {
	action, ok, err := d.opts.Store.ConfirmedAction(ctx, d.opts.Template.ID, actionID)
	if err != nil {
		return nil, fmt.Errorf("reading own metric action: %w", err)
	}
	if !ok {
		return nil, threads.ErrMetricsUnavailable
	}
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	values, err := d.opts.Threads.ReadActionMetrics(call, action.MediaID)
	if err != nil {
		return nil, err
	}
	counts := []int64{values.Views, values.Likes, values.Replies, values.Reposts}
	for _, p := range []*int64{values.Quotes, values.Shares} {
		if p != nil {
			counts = append(counts, *p)
		}
	}
	for _, count := range counts {
		if count < 0 || count > 1<<53 {
			return nil, threads.ErrMetricsUnavailable
		}
	}
	out := ActionMetrics{ActionID: actionID, Metrics: values}
	if action.Kind == "reply" && action.TargetUsername != "" {
		if page, readErr := d.opts.Threads.ReadReplies(call, action.MediaID); readErr == nil {
			if err = d.observeStops(ctx, "read_replies", page); err != nil {
				return nil, err
			}
			replied := false
			for _, post := range page.Posts {
				replied = replied || strings.EqualFold(strings.TrimPrefix(post.Username, "@"), strings.TrimPrefix(action.TargetUsername, "@"))
			}
			out.AuthorReplied = &replied
		}
	}
	return out, nil
}

func (d *Dispatcher) browserAction(ctx context.Context, operation string, a arguments) (data any, err error) {
	started := d.opts.Now()
	defer func() { err = d.browserResult(ctx, operation, started, err) }()
	var published threads.Published
	if operation == "post" {
		published, err = d.opts.Threads.Post(ctx, a.Text("text"), a.Text("topic_tag"))
	} else {
		// An empty target_id is a feed post, replied to through the browser by permalink.
		target := a.Text("target_id")
		if target == "" {
			target = a.Text("target_url")
		}
		published, err = d.opts.Threads.Reply(ctx, target, a.Text("text"))
		// A failure that is not an unknown outcome happened before submit: the
		// reply never reached Threads, so it must not use up a daily cap.
		if err != nil && !errors.Is(err, threads.ErrUnknownOutcome) && a.ReservedActionID > 0 {
			finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_ = d.opts.Store.MarkActionFailed(finalCtx, d.opts.Template.ID, a.ReservedActionID)
			cancel()
		}
	}
	if err != nil {
		return nil, err
	}
	confirmed := published.URL
	// A browser reply may have no media id: it then has no metric jobs.
	if confirmed == "" || (published.ID == "" && a.Text("target_id") != "") {
		return nil, threads.ErrUnknownOutcome
	}
	if d.redactor.Contains(confirmed) {
		return nil, threads.ErrUnknownOutcome
	}
	// The pre-effect intent remains even if storing the acknowledgement fails.
	// Ending the wake is mandatory: another model turn could duplicate the effect.
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if ok, e := d.opts.Store.ConfirmPublished(finalCtx, d.opts.Template.ID, a.ReservedActionID, confirmed, published.ID); e != nil || !ok {
		return nil, threads.ErrUnknownOutcome
	}
	if err = d.publishParts(ctx, a, published.ID); err != nil {
		return nil, err
	}
	return map[string]any{"dry_run": false, "action_id": a.ReservedActionID, "thread_url": confirmed, "parts": 1 + len(a.TextList("parts"))}, nil
}

// seriesPartSpacing is the pause before each later part of a series.
const seriesPartSpacing = 5 * time.Second

// publishParts publishes the later parts of a series, each as a reply to the
// part before it, and records each one before the next. A later part that
// fails ends the wake: the published parts stay recorded and the series is
// never retried, because a retry would publish part 1 again.
func (d *Dispatcher) publishParts(ctx context.Context, a arguments, firstID string) error {
	previous := firstID
	for i, text := range a.TextList("parts") {
		err := d.opts.Wait(ctx, seriesPartSpacing)
		var published threads.Published
		if err == nil {
			published, err = d.opts.Threads.Reply(ctx, previous, text)
		}
		if err == nil && (published.URL == "" || published.ID == "" || d.redactor.Contains(published.URL)) {
			err = threads.ErrUnknownOutcome
		}
		if err == nil {
			finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			ok, e := d.opts.Store.ConfirmPart(finalCtx, d.opts.Template.ID, a.ReservedActionID, i+2, published.URL, published.ID)
			cancel()
			if e != nil || !ok {
				err = threads.ErrUnknownOutcome
			}
		}
		if err != nil {
			d.stopped = &browserStop{reason: "series_part_failed", cause: err}
			return d.stopped
		}
		previous = published.ID
	}
	return nil
}

func (d *Dispatcher) browserResult(ctx context.Context, operation string, started time.Time, err error) error {
	reason, outcome := "completed", "success"
	if err != nil {
		outcome = "error"
		if reason = threads.Reason(err); reason == "" {
			reason = "operation_failed"
		}
	}
	logging.Emit(context.WithoutCancel(ctx), d.opts.Logger, slog.LevelInfo, logging.Record{Event: logging.BrowserOperationFinished, AgentID: d.opts.Template.ID, Phase: d.opts.Phase, WakeID: d.opts.WakeID, Operation: operation, Tool: operation, Outcome: outcome, Reason: reason, DurationMS: d.opts.Now().Sub(started).Milliseconds()})
	if reason == threads.ReasonLoggedOut || reason == threads.ReasonCaptcha {
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
	if reason == threads.ReasonUnknownOutcome || reason == threads.ReasonCancelled || reason == threads.ReasonTimeout {
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
		return d.redactor.String(v)
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

// ReconcileUnpublished marks live post intents that never recorded a media id
// as failed when the account's own thread listing shows no post with their
// text. A failed listing proves nothing and leaves every intent counted.
// ponytail: the listing holds the newest 100 threads; an older unconfirmed intent is not proven.
func (d *Dispatcher) ReconcileUnpublished(ctx context.Context) {
	pending, err := d.opts.Store.UnpublishedPosts(ctx, d.opts.Template.ID)
	if err != nil || len(pending) == 0 {
		return
	}
	page, err := d.opts.Threads.ReadOwnPosts(ctx)
	if err != nil {
		return
	}
	for _, p := range pending {
		found := false
		for _, own := range page.Posts {
			found = found || strings.TrimSpace(own.Text) == strings.TrimSpace(p.Text)
		}
		if !found {
			_ = d.opts.Store.MarkActionFailed(ctx, d.opts.Template.ID, p.ID)
		}
	}
}

// ReadFollowers is the harness read of the account follower count through the
// guard and the audit, never a model-visible step. The count is recorded as the
// latest own follower count. An unreadable count is nil; only cancellation and
// audit failures are errors.
func (d *Dispatcher) ReadFollowers(ctx context.Context, id string) (*int64, error) {
	output, err := d.DispatchHarness(ctx, llm.ToolCall{ID: id, Type: "function", Function: llm.CallFunction{Name: "read_own_metrics", Arguments: "{}"}})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, guard.ErrAudit) {
			return nil, fmt.Errorf("reading followers: %w", err)
		}
		return nil, nil
	}
	var envelope struct {
		Data threads.Metrics `json:"data"`
	}
	if json.Unmarshal([]byte(output), &envelope) != nil {
		return nil, nil
	}
	return envelope.Data.Followers, nil
}
