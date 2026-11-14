package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/store"
)

// scoreboard is the agent's numbers against its KPI targets, computed by code
// from the store. Behind decides the stop rule and the wake cadence.
type scoreboard struct {
	store.KPIStats
	KPI config.KPIConfig
	Now time.Time
}

func (r *Runtime) scoreboard(ctx context.Context, now time.Time) (scoreboard, error) {
	t := r.opts.Template
	dayStart, _, _ := t.Limits.DayBounds(now)
	stats, err := r.opts.Store.KPIStats(ctx, t.ID, t.DryRun, now, dayStart)
	b := scoreboard{KPIStats: stats, KPI: t.KPI, Now: now}
	return b, err
}

// hoursSincePublish is -1 when nothing was ever published.
func (s scoreboard) hoursSincePublish() int {
	if s.LastPublish.IsZero() {
		return -1
	}
	return int(s.Now.Sub(s.LastPublish).Hours())
}

// Behind: engagements under target, followers gained under the weekly target,
// or no publish for longer than the maximum. Followers is the gain over the
// last 7 days, compared with the full weekly target and not pro-rated: the
// window is rolling, so there is no partial week to scale by. With under a week
// of readings (or none) it reads behind. A template without a kpi block is
// never behind.
func (s scoreboard) Behind() bool {
	if !s.KPI.Enabled() {
		return false
	}
	h := s.hoursSincePublish()
	return s.Engagements < int64(s.KPI.EngagementsPerDay) || s.Followers < int64(s.KPI.FollowersPerWeek) || h < 0 || h >= s.KPI.MaxHoursWithoutPublish
}

// gap is the one blunt line that ends the numbers.
func (s scoreboard) gap() string {
	h := s.hoursSincePublish()
	numbers := fmt.Sprintf("%d of %d engagements in 24h, %d of %d followers gained in 7 days", s.Engagements, s.KPI.EngagementsPerDay, s.Followers, s.KPI.FollowersPerWeek)
	if !s.Behind() {
		return fmt.Sprintf("on target: %s; last published %dh ago", numbers, h)
	}
	line := fmt.Sprintf("behind: %s, after $%.2f and %d wakes today", numbers, s.SpendToday, s.WakesToday)
	switch {
	case h < 0:
		line += "; nothing published yet"
	case h >= s.KPI.MaxHoursWithoutPublish:
		line += fmt.Sprintf("; nothing published for %dh", h)
	}
	return line
}

// text is the scoreboard block at the top of an agent wake's user message.
func (s scoreboard) text() string {
	var b strings.Builder
	b.WriteString("# Scoreboard (computed by code)\n")
	fmt.Fprintf(&b, "- Spend: today $%.2f (%d tokens), last 7 days $%.2f (%d tokens). Wakes today: %d.\n", s.SpendToday, s.TokensToday, s.Spend7d, s.Tokens7d, s.WakesToday)
	if h := s.hoursSincePublish(); h < 0 {
		b.WriteString("- Last published action: none yet.\n")
	} else {
		fmt.Fprintf(&b, "- Last published action: %dh ago (most you may go: %dh).\n", h, s.KPI.MaxHoursWithoutPublish)
	}
	fmt.Fprintf(&b, "- Engagements (likes, replies, reposts, quotes on your actions) in 24h: %d of %d. Followers gained in 7 days: %d of %d.\n", s.Engagements, s.KPI.EngagementsPerDay, s.Followers, s.KPI.FollowersPerWeek)
	if s.Engagements > 0 {
		fmt.Fprintf(&b, "- Cost per engagement: $%.3f (spend today over engagements in 24h).\n", s.SpendToday/float64(s.Engagements))
	} else {
		b.WriteString("- Cost per engagement: none yet, no engagements.\n")
	}
	b.WriteString(s.gap() + "\n")
	b.WriteString("Pressure means: try more and better ideas, never lower quality. The critic still decides and the caps still hold.")
	if s.Behind() {
		b.WriteString(" Behind is not a reason to reply: call ideas (when you have it) and publish only what is worth publishing.")
	}
	return b.String() + "\n\n"
}

// stopCheck is the one extra turn a behind agent gets when it stops early:
// it lists the posts it read that still score well and it has not answered,
// and what a wake owes (ideas). An agent on target is never
// asked. The turn is sent once per wake (llm.MaxStopNudges) and imposes no
// reply count. Empty when the stop is allowed. hidden lists tools this wake
// does not offer.
func (s scoreboard) stopCheck(hidden []string, open func() []string, usdLeft float64) func(called map[string]bool, succeeded map[string]int) string {
	return func(called map[string]bool, succeeded map[string]int) string {
		if !s.Behind() {
			return ""
		}
		var todo []string
		if !called["ideas"] && !slices.Contains(hidden, "ideas") {
			todo = append(todo, "call ideas")
		}
		if refs := open(); len(refs) > 0 && !slices.Contains(hidden, "reply") && usdLeft >= minUSDToContinue {
			shown := refs[:min(len(refs), guard.MaxCandidates)]
			todo = append(todo, fmt.Sprintf("%d posts you read still score well and you have not answered them (%s); reply only to one you have a real angle for", len(refs), strings.Join(shown, ", ")))
		}
		if len(todo) == 0 {
			return ""
		}
		return "You are " + s.gap() + ". Before you stop: " + strings.Join(todo, ", then ") + "."
	}
}

// minUSDToContinue is the spend left below which a wake is not sent back to
// answer more posts; one reply with its writer and critic costs about this.
const minUSDToContinue = 0.05

// nextWake is when code wakes the agent again: the template minimum when
// behind, the on-target interval when not, stretched by budget pacing (see
// pacedInterval), and the end of the capped day when the day is capped (see
// capReset).
func (r *Runtime) nextWake(ctx context.Context, now time.Time) (time.Time, error) {
	t := r.opts.Template
	s, err := r.scoreboard(ctx, now)
	if err != nil {
		return time.Time{}, err
	}
	interval := r.fallback(now).Sub(now)
	if t.KPI.Enabled() {
		minutes := t.KPI.OnTargetMinutes
		if s.Behind() {
			minutes = t.Limits.MinMinutesBetweenWakes
		}
		interval = time.Duration(minutes) * time.Minute
	}
	_, dayEnd, _ := t.Limits.DayBounds(now)
	horizon := dayEnd
	if quiet, ok := r.schedule.NextQuietFrom(now); ok && quiet.Before(horizon) {
		horizon = quiet
	}
	interval = max(interval, pacedInterval(horizon.Sub(now), s.SpendToday, int64(s.WakesToday), t.Limits.MaxUSDPerDay-s.SpendToday))
	return r.settleWake(ctx, now)(now.Add(interval))
}

// pacedInterval spreads what is left of the daily budget over what is left of
// the working day, so a behind agent does not spend it all by the afternoon and
// sleep through the evening peak.
//
//	interval = timeLeft * (spentToday / wakesToday) / usdLeft
//
// timeLeft is the time until the working day ends: the next start of the quiet
// window, or the day reset, whichever comes first. spentToday / wakesToday is
// the average cost of one agent wake today, all phases included, and usdLeft is
// limits.max_usd_per_day minus spentToday. The interval is the time that, at
// that average cost, makes the budget last until timeLeft is over. The caller
// takes the larger of it and the template's own interval. With no wake or no
// spend today there is no cost history and the result is zero; with no budget
// left the day is capped and capReset decides.
func pacedInterval(timeLeft time.Duration, spentToday float64, wakesToday int64, usdLeft float64) time.Duration {
	if timeLeft <= 0 || wakesToday <= 0 || spentToday <= 0 || usdLeft <= 0 {
		return 0
	}
	paced := float64(timeLeft) * (spentToday / float64(wakesToday)) / usdLeft
	return time.Duration(min(paced, float64(timeLeft)))
}

// settleWake returns the rule every wake time follows, early ones included:
// not before a capped day resets (see capReset), and not inside the quiet
// window, where a wake spends tokens for posts nobody reads. A capped day
// cannot be worked: a wake would burn tokens on reads and ideas and publish
// nothing. The cap is read only when a time is settled.
func (r *Runtime) settleWake(ctx context.Context, now time.Time) func(time.Time) (time.Time, error) {
	return func(at time.Time) (time.Time, error) {
		reset, err := r.capReset(ctx, now)
		if err != nil {
			return time.Time{}, err
		}
		if at.Before(reset) {
			at = reset
		}
		return r.schedule.AfterQuiet(at), nil
	}
}

// capReset is when a capped day ends, or the zero time when it is not capped:
// the next local midnight, when spend or both the post and the reply caps (in
// the template's mode, as the guard counts) are used up.
func (r *Runtime) capReset(ctx context.Context, now time.Time) (time.Time, error) {
	t := r.opts.Template
	dayStart, dayEnd, day := t.Limits.DayBounds(now)
	spent, err := r.opts.Store.LLMUsageForDay(ctx, t.ID, day)
	if err != nil {
		return time.Time{}, err
	}
	done, err := r.opts.Store.CountActions(ctx, store.ActionScope{AgentID: t.ID, DryRun: t.DryRun}, dayStart, dayEnd)
	if err != nil {
		return time.Time{}, err
	}
	if spent.CostUSD >= t.Limits.MaxUSDPerDay || (done.Posts >= t.Limits.PostsPerDay && done.Replies >= t.Limits.RepliesPerDay) {
		return dayEnd, nil
	}
	return time.Time{}, nil
}
