package agent

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/metrics"
	"github.com/canhta/threadz/internal/trends"
)

// ErrBusy refuses an operator job while another one is waiting or running.
var ErrBusy = errors.New("operator job: another job is waiting or running")

type job struct {
	ctx  context.Context
	fn   func(context.Context) (any, error)
	done chan jobResult
}

type jobResult struct {
	value any
	err   error
}

// Control hands one operator job at a time to the scheduler loop, so a manual
// trigger runs between scheduled work and shares its serial execution.
type Control struct {
	jobs chan job
	busy atomic.Bool
}

func NewControl() *Control { return &Control{jobs: make(chan job, 1)} }

// Do queues fn for the scheduler loop and waits for its result. A second call
// while one is queued or running returns ErrBusy at once.
func (c *Control) Do(ctx context.Context, fn func(context.Context) (any, error)) (any, error) {
	if !c.busy.CompareAndSwap(false, true) {
		return nil, ErrBusy
	}
	j := job{ctx: ctx, fn: fn, done: make(chan jobResult, 1)}
	c.jobs <- j
	select {
	case res := <-j.done:
		return res.value, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// drain runs a queued job, if any. A nil Control has none.
func (c *Control) drain(ctx context.Context) {
	if c == nil {
		return
	}
	select {
	case j := <-c.jobs:
		c.run(ctx, j)
	default:
	}
}

func (c *Control) run(ctx context.Context, j job) {
	defer c.busy.Store(false)
	if err := j.ctx.Err(); err != nil {
		j.done <- jobResult{err: err}
		return
	}
	value, err := j.fn(ctx)
	j.done <- jobResult{value: value, err: err}
}

// idle waits like Wait but returns early to run an operator job.
func (s Scheduler) idle(ctx context.Context, d time.Duration) error {
	if s.Control == nil {
		return s.Wait(ctx, d)
	}
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	waited := make(chan error, 1)
	go func() { waited <- s.Wait(waitCtx, d) }()
	select {
	case err := <-waited:
		return err
	case j := <-s.Control.jobs:
		cancel()
		<-waited
		s.Control.run(ctx, j)
		return nil
	}
}

// WakeRequest asks for one wake outside the schedule. Phase is agent or dream.
// DryRun makes every write of this wake a dry-run write.
type WakeRequest struct {
	Phase  string
	DryRun bool
}

// WakeNow runs one wake immediately. The agent must be running and every
// guard, limit and spend cap applies unchanged; only the schedule is skipped.
func (r *Runtime) WakeNow(ctx context.Context, req WakeRequest) (WakeResult, error) {
	run := r
	if req.DryRun {
		copied := *r
		copied.opts.Template.DryRun = true
		run = &copied
	}
	switch req.Phase {
	case guard.PhaseAgent:
		return run.wakeNow(ctx)
	case guard.PhaseDream:
		return run.RunPhase(ctx, req.Phase, "manual-"+r.opts.Now().UTC().Format("20060102T150405.000000000"))
	}
	return WakeResult{}, fmt.Errorf("starting wake: unknown phase %q", req.Phase)
}

func (r *Runtime) wakeNow(ctx context.Context) (WakeResult, error) {
	return r.startWake(ctx, r.opts.Now())
}

// RunMetrics measures the agent's metric targets that are due now.
func (r *Runtime) RunMetrics(ctx context.Context) (metrics.Result, error) {
	return r.metrics.RunDue(ctx)
}

// RefreshTrends replaces the agent's outside-trend snapshot now.
func (r *Runtime) RefreshTrends(ctx context.Context) (int, error) {
	return trends.Refresh(ctx, r.opts.Store, r.opts.Template, r.opts.FetchTrends, r.opts.Now())
}
