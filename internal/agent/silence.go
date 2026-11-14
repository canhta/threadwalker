package agent

import (
	"context"
	"fmt"
	"time"
)

// silenceSlack is how long past kpi.max_hours_without_publish an agent may go
// without a publish before the owner is told: the wake cadence needs that long
// to recover on its own.
const silenceSlack = 3 * time.Hour

// WarnIfSilent queues one owner notice per local day when a running live agent, in
// waking hours, has gone longer than kpi.max_hours_without_publish plus
// silenceSlack without a publish. An agent that never published has no clock
// to measure and is skipped.
func (r *Runtime) WarnIfSilent(ctx context.Context, now time.Time) error {
	t := r.opts.Template
	limit := time.Duration(t.KPI.MaxHoursWithoutPublish) * time.Hour
	if limit <= 0 || t.DryRun || r.schedule.AfterQuiet(now) != now {
		return nil
	}
	state, err := r.opts.Store.AgentState(ctx, t.ID)
	if err != nil || state.Status != "running" {
		return err
	}
	last, err := r.opts.Store.LastPublish(ctx, t.ID, t.DryRun, "")
	if err != nil || last.IsZero() || now.Sub(last) <= limit+silenceSlack {
		return err
	}
	text := fmt.Sprintf("The agent has published nothing for %d hours, longer than the %d hours its targets allow. It is running; send /status to see its next run and how its latest runs ended.", int(now.Sub(last).Hours()), t.KPI.MaxHoursWithoutPublish)
	_, err = r.opts.Store.QueueOwner(ctx, t.ID, "notice", text, "notice:silent_agent:"+t.Limits.Day(now), now)
	return err
}
