// Package agent owns serial wake lifecycle and secret-free context assembly.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/canhta/threadz/internal/stdx"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/metrics"
	"github.com/canhta/threadz/internal/quality"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/tools"
	"github.com/canhta/threadz/internal/trends"
)

type Options struct {
	OwnUsernames []string
	// ThreadsFactory is harness-only; a wake owns and always closes its backend.
	ThreadsFactory threads.Factory
	// Tokens refreshes the Threads API token for the api backend.
	Tokens *threads.Tokens
	// EvidenceDir receives the page snapshots of failed browser steps; empty saves none.
	EvidenceDir string
	Owner       tools.Owner
	Redactor    *config.Redactor
	Store       *store.Store
	Template    config.Template
	Now         func() time.Time
	Wait        func(context.Context, time.Duration) error
	HTTPClient  *http.Client
	Logger      *slog.Logger
	FetchTrends func(context.Context) ([]trends.Trend, error)
	// SearchWeb carries the web_search transport.
	SearchWeb trends.WebSearcher
	// TokenMissing is told when a wake cannot start because the agent has no Threads token.
	TokenMissing func(ctx context.Context, agentID string)
	// ProfileHeld reports that a cookie import holds the agent's browser profile;
	// no wake or session check runs while it does.
	ProfileHeld func(agentID string) bool
}

// ErrProfileHeld refuses a wake while a cookie import holds the browser profile.
var ErrProfileHeld = errors.New("starting wake: a cookie import holds the browser profile")

// sessionCheckEvery is the gap between read-only checks that the browser profile is still logged in.
const sessionCheckEvery = 24 * time.Hour

// ErrNotRunning allows the scheduler to absorb an owner control racing selection.
var ErrNotRunning = errors.New("starting wake: agent is not running")

// reasoningTimeout bounds one writer, critic or ideas request. Those calls run
// at a real reasoning effort, and gpt-6-luna at effort high was measured past
// the 30s default request timeout.
const reasoningTimeout = 2 * time.Minute

type Runtime struct {
	opts     Options
	metrics  *metrics.Processor
	schedule *PhaseSchedule
	// canPublish is true only for a backend that can submit; it alone lifts the guard's live gate.
	canPublish bool
	session    *sessionClock
	failure    *failureClock
	inbox      *inboxClock
}

// failureClock backs an agent off after failed work so one agent's provider
// outage never stops the process or the other agents. It is shared by the
// copies WakeNow makes of a Runtime.
type failureClock struct {
	mu     sync.Mutex
	streak int
	until  time.Time
}

// Backoff bounds for failed work: the first retry waits failureBackoff, each
// further failure doubles it, up to failureBackoffMax.
const (
	failureBackoff    = 15 * time.Minute
	failureBackoffMax = 4 * time.Hour
)

// failed records failed work, schedules the retry and tells the owner once per
// streak. It returns the wait before the agent is offered work again.
func (r *Runtime) failed(ctx context.Context, kind string, err error) time.Duration {
	f := r.failure
	f.mu.Lock()
	f.streak++
	streak := f.streak
	wait := failureBackoff << min(streak-1, 8)
	if wait > failureBackoffMax {
		wait = failureBackoffMax
	}
	now := r.opts.Now()
	f.until = now.Add(wait)
	f.mu.Unlock()
	r.event(ctx, kind, logging.WakeFinished, "error", "work_failed_backoff", 0, 0)
	if streak == 1 {
		text := fmt.Sprintf("A %s run failed (%s); it retries in %s and waits longer after each further failure. The next notice comes after a run succeeds and fails again.", kind, wakeEndReason(err), wait.Round(time.Minute))
		if _, qerr := r.opts.Store.QueueOwner(ctx, r.opts.Template.ID, "notice", text, "notice:work_failed:"+now.UTC().Format(time.RFC3339), now); qerr != nil && r.opts.Logger != nil {
			r.opts.Logger.Error("queueing failed-work notice failed", "agent", r.opts.Template.ID)
		}
	}
	return wait
}

// succeeded ends a failure streak.
func (r *Runtime) succeeded() {
	r.failure.mu.Lock()
	r.failure.streak, r.failure.until = 0, time.Time{}
	r.failure.mu.Unlock()
}

func (r *Runtime) backingOff(now time.Time) bool {
	r.failure.mu.Lock()
	defer r.failure.mu.Unlock()
	return now.Before(r.failure.until)
}

// sessionClock is shared by the copies WakeNow makes of a Runtime.
type sessionClock struct {
	mu  sync.Mutex
	due time.Time
}

type WakeResult struct {
	ID        int64
	EndReason string
	Steps     int
}

func New(opts Options) (*Runtime, error) {
	if opts.Store == nil || opts.Template.ID == "" {
		return nil, errors.New("configuring runtime: store and agent required")
	}
	opts.OwnUsernames = append([]string(nil), opts.OwnUsernames...)
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Wait == nil {
		opts.Wait = stdx.Sleep
	}
	factory, canPublish := threads.NewBackend(opts.Template, opts.Now, opts.Tokens)
	if opts.ThreadsFactory != nil {
		factory, canPublish = opts.ThreadsFactory, true
	}
	if !opts.Template.DryRun && !canPublish {
		return nil, errors.New("configuring runtime: non-dry-run requires a publishing backend")
	}
	opts.ThreadsFactory = factory
	if opts.Template.Limits.MinMinutesBetweenWakes <= 0 || opts.Template.Limits.MinMinutesBetweenWakes > 1440 {
		return nil, errors.New("configuring runtime: wake minimum must be within 1..1440 minutes")
	}
	// ponytail: the check clock lives in memory, so a restart waits a full day
	// before its next check; persist it in the store if restarts become frequent.
	r := &Runtime{opts: opts, canPublish: canPublish, session: &sessionClock{due: opts.Now().Add(sessionCheckEvery)}, failure: &failureClock{}, inbox: &inboxClock{seen: map[string]bool{}}}
	processor, err := metrics.New(metrics.Options{Store: opts.Store, Template: opts.Template, Now: opts.Now, Logger: opts.Logger, ThreadsFactory: opts.ThreadsFactory, Owner: opts.Owner, Redactor: opts.Redactor, FetchTrends: opts.FetchTrends, SearchWeb: opts.SearchWeb})
	if err != nil {
		return nil, err
	}
	schedule, err := NewPhaseSchedule(opts.Template.Schedule)
	if err != nil {
		return nil, err
	}
	r.metrics, r.schedule = processor, schedule
	return r, nil
}
func (r *Runtime) fallback(at time.Time) time.Time {
	interval := time.Hour
	if min := time.Duration(r.opts.Template.Limits.MinMinutesBetweenWakes) * time.Minute; min > interval {
		interval = min
	}
	return at.Add(interval)
}

// appendTranscript is best effort: a lost entry is logged and never stops a wake.
func (r *Runtime) appendTranscript(ctx context.Context, phase string, id int64, kind, body string) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.opts.Store.AppendTranscript(writeCtx, r.opts.Template.ID, id, kind, body, r.opts.Now()); err != nil {
		r.event(writeCtx, phase, logging.TranscriptFailed, "error", "append_failed", id, 0)
	}
}
func (r *Runtime) browserEvent(ctx context.Context, phase, event string, id int64, outcome, reason string, started time.Time) {
	logging.Emit(ctx, r.opts.Logger, slog.LevelInfo, logging.Record{Event: event, AgentID: r.opts.Template.ID, Phase: phase, WakeID: id, Operation: "browser", Outcome: outcome, Reason: reason, DurationMS: r.opts.Now().Sub(started).Milliseconds()})
}
func (r *Runtime) event(ctx context.Context, phase, event, outcome, reason string, id int64, steps int) {
	logging.Emit(ctx, r.opts.Logger, slog.LevelInfo, logging.Record{Event: event, AgentID: r.opts.Template.ID, Phase: phase, WakeID: id, Operation: "wake", Outcome: outcome, Reason: reason, Steps: steps})
}

// Start recovers sessions without replaying messages or action intents. A
// recovered agent waits until its new future schedule before a fresh wake.
func (r *Runtime) Start(ctx context.Context) error {
	if err := r.opts.Store.EnsureAgent(ctx, r.opts.Template.ID); err != nil {
		return err
	}
	now := r.opts.Now()
	before, err := r.opts.Store.AgentState(ctx, r.opts.Template.ID)
	if err != nil {
		return err
	}
	ids, err := r.opts.Store.RecoverWakes(ctx, r.opts.Template.ID, now, r.fallback(now))
	if err != nil {
		return err
	}
	for _, id := range ids {
		r.event(ctx, "", logging.WakeStopped, "stopped", "interrupted", id, 0)
	}
	if len(ids) > 0 {
		after, err := r.opts.Store.AgentState(ctx, r.opts.Template.ID)
		if err != nil {
			return err
		}
		if !before.NextWake.Equal(after.NextWake) {
			r.event(ctx, "", logging.SchedulerScheduled, "success", "recovery_advanced", 0, 0)
		}
	}
	return nil
}

// Wake starts a fresh agent session. Stops are persisted; no caller may retry it.
func (r *Runtime) Wake(ctx context.Context) (WakeResult, error) {
	now := r.opts.Now()
	state, err := r.opts.Store.AgentState(ctx, r.opts.Template.ID)
	if err != nil {
		return WakeResult{}, err
	}
	if state.Status != "running" {
		return WakeResult{}, ErrNotRunning
	}
	if !state.NextWake.IsZero() && state.NextWake.After(now) {
		return WakeResult{}, errors.New("starting wake: not due")
	}
	return r.startWake(ctx, now)
}

func (r *Runtime) held() bool {
	return r.opts.ProfileHeld != nil && r.opts.ProfileHeld(r.opts.Template.ID)
}

func (r *Runtime) startWake(ctx context.Context, now time.Time) (WakeResult, error) {
	if r.held() {
		return WakeResult{}, ErrProfileHeld
	}
	id, err := r.opts.Store.BeginWake(ctx, r.opts.Template.ID, now, r.fallback(now))
	if err != nil {
		if latest, e := r.opts.Store.AgentState(ctx, r.opts.Template.ID); e == nil && latest.Status != "running" {
			return WakeResult{}, ErrNotRunning
		}
		return WakeResult{}, err
	}
	wake := WakeResult{ID: id, EndReason: "runtime_error"}
	r.event(ctx, guard.PhaseAgent, logging.WakeStarted, "started", "due", id, 0)
	r.event(ctx, guard.PhaseAgent, logging.SchedulerScheduled, "success", "fallback", id, 0)
	return r.execute(ctx, wake, guard.PhaseAgent, 0, 0, func(context.Context) ([]llm.Message, error) {
		return r.wakeContext(ctx, guard.PhaseAgent, id)
	})
}

// PhaseDue reports the next dream instant for this agent. A returned
// instant in the past is a missed occurrence and may run once on this start.
func (r *Runtime) PhaseDue(ctx context.Context, now time.Time) (time.Time, string, string, bool, error) {
	type candidate struct {
		due  time.Time
		kind string
		key  string
	}
	var candidates []candidate
	if r.schedule.DreamEnabled() {
		last, ok, err := r.opts.Store.LastPhaseWake(ctx, r.opts.Template.ID, store.WakeDream)
		if err != nil {
			return time.Time{}, "", "", false, err
		}
		if !ok {
			last = time.Time{}
		}
		if due, key, enabled := r.schedule.DreamDue(now, last); enabled {
			candidates = append(candidates, candidate{due, store.WakeDream, key})
		}
	}
	if len(candidates) == 0 {
		return time.Time{}, "", "", false, nil
	}
	best := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.due.Before(best.due) {
			best = candidate
		}
	}
	return best.due, best.kind, best.key, true, nil
}

// RunPhase executes one dream occurrence. It never changes the agent's
// normal next wake; the durable phase key makes a repeat a no-op.
func (r *Runtime) RunPhase(ctx context.Context, kind, phaseKey string) (WakeResult, error) {
	state, err := r.opts.Store.AgentState(ctx, r.opts.Template.ID)
	if err != nil {
		return WakeResult{}, err
	}
	if state.Status != "running" {
		return WakeResult{}, ErrNotRunning
	}
	if r.held() {
		return WakeResult{}, ErrProfileHeld
	}
	id, err := r.opts.Store.BeginPhaseWake(ctx, r.opts.Template.ID, kind, phaseKey, r.opts.Now())
	if errors.Is(err, store.ErrPhaseAlreadyRan) {
		return WakeResult{EndReason: "already_ran"}, nil
	}
	if err != nil {
		return WakeResult{}, err
	}
	wake := WakeResult{ID: id, EndReason: "runtime_error"}
	phase := kind
	r.event(ctx, kind, logging.WakeStarted, "started", kind, id, 0)
	capUSD, maxSteps := r.phaseCap(kind)
	return r.execute(ctx, wake, phase, capUSD, maxSteps, func(context.Context) ([]llm.Message, error) {
		return r.wakeContext(ctx, kind, id)
	})
}

// phaseCap is the spend cap and step limit of a phase; zero means the template's
// daily limits apply.
func (r *Runtime) phaseCap(phase string) (capUSD float64, maxSteps int) {
	if phase == store.WakeDream {
		return r.opts.Template.Limits.MaxUSDPerDream, r.opts.Template.Limits.MaxStepsPerDream
	}
	return 0, 0
}

func (r *Runtime) execute(ctx context.Context, wake WakeResult, phase string, capUSD float64, maxSteps int, build func(context.Context) ([]llm.Message, error)) (result WakeResult, wakeErr error) {
	result = wake
	defer func() {
		finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := r.opts.Store.FinishWake(finalCtx, result.ID, r.opts.Now(), result.Steps, result.EndReason); err != nil {
			wakeErr = errors.Join(wakeErr, err)
		}
		event, outcome := logging.WakeFinished, "success"
		if result.EndReason != "completed" {
			event, outcome = logging.WakeStopped, "stopped"
		}
		r.event(finalCtx, phase, event, outcome, result.EndReason, result.ID, result.Steps)
		end, _ := json.Marshal(map[string]any{"reason": result.EndReason, "steps": result.Steps})
		r.appendTranscript(finalCtx, phase, result.ID, store.TranscriptEnd, string(end))
	}()
	launched := r.opts.Now()
	backend, err := r.opts.ThreadsFactory(ctx, r.opts.Template)
	launchOutcome, launchReason := "success", "launched"
	if err != nil {
		launchOutcome, launchReason = "error", "launch_failed"
	}
	r.browserEvent(ctx, phase, logging.BrowserLaunch, result.ID, launchOutcome, launchReason, launched)
	if errors.Is(err, threads.ErrNoToken) {
		// A missing token ends this wake only; the process keeps running and the
		// next wake retries after the fallback interval.
		result.EndReason = "no_token"
		if r.opts.TokenMissing != nil {
			r.opts.TokenMissing(ctx, r.opts.Template.ID)
		}
		return result, nil
	}
	if err != nil {
		result.EndReason = "browser_error"
		return result, errors.New("opening wake browser: failed")
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		closing := r.opts.Now()
		closeOutcome, closeReason := "success", "closed"
		if closeErr := backend.Close(closeCtx); closeErr != nil {
			// The wake has already decided its own outcome. A cleanup
			// failure is logged and must not rewrite end_reason, fail
			// the scheduler or cancel the independent owner poller.
			closeOutcome, closeReason = "error", "close_failed"
			r.event(closeCtx, phase, logging.WakeCleanupFailed, "error", "close_failed", result.ID, result.Steps)
		}
		r.browserEvent(closeCtx, phase, logging.BrowserClose, result.ID, closeOutcome, closeReason, closing)
	}()
	hidden, err := r.opts.Store.RefusedTools(ctx, r.opts.Template.ID, r.opts.Now())
	if err != nil {
		return result, err
	}
	reviewer := r.Reviewer(result.ID)
	if reviewer.Ideas == nil {
		hidden = append(hidden, "ideas") // no writer block: the wake text must not name the tool
	}
	dispatcher, err := tools.NewDispatcher(tools.Options{Hidden: hidden, Scope: guard.Scope{Store: r.opts.Store, Template: r.opts.Template, OwnUsernames: r.opts.OwnUsernames, WakeID: result.ID, Phase: phase, Now: r.opts.Now, Logger: r.opts.Logger, AllowLive: r.canPublish, Critic: reviewer.Critic, Writer: reviewer.Writer, Ideas: reviewer.Ideas, Sleep: r.opts.Wait}, FetchTrends: r.opts.FetchTrends, SearchWeb: r.opts.SearchWeb, Owner: r.opts.Owner, Threads: backend, Redactor: r.redactor(), EvidenceDir: r.opts.EvidenceDir})
	if err != nil {
		return result, err
	}
	if phase == guard.PhaseAgent {
		// The follower count read lands before the context is built, so the fact
		// sheet is current. A failed read keeps the older recorded count; a stop
		// it raised, such as a logged-out browser, ends the wake at the first tool call.
		dispatcher.ReconcileUnpublished(ctx)
		if _, err = dispatcher.ReadFollowers(ctx, fmt.Sprintf("followers-%d", result.ID)); errors.Is(err, guard.ErrAudit) {
			return result, err
		}
	}
	var inbox []tools.InboxItem
	if phase == guard.PhaseAgent {
		var replies []tools.InboxReply
		replies, inbox = dispatcher.Inbox(ctx)
		r.inbox.markSeen(replies)
	}
	messages, err := build(ctx)
	if err != nil {
		return result, err
	}
	if section, secErr := inboxSection(inbox); secErr != nil {
		return result, secErr
	} else if section != "" {
		messages[1].Content = section + messages[1].Content
	}
	var stopCheck func(called map[string]bool, succeeded map[string]int) string
	if phase == guard.PhaseAgent {
		board, boardErr := r.scoreboard(ctx, r.opts.Now())
		if boardErr != nil {
			return result, boardErr
		}
		stopCheck = board.stopCheck(hidden, func() []string { return dispatcher.OpenTargets(ctx) }, r.opts.Template.Limits.MaxUSDPerDay-board.SpendToday)
	}
	client, err := llm.New(r.opts.Template.LLM, r.opts.Template.Limits, r.opts.Store, dispatcher.ConfigureClient(llm.Options{StopCheck: stopCheck, Logger: r.opts.Logger, Now: r.opts.Now, Sleep: r.opts.Wait, HTTPClient: r.opts.HTTPClient, ReasoningEffort: r.opts.Template.LLM.ReasoningEffort, Phase: phase, CapUSD: capUSD, MaxSteps: maxSteps, Transcript: func(ctx context.Context, kind, body string) { r.appendTranscript(ctx, phase, result.ID, kind, body) }}))
	if err != nil {
		return result, err
	}
	run, err := client.Run(ctx, r.opts.Template.ID, result.ID, messages, dispatcher.Definitions(), dispatcher)
	result.Steps = run.Steps
	result.EndReason = wakeEndReason(err)
	if err != nil && !nonFatalWakeEnd(result.EndReason) {
		return result, fmt.Errorf("running wake: %w", err)
	}
	if phase == guard.PhaseAgent {
		// Code owns the cadence. A failed wake keeps the fallback set at its start.
		next, err := r.nextWake(ctx, r.opts.Now())
		if err == nil {
			err = r.opts.Store.SetNextWake(ctx, r.opts.Template.ID, next)
		}
		outcome, reason := "success", "kpi_cadence"
		if err != nil {
			outcome, reason = "error", "cadence_failed"
		}
		r.event(ctx, phase, logging.SchedulerScheduled, outcome, reason, result.ID, result.Steps)
		if err != nil {
			return result, fmt.Errorf("scheduling next wake: %w", err)
		}
	}
	return result, nil
}

func wakeEndReason(err error) string {
	if err == nil {
		return "completed"
	}
	reason := threads.Reason(err)
	switch reason {
	case threads.ReasonUnknownOutcome, threads.ReasonLoggedOut, threads.ReasonCaptcha:
		return reason
	}
	switch {
	case errors.Is(err, guard.ErrActionLimit):
		return "action_limit"
	case errors.Is(err, guard.ErrAudit):
		return "audit_failure"
	case errors.Is(err, tools.ErrStallStop):
		return "stall_stop"
	case errors.Is(err, llm.ErrStepLimit):
		return "step_limit"
	case errors.Is(err, llm.ErrBudget):
		return "budget"
	}
	switch reason {
	case threads.ReasonCancelled, threads.ReasonTimeout:
		return reason
	}
	return "provider_error"
}

func nonFatalWakeEnd(reason string) bool {
	switch reason {
	case "stall_stop", "step_limit", "budget", "action_limit", "logged_out", "captcha":
		return true
	}
	return false
}

type Scheduler struct {
	Agents []*Runtime
	// Control, when set, lets an operator run a job between scheduled work.
	Control *Control
	Now     func() time.Time
	Wait    func(context.Context, time.Duration) error
}

// Work is the next thing the scheduler would run for one agent.
type Work struct {
	Runtime  *Runtime
	Due      time.Time
	Kind     string
	PhaseKey string
}

// NextWork returns the earliest of the agent's wake, metric job, dream and
// session check. ok is false for an agent that is not running.
func (r *Runtime) NextWork(ctx context.Context, now time.Time) (Work, bool, error) {
	state, err := r.opts.Store.AgentState(ctx, r.opts.Template.ID)
	if err != nil {
		return Work{}, false, err
	}
	if r.held() || r.backingOff(now) {
		return Work{}, false, nil
	}
	if state.Status != "running" {
		return Work{}, false, nil
	}
	due := state.NextWake
	if due.IsZero() {
		due = now
	}
	best := Work{Runtime: r, Due: due, Kind: "agent"}
	metricDue, metricOK, err := r.metrics.NextDue(ctx)
	if err != nil {
		return Work{}, false, err
	}
	if metricOK && metricDue.Before(best.Due) {
		best = Work{Runtime: r, Due: metricDue, Kind: "metrics"}
	}
	phaseDue, phaseKind, phaseKey, phaseOK, err := r.PhaseDue(ctx, now)
	if err != nil {
		return Work{}, false, err
	}
	if phaseOK && phaseDue.Before(best.Due) {
		best = Work{Runtime: r, Due: phaseDue, Kind: phaseKind, PhaseKey: phaseKey}
	}
	if r.opts.Template.Threads.Backend == "api" {
		r.session.mu.Lock()
		sessionDue := r.session.due
		r.session.mu.Unlock()
		if sessionDue.Before(best.Due) {
			best = Work{Runtime: r, Due: sessionDue, Kind: "session"}
		}
	}
	return best, true, nil
}

// CheckSession reads the feed once, read-only, to see whether the browser
// profile is still logged in. A logged-out or captcha page pauses the agent and
// sends the owner notice that says how to send cookies, like a failed wake would. Any other
// result, including a failure to open the browser, only reschedules the check.
func (r *Runtime) CheckSession(ctx context.Context) error {
	r.session.mu.Lock()
	r.session.due = r.opts.Now().Add(sessionCheckEvery)
	r.session.mu.Unlock()
	// At most one notice a day.
	eventID := "session-" + r.opts.Now().UTC().Format("20060102")
	backend, err := r.opts.ThreadsFactory(ctx, r.opts.Template)
	if err != nil {
		r.event(ctx, "", logging.SessionChecked, "error", "open_failed", 0, 0)
		return nil
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = backend.Close(closeCtx)
	}()
	_, err = backend.ReadFeed(ctx)
	reason := threads.Reason(err)
	if reason != threads.ReasonLoggedOut && reason != threads.ReasonCaptcha {
		outcome, why := "success", "logged_in"
		if err != nil {
			outcome, why = "error", "read_failed"
		}
		r.event(ctx, "", logging.SessionChecked, outcome, why, 0, 0)
		return nil
	}
	r.event(ctx, "", logging.SessionChecked, "stopped", reason, 0, 0)
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	pauseErr := r.opts.Store.PauseOwnerAgent(finalCtx, r.opts.Template.ID)
	var noticeErr error
	if owner, ok := r.opts.Owner.(tools.BrowserOwner); ok {
		noticeErr = owner.BrowserStopped(finalCtx, r.opts.Template.ID, reason, eventID)
	} else {
		_, noticeErr = r.opts.Store.QueueOwner(finalCtx, r.opts.Template.ID, "notice", "Browser logged out; agent paused.", "notice:"+reason+":"+eventID, r.opts.Now())
	}
	return errors.Join(pauseErr, noticeErr)
}

// prune applies the retention table once; it runs between wakes, so it never
// overlaps one. All agents share one store.
func (s Scheduler) prune(ctx context.Context, now time.Time) error {
	r := s.Agents[0]
	counts, err := r.opts.Store.Prune(ctx, now)
	if err != nil {
		return err
	}
	for _, c := range counts {
		logging.Emit(ctx, r.opts.Logger, slog.LevelInfo, logging.Record{Event: logging.RetentionPruned, Operation: "prune", Outcome: "success", Reason: c.Table, N: c.Deleted})
	}
	return nil
}

// Run serializes all wakes. Once waits for and executes at most one due wake.
// Owner-paused/killed agents are rechecked on a bounded idle interval.
func (s Scheduler) Run(ctx context.Context, once bool) error {
	if len(s.Agents) == 0 {
		return errors.New("scheduling agents: no agents")
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	if s.Wait == nil {
		s.Wait = stdx.Sleep
	}
	templates := make([]config.Template, 0, len(s.Agents))
	users := []string{}
	for _, r := range s.Agents {
		if r == nil {
			return errors.New("scheduling agents: nil agent")
		}
		templates = append(templates, r.opts.Template)
		if r.opts.Template.Threads.Username != "" {
			users = append(users, r.opts.Template.Threads.Username)
		}
	}
	if err := config.ValidateTemplates(templates); err != nil {
		return fmt.Errorf("scheduling agents: %w", err)
	}
	// Install the complete set before starting any agent. Never discover identities
	// incrementally while another agent may already execute effects.
	for _, r := range s.Agents {
		seen := map[string]bool{}
		complete := []string{}
		for _, user := range append(append([]string(nil), r.opts.OwnUsernames...), users...) {
			normalized, err := store.NormalizeUsername(user)
			if err != nil {
				return errors.New("scheduling agents: invalid own identity")
			}
			if !seen[normalized] {
				complete = append(complete, normalized)
				seen[normalized] = true
			}
		}
		r.opts.OwnUsernames = complete
	}
	for _, r := range s.Agents {
		if r == nil {
			return errors.New("scheduling agents: nil agent")
		}
		if err := r.Start(ctx); err != nil {
			return err
		}
	}
	pruned := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.Control.drain(ctx)
		now := s.Now()
		if today := now.Format(time.DateOnly); today != pruned {
			if err := s.prune(ctx, now); err != nil {
				return err
			}
			pruned = today
		}
		var selected *Work
		for _, r := range s.Agents {
			if err := r.opts.Store.WakeOnWebhook(ctx, r.opts.Template.ID, now, time.Duration(r.opts.Template.Limits.MinMinutesBetweenWakes)*time.Minute); err != nil {
				return err
			}
			if err := r.PollInbox(ctx, now); err != nil {
				return err
			}
			candidate, ok, err := r.NextWork(ctx, now)
			if err != nil {
				return err
			}
			if ok && (selected == nil || candidate.Due.Before(selected.Due)) {
				chosen := candidate
				selected = &chosen
			}
		}
		if selected == nil {
			if once {
				return nil
			}
			if err := s.idle(ctx, time.Minute); err != nil {
				return err
			}
			continue
		}
		next := selected.Due
		if next.After(now) {
			delay := next.Sub(now)
			if delay > time.Minute {
				delay = time.Minute
			}
			if err := s.idle(ctx, delay); err != nil {
				return err
			}
			continue
		}
		switch selected.Kind {
		case "metrics":
			result, err := selected.Runtime.metrics.RunDue(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return err
				}
				selected.Runtime.failed(ctx, "metrics", err)
				if once {
					return err
				}
				continue
			}
			if once {
				return nil
			}
			// Another process can own the agent's audit scope. Avoid a busy
			// loop when the selected maintenance lost that race.
			if result.ClaimedJobs == 0 {
				if err := s.idle(ctx, time.Minute); err != nil {
					return err
				}
			}
			continue
		case "session":
			if err := selected.Runtime.CheckSession(ctx); err != nil && ctx.Err() != nil {
				return err
			}
			if once {
				return nil
			}
			continue
		case "dream":
			if _, err := selected.Runtime.RunPhase(ctx, selected.Kind, selected.PhaseKey); err != nil {
				if errors.Is(err, ErrNotRunning) || errors.Is(err, ErrProfileHeld) || errors.Is(err, store.ErrPhaseAlreadyRan) {
					continue
				}
				if ctx.Err() != nil {
					return err
				}
				selected.Runtime.failed(ctx, selected.Kind, err)
				if once {
					return err
				}
				continue
			}
			selected.Runtime.succeeded()
			if once {
				return nil
			}
			continue
		}
		if _, err := selected.Runtime.Wake(ctx); err != nil {
			if errors.Is(err, ErrNotRunning) || errors.Is(err, ErrProfileHeld) {
				continue
			}
			if ctx.Err() != nil {
				return err
			}
			selected.Runtime.failed(ctx, "agent", err)
			if once {
				return err
			}
			continue
		}
		selected.Runtime.succeeded()
		if once {
			return nil
		}
	}
}

// redactor covers the process-wide secrets plus this agent's own.
func (r *Runtime) redactor() *config.Redactor {
	return config.NewRedactor(r.opts.Redactor, r.opts.Template)
}

// Reviewer is the writer and the critic of one wake: tool-free calls to
// separate models, billed to the critic ledger phase. Each client is built on
// first use so a wake that writes nothing pays for nothing. Writer is nil when
// the template has no writer block; Critic falls back to the agent's llm.
type Reviewer struct {
	Writer func(context.Context, quality.WriterInput) ([]string, error)
	Critic func(context.Context, quality.CriticInput) (quality.CriticVerdict, error)
	// Ideas is the ideas tool's call, on the writer model; nil without a writer block.
	Ideas func(context.Context, []quality.Item) ([]quality.Idea, error)
}

// Reviewer builds the reviewer for a wake.
func (r *Runtime) Reviewer(wakeID int64) Reviewer {
	return NewReviewer(ReviewerOptions{Store: r.opts.Store, Template: r.opts.Template, Now: r.opts.Now, Wait: r.opts.Wait, HTTPClient: r.opts.HTTPClient, Logger: r.opts.Logger, Redactor: r.redactor()}, wakeID)
}

// ReviewerOptions is what a Reviewer needs from a runtime; `ctl draft` builds one without a runtime.
type ReviewerOptions struct {
	Store      *store.Store
	Template   config.Template
	Now        func() time.Time
	Wait       func(context.Context, time.Duration) error
	HTTPClient *http.Client
	Logger     *slog.Logger
	Redactor   *config.Redactor
}

// NewReviewer builds the writer and critic of one wake; spend is recorded on wakeID.
func NewReviewer(o ReviewerOptions, wakeID int64) Reviewer {
	t := o.Template
	call := func(cfg config.LLMConfig) func(context.Context, []llm.Message) (string, error) {
		var client *llm.Client
		return func(ctx context.Context, msgs []llm.Message) (string, error) {
			if client == nil {
				var err error
				client, err = llm.New(cfg, t.Limits, o.Store, llm.Options{Logger: o.Logger, Now: o.Now, Sleep: o.Wait, HTTPClient: o.HTTPClient, ReasoningEffort: cfg.ReasoningEffort, Phase: "critic", MaxSteps: 2, RequestTimeout: reasoningTimeout})
				if err != nil {
					return "", err
				}
			}
			res, err := client.Run(ctx, t.ID, wakeID, msgs, nil, nil)
			return res.Content, err
		}
	}
	redact := o.Redactor.String
	redactAll := func(in []string) []string {
		out := make([]string, len(in))
		for i, s := range in {
			out[i] = redact(s)
		}
		return out
	}
	criticCfg := t.Critic
	if criticCfg.Model == "" {
		criticCfg = t.LLM
	}
	judge, write := call(criticCfg), call(t.Writer)
	writePost := write
	if t.PostWriter.Model != "" {
		writePost = call(t.PostWriter)
	}
	var rv Reviewer
	rv.Critic = func(ctx context.Context, in quality.CriticInput) (quality.CriticVerdict, error) {
		rubric, err := quality.DefaultRubric()
		if err != nil {
			return quality.CriticVerdict{}, err
		}
		in.Parent, in.Texts, in.Recent = redact(in.Parent), redactAll(in.Texts), redactAll(in.Recent)
		out, err := judge(ctx, quality.CriticMessages(rubric, t.Persona, in))
		if err != nil {
			return quality.CriticVerdict{}, err
		}
		return quality.ParseCritic(out)
	}
	if t.Writer.Model != "" {
		rv.Writer = func(ctx context.Context, in quality.WriterInput) ([]string, error) {
			rubric, err := quality.DefaultRubric()
			if err != nil {
				return nil, err
			}
			in.Angle, in.Fact, in.Parent = redact(in.Angle), redact(in.Fact), redact(in.Parent)
			in.Draft, in.Problems, in.Recent = redactAll(in.Draft), redactAll(in.Problems), redactAll(in.Recent)
			draft := write
			if in.Kind == quality.Post {
				draft = writePost
			}
			out, err := draft(ctx, quality.WriterMessages(rubric, t.Persona, in))
			if err != nil {
				return nil, err
			}
			return quality.ParseWriter(out, store.MaxSeriesParts)
		}
		rv.Ideas = func(ctx context.Context, items []quality.Item) ([]quality.Idea, error) {
			// A thread replied to in the last day is not offered again (thread_per_day).
			replied, err := o.Store.RepliedSince(ctx, t.ID, t.DryRun, o.Now().Add(-24*time.Hour))
			if err != nil {
				return nil, err
			}
			items = slices.DeleteFunc(items, func(it quality.Item) bool { return replied[it.URL] })
			for i := range items {
				items[i].Text = redact(items[i].Text)
			}
			recent, err := o.Store.RecentOwnTexts(ctx, store.ActionScope{AgentID: t.ID, DryRun: t.DryRun}, 20)
			if err != nil {
				return nil, err
			}
			out, err := write(ctx, quality.IdeasMessages(t.Persona, items, redactAll(recent)))
			if err != nil {
				return nil, err
			}
			return quality.ParseIdeas(out)
		}
	}
	return rv
}
