package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/canhta/threadz/internal/admin"
	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/api"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/telegram"
	"github.com/canhta/threadz/internal/threads"
)

// adminService executes operator commands inside the running process. Jobs that
// start work go through the scheduler's Control so they share its serial loop;
// pause, resume and kill take the owner-control path directly, as Telegram does.
type adminService struct {
	templates []config.Template
	runtimes  map[string]*agent.Runtime
	control   *agent.Control
	store     *store.Store
	oauth     *api.Server
	cookies   *cookieImporter
	deps      ctlDeps
	receipts  atomic.Uint64
}

func (a *adminService) template(id string) (config.Template, error) {
	for _, t := range a.templates {
		if t.ID == id {
			return t, nil
		}
	}
	return config.Template{}, fmt.Errorf("unknown agent %q", id)
}

// Handle implements admin.Handler.
func (a *adminService) Handle(ctx context.Context, req admin.Request) (any, error) {
	if req.Command == admin.Status {
		return a.status(ctx, req.Agent)
	}
	if req.Command == admin.Prune {
		return a.job(ctx, func(jobCtx context.Context) (any, error) {
			counts, err := a.store.Prune(jobCtx, a.deps.now())
			if err != nil {
				return nil, err
			}
			out := maintenanceOutcome{Step: admin.Prune, Tables: map[string]int64{}}
			for _, c := range counts {
				out.Tables[c.Table] = c.Deleted
				out.Count += int(c.Deleted)
			}
			return out, nil
		})
	}
	t, err := a.template(req.Agent)
	if err != nil {
		return nil, err
	}
	r := a.runtimes[t.ID]
	switch req.Command {
	case admin.Pause, admin.Resume, admin.Kill:
		now := a.deps.now()
		key := "ctl-" + strconv.FormatInt(now.UnixNano(), 10) + "-" + strconv.FormatUint(a.receipts.Add(1), 10)
		if err = a.store.OwnerControl(ctx, t.ID, req.Command, key, "operator "+req.Command, now); err != nil {
			return nil, err
		}
		state, err := a.store.AgentState(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		return controlOutcome{Agent: t.ID, State: state.Status}, nil
	case admin.Wake:
		phase := req.Phase
		if phase == "" {
			phase = guard.PhaseAgent
		}
		return a.job(ctx, func(jobCtx context.Context) (any, error) {
			res, err := r.WakeNow(jobCtx, agent.WakeRequest{Phase: phase, DryRun: req.DryRun})
			if err != nil {
				return nil, err
			}
			return wakeOutcome{Agent: t.ID, Phase: phase, DryRun: req.DryRun || t.DryRun, WakeID: res.ID, EndReason: res.EndReason, Steps: res.Steps}, nil
		})
	case admin.MetricsRun:
		return a.job(ctx, func(jobCtx context.Context) (any, error) {
			res, err := r.RunMetrics(jobCtx)
			if err != nil {
				return nil, err
			}
			return maintenanceOutcome{Step: admin.MetricsRun, Agent: t.ID, Count: res.ClaimedJobs}, nil
		})
	case admin.TrendsRefresh:
		return a.job(ctx, func(jobCtx context.Context) (any, error) {
			n, err := r.RefreshTrends(jobCtx)
			if err != nil {
				return nil, err
			}
			return maintenanceOutcome{Step: admin.TrendsRefresh, Agent: t.ID, Count: n}, nil
		})
	case admin.MemoryRestore:
		if err := a.store.RestoreMemory(ctx, t.ID, req.ID, a.deps.now()); err != nil {
			return nil, err
		}
		return maintenanceOutcome{Step: admin.MemoryRestore, Agent: t.ID, Count: 1}, nil
	case admin.StrategyRevert:
		version, err := a.store.RevertStrategy(ctx, t.ID, req.ID, a.deps.now())
		if err != nil {
			return nil, err
		}
		return maintenanceOutcome{Step: admin.StrategyRevert, Agent: t.ID, Count: int(version)}, nil
	case admin.OAuthLink:
		if a.oauth == nil {
			return nil, errors.New("the api server is not running; start the service with -api-listen")
		}
		link, err := a.oauth.NewLink(t.ID)
		if err != nil {
			return nil, err
		}
		now := a.deps.now()
		key := "oauth-" + strconv.FormatInt(now.UnixNano(), 10)
		if _, err = a.store.QueueOwner(ctx, t.ID, "notice", "Authorize Threads (one use, 15 minutes): "+link, key, now); err != nil {
			return nil, err
		}
		return maintenanceOutcome{Step: admin.OAuthLink, Agent: t.ID, Count: 1}, nil
	case admin.Cookies:
		if a.cookies == nil {
			return nil, errors.New("cookie import is not available: the agent has no api Threads backend")
		}
		status := a.cookies.Import(ctx, t.ID, req.Cookies)
		if status != threads.CookiesOK {
			return nil, errors.New(telegram.CookieResultText(status))
		}
		return cookiesOutcome{Agent: t.ID, Message: telegram.CookieResultText(status)}, nil
	case admin.Context:
		phase := req.Phase
		if phase == "" {
			phase = guard.PhaseAgent
		}
		messages, err := r.Context(ctx, phase)
		if err != nil {
			return nil, err
		}
		view := contextView{Agent: t.ID, Phase: phase}
		for _, m := range messages {
			view.Messages = append(view.Messages, contextMessage{Role: m.Role, Content: m.Content})
		}
		return view, nil
	}
	return nil, fmt.Errorf("unknown command %q", req.Command)
}

func (a *adminService) job(ctx context.Context, fn func(context.Context) (any, error)) (any, error) {
	out, err := a.control.Do(ctx, fn)
	if errors.Is(err, agent.ErrBusy) {
		return nil, admin.ErrBusy
	}
	return out, err
}

func (a *adminService) status(ctx context.Context, only string) ([]agentStatus, error) {
	now := a.deps.now()
	var out []agentStatus
	for _, t := range a.templates {
		if only != "" && t.ID != only {
			continue
		}
		r := a.runtimes[t.ID]
		state, err := a.store.AgentState(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		s := agentStatus{Agent: t.ID, State: state.Status, DryRun: t.DryRun, DailyCapUSD: t.Limits.MaxUSDPerDay, DreamCapUSD: t.Limits.MaxUSDPerDream, NextWake: state.NextWake}
		if work, ok, err := r.NextWork(ctx, now); err != nil {
			return nil, err
		} else if ok {
			s.NextWake, s.NextKind = work.Due, work.Kind
		}
		phases, err := a.store.LLMUsageByPhaseForDay(ctx, t.ID, t.Limits.Day(now))
		if err != nil {
			return nil, err
		}
		s.SpentTodayUSD = phases["agent"].CostUSD + phases["study"].CostUSD + phases["critic"].CostUSD
		s.DreamSpentUSD = phases["dream"].CostUSD
		if wakes, err := a.store.Wakes(ctx, t.ID, time.Time{}.AddDate(1, 0, 0), 1); err != nil {
			return nil, err
		} else if len(wakes) == 1 {
			w := wakes[0]
			s.LastWake = &wakeSummary{ID: w.ID, Kind: w.Kind, StartedAt: w.StartedAt, EndedAt: w.EndedAt, Steps: w.Steps, EndReason: w.EndReason, CostUSD: w.CostUSD}
		}
		if s.PendingMetricJobs, err = a.store.PendingMetricJobs(ctx, t.ID); err != nil {
			return nil, err
		}
		s.Health = runChecks(ctx, agentChecks(a.deps, t), true)
		out = append(out, s)
	}
	if only != "" && len(out) == 0 {
		return nil, fmt.Errorf("unknown agent %q", only)
	}
	return out, nil
}
