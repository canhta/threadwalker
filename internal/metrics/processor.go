// Package metrics owns fixed-window remeasurement of confirmed own actions.
// It never calls a model, submits content or changes the agent's chosen schedule.
package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/guard"
	"github.com/canhta/threadwalker/internal/llm"
	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/playbook"
	"github.com/canhta/threadwalker/internal/store"
	"github.com/canhta/threadwalker/internal/threads"
	"github.com/canhta/threadwalker/internal/tools"
)

const (
	DefaultLeaseDuration = 5 * time.Minute
	DefaultRetryDelay    = 5 * time.Minute
	DefaultBatchSize     = 10
)

type Options struct {
	Store          *store.Store
	Template       config.Template
	Now            func() time.Time
	Logger         *slog.Logger
	ThreadsFactory func(context.Context, config.Template) (threads.Backend, error)
	Owner          tools.Owner
	Secrets        []config.Secret
	LeaseDuration  time.Duration
	RetryDelay     time.Duration
	BatchSize      int
}
type Result struct {
	ClaimedJobs  int `json:"claimed_jobs"`
	Samples      int `json:"samples"`
	DeferredJobs int `json:"deferred_jobs"`
}
type Processor struct {
	mu   sync.Mutex
	opts Options
}

func New(opts Options) (*Processor, error) {
	if opts.Store == nil || opts.Template.ID == "" || opts.Template.Limits.MinMinutesBetweenWakes < 1 || opts.Template.Limits.MinMinutesBetweenWakes > 1440 {
		return nil, errors.New("configuring metrics: store, agent and valid wake interval required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.LeaseDuration == 0 {
		opts.LeaseDuration = DefaultLeaseDuration
	}
	if opts.RetryDelay == 0 {
		opts.RetryDelay = DefaultRetryDelay
	}
	if opts.BatchSize == 0 {
		opts.BatchSize = DefaultBatchSize
	}
	if opts.LeaseDuration < 0 || opts.RetryDelay < 0 || opts.BatchSize < 1 || opts.BatchSize > 1000 {
		return nil, errors.New("configuring metrics: positive lease, retry and bounded batch required")
	}
	if opts.ThreadsFactory == nil {
		switch opts.Template.Threads.Backend {
		case "browser":
			opts.ThreadsFactory = func(ctx context.Context, tmpl config.Template) (threads.Backend, error) {
				b, err := threads.New(ctx, threads.Options{AgentID: tmpl.ID, BaseURL: tmpl.Threads.BaseURL, ProfileDir: tmpl.Threads.ProfileDir, ExecutablePath: tmpl.Threads.ExecutablePath, Timeout: 15 * time.Second, StartupTimeout: 30 * time.Second, Logger: opts.Logger, Now: opts.Now, NoSandbox: tmpl.Threads.NoSandbox})
				if b == nil {
					return nil, err
				}
				return b, err
			}
		case "agent_browser":
			opts.ThreadsFactory = func(ctx context.Context, tmpl config.Template) (threads.Backend, error) {
				b, err := threads.NewAgentBrowser(ctx, threads.AgentBrowserOptions{AgentID: tmpl.ID, BaseURL: tmpl.Threads.BaseURL, ProfileDir: tmpl.Threads.ProfileDir, ExecutablePath: tmpl.Threads.ExecutablePath, AgentBrowserPath: tmpl.Threads.AgentBrowserPath, PageProfile: tmpl.Threads.PageProfile, Timeout: 15 * time.Second, StartupTimeout: 30 * time.Second, Logger: opts.Logger, Now: opts.Now, NoSandbox: tmpl.Threads.NoSandbox})
				if b == nil {
					return nil, err
				}
				return b, err
			}
		}
	}
	return &Processor{opts: opts}, nil
}

// NextDue reconciles confirmed actions before returning the next eligible target
// or retry/lease expiry. Paused and killed agents have no eligible due time.
func (p *Processor) NextDue(ctx context.Context) (time.Time, bool, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, false, err
	}
	state, err := p.opts.Store.AgentState(ctx, p.opts.Template.ID)
	if err != nil {
		return time.Time{}, false, metricError("reading metric agent", err)
	}
	if state.Status != "running" {
		return time.Time{}, false, nil
	}
	count, err := p.opts.Store.SeedMetricJobs(ctx, p.opts.Template.ID)
	if err != nil {
		return time.Time{}, false, metricError("seeding metric targets", err)
	}
	if count > 0 {
		p.emit(ctx, logging.Record{Event: logging.MetricsScheduled, Outcome: "success", Reason: "targets_seeded", Jobs: count})
	}
	at, ok, err := p.opts.Store.NextMetricDue(ctx, p.opts.Template.ID)
	return at, ok, metricError("reading next metric target", err)
}

// RunDue leases at most BatchSize action groups. Each group has one guarded
// current read, one actual-timestamp sample and all its ready targets completed
// atomically. Missing/failed reads are deferred without manufacturing zeros.
func (p *Processor) RunDue(ctx context.Context) (result Result, runErr error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := playbook.Process(ctx, p.opts.Store, p.opts.Template.ID, p.opts.Now(), p.opts.Logger); err != nil {
		return result, err
	}
	due, ok, err := p.NextDue(ctx)
	if err != nil {
		return result, err
	}
	if !ok || due.After(p.opts.Now()) {
		return result, nil
	}
	available, err := p.available(ctx)
	if err != nil {
		return result, metricError("checking metric wake", err)
	}
	if !available {
		return result, nil
	}
	wakeID, err := p.opts.Store.BeginMetricWake(ctx, p.opts.Template.ID, p.opts.Now())
	if err != nil {
		if ready, e := p.available(ctx); e == nil && !ready {
			return result, nil
		}
		return result, metricError("starting metric wake", err)
	}
	endReason := "completed"
	defer func() {
		finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if runErr != nil {
			endReason = "measurement_failed"
		}
		if err := p.opts.Store.FinishWake(finalCtx, wakeID, p.opts.Now(), 0, endReason); err != nil {
			runErr = errors.Join(runErr, metricError("finishing metric wake", err))
		}
		outcome := "success"
		if runErr != nil {
			outcome = "error"
		}
		p.emit(finalCtx, logging.Record{Event: logging.MetricsFinished, WakeID: wakeID, Outcome: outcome, Reason: endReason, Jobs: result.ClaimedJobs, Samples: result.Samples})
	}()
	jobs, err := p.opts.Store.ClaimMetricJobs(ctx, p.opts.Template.ID, p.opts.Now(), p.opts.LeaseDuration, p.opts.BatchSize)
	if err != nil {
		return result, metricError("claiming metric targets", err)
	}
	result.ClaimedJobs = len(jobs)
	if len(jobs) == 0 {
		return result, nil
	}
	groups := groupJobs(jobs)
	// Every early exit releases unprocessed claims under a bounded cleanup context.
	pending := groups
	defer func() {
		if len(pending) == 0 {
			return
		}
		finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		reason := "measurement_failed"
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			reason = "cancelled"
		}
		for _, group := range pending {
			if err := p.deferJobs(finalCtx, wakeID, group, reason); err != nil {
				runErr = errors.Join(runErr, err)
			} else {
				result.DeferredJobs += len(group)
			}
		}
	}()
	var backend threads.Backend
	if p.opts.ThreadsFactory != nil {
		// The source may bind its lifetime to this context. Keep it alive through
		// cleanup; its own startup and each guarded read have tighter timeouts.
		openCtx, cancel := context.WithTimeout(ctx, p.opts.LeaseDuration)
		defer cancel()
		backend, err = p.opts.ThreadsFactory(openCtx, p.opts.Template)
	}
	if backend != nil {
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if e := backend.Close(closeCtx); e != nil {
				runErr = errors.Join(runErr, metricError("closing metric source", safeContextError(e)))
			}
		}()
	}
	if err != nil || backend == nil {
		if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return result, metricError("opening metric source", safeContextError(err))
		}
		for _, group := range groups {
			if e := p.deferJobs(ctx, wakeID, group, "source_unavailable"); e != nil {
				return result, e
			}
			result.DeferredJobs += len(group)
			pending = pending[1:]
		}
		endReason = "source_unavailable"
		return result, nil
	}
	g, err := guard.New(guard.Options{Store: p.opts.Store, Template: p.opts.Template, WakeID: wakeID, Now: p.opts.Now, Logger: p.opts.Logger, AllowLive: true})
	if err != nil {
		return result, metricError("configuring metric guard", err)
	}
	dispatcher, err := tools.NewDispatcher(g, tools.Options{Store: p.opts.Store, Template: p.opts.Template, WakeID: wakeID, Now: p.opts.Now, Logger: p.opts.Logger, Threads: backend, Owner: p.opts.Owner, AllowLive: true, Secrets: p.opts.Secrets})
	if err != nil {
		return result, metricError("configuring metric tools", err)
	}
	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		first := group[0]
		call := llm.ToolCall{ID: fmt.Sprintf("metric-%d-%d", wakeID, first.ActionID), Type: "function", Function: llm.CallFunction{Name: "read_own_metrics", Arguments: fmt.Sprintf(`{"action_id":%d}`, first.ActionID)}}
		output, readErr := dispatcher.Dispatch(ctx, call)
		if readErr != nil {
			if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) || errors.Is(readErr, guard.ErrAudit) {
				return result, metricError("reading guarded metric", readErr)
			}
			reason := "read_failed"
			if errors.Is(readErr, threads.ErrMetricsUnavailable) {
				reason = "metrics_unavailable"
			}
			if errors.Is(readErr, threads.ErrLoggedOut) || errors.Is(readErr, threads.ErrCaptcha) {
				reason = "auth_required"
			}
			if e := p.deferJobs(ctx, wakeID, group, reason); e != nil {
				return result, e
			}
			result.DeferredJobs += len(group)
			pending = pending[1:]
			endReason = reason
			continue
		}
		var envelope struct {
			Untrusted bool                `json:"untrusted"`
			Source    string              `json:"source"`
			Data      tools.ActionMetrics `json:"data"`
		}
		if json.Unmarshal([]byte(output), &envelope) != nil || !envelope.Untrusted || envelope.Source != "read_own_metrics" || envelope.Data.ActionID != first.ActionID {
			if e := p.deferJobs(ctx, wakeID, group, "binding_mismatch"); e != nil {
				return result, e
			}
			result.DeferredJobs += len(group)
			pending = pending[1:]
			endReason = "binding_mismatch"
			continue
		}
		at := p.opts.Now()
		values := envelope.Data.Metrics
		committed, e := p.opts.Store.CompleteMetricJobs(ctx, group, at, store.MetricValues{Views: values.Views, Likes: values.Likes, Replies: values.Replies, Reposts: values.Reposts})
		if e != nil {
			// A stale/expired fence cannot write or release a newer claim. Leave it
			// recoverable at lease expiry; other DB errors get a future retry if possible.
			if errors.Is(e, store.ErrMetricClaim) {
				pending = pending[1:]
				return result, metricError("committing metric sample", e)
			}
			if deferErr := p.deferJobs(context.WithoutCancel(ctx), wakeID, group, "persistence_failed"); deferErr != nil {
				return result, errors.Join(metricError("committing metric sample", e), deferErr)
			}
			result.DeferredJobs += len(group)
			pending = pending[1:]
			return result, metricError("committing metric sample", e)
		}
		pending = pending[1:]
		if committed {
			result.Samples++
			earliest := group[0].DueAt
			for _, job := range group {
				if job.DueAt.Before(earliest) {
					earliest = job.DueAt
				}
			}
			p.emit(ctx, logging.Record{Event: logging.MetricsMeasured, WakeID: wakeID, ActionID: first.ActionID, TargetAt: earliest, MeasuredAt: at, LatenessMS: max(0, at.Sub(earliest).Milliseconds()), Outcome: "success", Reason: "sample_committed", Jobs: len(group), Samples: 1})
			for _, job := range group {
				p.emit(ctx, logging.Record{Event: logging.MetricsTargetCompleted, WakeID: wakeID, ActionID: job.ActionID, WindowHours: job.WindowHours, TargetAt: job.DueAt, MeasuredAt: at, LatenessMS: max(0, at.Sub(job.DueAt).Milliseconds()), Outcome: "success", Reason: "target_completed", Jobs: 1})
			}
		}
	}
	if err := playbook.Process(ctx, p.opts.Store, p.opts.Template.ID, p.opts.Now(), p.opts.Logger); err != nil {
		return result, err
	}
	return result, nil
}

func (p *Processor) available(ctx context.Context) (bool, error) {
	var ready bool
	err := p.opts.Store.DB().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=? AND status='running') AND NOT EXISTS(SELECT 1 FROM wakes WHERE agent_id=? AND ended_at IS NULL)`, p.opts.Template.ID, p.opts.Template.ID).Scan(&ready)
	return ready, err
}
func groupJobs(jobs []store.MetricJob) [][]store.MetricJob {
	groups := [][]store.MetricJob{}
	positions := map[int64]int{}
	for _, job := range jobs {
		i, ok := positions[job.ActionID]
		if !ok {
			i = len(groups)
			positions[job.ActionID] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], job)
	}
	return groups
}
func (p *Processor) deferJobs(ctx context.Context, wakeID int64, jobs []store.MetricJob, reason string) error {
	finalCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	now := p.opts.Now()
	if err := p.opts.Store.DeferMetricJobs(finalCtx, jobs, now, now.Add(p.opts.RetryDelay), reason); err != nil {
		return metricError("deferring metric targets", err)
	}
	for _, job := range jobs {
		p.emit(finalCtx, logging.Record{Event: logging.MetricsDeferred, WakeID: wakeID, ActionID: job.ActionID, WindowHours: job.WindowHours, TargetAt: job.DueAt, Outcome: "deferred", Reason: reason, Jobs: len(jobs)})
	}
	return nil
}
func (p *Processor) emit(ctx context.Context, r logging.Record) {
	r.AgentID = p.opts.Template.ID
	r.Operation = "metrics"
	logging.Emit(ctx, p.opts.Logger, slog.LevelInfo, r)
}

type operationError struct {
	operation string
	cause     error
}

func (e *operationError) Error() string { return e.operation + ": failed" }
func (e *operationError) Unwrap() error { return e.cause }
func metricError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return &operationError{operation, err}
}
func safeContextError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errors.New("source_failed")
}
