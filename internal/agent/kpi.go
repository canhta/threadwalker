package agent

import (
	"context"
	"fmt"
	"math"
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
	// NextPost is when the guard next allows a post; zero when allowed now.
	// PostsCapped means today's posts_per_day is used up. Loc renders local times.
	NextPost    time.Time
	PostsCapped bool
	Loc         *time.Location
}

func (r *Runtime) scoreboard(ctx context.Context, now time.Time) (scoreboard, error) {
	t := r.opts.Template
	dayStart, _, _ := t.Limits.SpendDayBounds(now)
	stats, err := r.opts.Store.KPIStats(ctx, t.ID, t.DryRun, now, dayStart)
	b := scoreboard{KPIStats: stats, KPI: t.KPI, Now: now, Loc: time.UTC}
	if err != nil {
		return b, err
	}
	b.Loc = r.schedule.loc
	if next := guard.NextPostAt(t.Limits, stats.LastPost); now.Before(next) {
		b.NextPost = next
	}
	u := now.UTC()
	day := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	done, err := r.opts.Store.CountActions(ctx, store.ActionScope{AgentID: t.ID, DryRun: t.DryRun}, day, day.Add(24*time.Hour))
	b.PostsCapped = done.Posts >= t.Limits.PostsPerDay
	return b, err
}

// hoursSincePublish is -1 when nothing was ever published.
func (s scoreboard) hoursSincePublish() int {
	if s.LastPublish.IsZero() {
		return -1
	}
	return int(s.Now.Sub(s.LastPublish).Hours())
}

// Behind: engagements under target, or no publish for longer than the maximum.
// A template without a kpi block is never behind.
func (s scoreboard) Behind() bool {
	if !s.KPI.Enabled() {
		return false
	}
	h := s.hoursSincePublish()
	return s.Engagements < int64(s.KPI.EngagementsPerDay) || h < 0 || h >= s.KPI.MaxHoursWithoutPublish
}

// gap is the one blunt line that ends the numbers.
func (s scoreboard) gap() string {
	h := s.hoursSincePublish()
	if !s.Behind() {
		return fmt.Sprintf("on target: %d of %d engagements in 24h; last published %dh ago", s.Engagements, s.KPI.EngagementsPerDay, h)
	}
	line := fmt.Sprintf("behind: %d of %d engagements in 24h after $%.2f and %d wakes today", s.Engagements, s.KPI.EngagementsPerDay, s.SpendToday, s.WakesToday)
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
	if s.PostsCapped {
		b.WriteString("- Posts: today's cap is used; no more posts today, only reply.\n")
	} else if !s.NextPost.IsZero() {
		fmt.Fprintf(&b, "- Next post allowed at %s local (in %d min); until then only reply.\n", s.NextPost.In(s.Loc).Format("15:04"), int(math.Ceil(s.NextPost.Sub(s.Now).Minutes())))
	}
	b.WriteString(s.gap() + "\n")
	b.WriteString("Pressure means: try more and better ideas, never lower quality. The critic still decides and the caps still hold.")
	if s.Behind() {
		b.WriteString(" You may not end this wake idle: call ideas (when you have it), try at least one post or reply, and if nothing is published end with a one-line note_write saying why.")
	}
	return b.String() + "\n\n"
}

// stopCheck is the extra turn an agent gets when it stops early. A behind
// agent may not stop idle. Any agent that wants to stop while posts it read
// still score well and today's budget is not spent is shown those posts and
// the money left; it decides which deserve a reply, or says why none do. No
// count is imposed: the budget and what it read set the volume. Empty when the
// stop is allowed. hidden lists tools this wake does not offer.
func (s scoreboard) stopCheck(hidden []string, open func() []string, usdLeft float64) func(called map[string]bool, succeeded map[string]int) string {
	return func(called map[string]bool, succeeded map[string]int) string {
		var todo []string
		if s.Behind() {
			if !called["ideas"] && !slices.Contains(hidden, "ideas") {
				todo = append(todo, "call ideas")
			}
			if !called["post"] && !called["reply"] {
				todo = append(todo, "try at least one post or reply")
			}
		}
		if refs := open(); len(refs) > 0 && usdLeft >= minUSDToContinue {
			shown := refs[:min(len(refs), 8)]
			todo = append(todo, fmt.Sprintf("%d posts you read still score well and you have not answered them (%s); $%.2f of today's budget is left: reply to each one you have a real angle for, and stop only when none of them is worth it, saying why in note_write", len(refs), strings.Join(shown, ", "), usdLeft))
		}
		if s.Behind() && succeeded["post"] == 0 && succeeded["reply"] == 0 && succeeded["note_write"] == 0 {
			todo = append(todo, "write a one-line note_write saying why nothing was published")
		}
		if len(todo) == 0 {
			return ""
		}
		prefix := "Before you stop: "
		if s.Behind() {
			prefix = "You are " + s.gap() + ". Before you stop: "
		}
		return prefix + strings.Join(todo, ", then ") + "."
	}
}

// minUSDToContinue is the spend left below which a wake is not sent back to
// answer more posts; one reply with its writer and critic costs about this.
const minUSDToContinue = 0.05

// nextWake is when code wakes the agent again: the template minimum when
// behind, the on-target interval when not, and the end of the capped day
// when the day is capped (see capReset).
func (r *Runtime) nextWake(ctx context.Context, now time.Time) (time.Time, error) {
	t := r.opts.Template
	s, err := r.scoreboard(ctx, now)
	if err != nil {
		return time.Time{}, err
	}
	next := r.fallback(now)
	if t.KPI.Enabled() {
		minutes := t.KPI.OnTargetMinutes
		if s.Behind() {
			minutes = t.Limits.MinMinutesBetweenWakes
		}
		next = now.Add(time.Duration(minutes) * time.Minute)
	}
	// A capped day cannot be worked: a wake would burn tokens on reads and ideas
	// and publish nothing. Wait until the caps reset.
	reset, err := r.capReset(ctx, now)
	if err != nil {
		return time.Time{}, err
	}
	if next.Before(reset) {
		next = reset
	}
	// Nobody reads at 3am: a night wake spends tokens for posts that sink.
	return r.schedule.AfterQuiet(next), nil
}

func nextUTCDay(now time.Time) time.Time {
	return now.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
}

// capReset is when a capped day ends, or the zero time when it is not capped.
// Spend resets at the next local midnight; both the post and the reply caps
// used (in the template's mode, as the guard counts) reset at the next UTC
// midnight. When both hold, the later one wins.
func (r *Runtime) capReset(ctx context.Context, now time.Time) (time.Time, error) {
	t := r.opts.Template
	_, spendEnd, day := t.Limits.SpendDayBounds(now)
	spent, err := r.opts.Store.LLMUsageForDayPhases(ctx, t.ID, day, "agent", "study")
	if err != nil {
		return time.Time{}, err
	}
	utcDay := now.UTC().Truncate(24 * time.Hour)
	done, err := r.opts.Store.CountActions(ctx, store.ActionScope{AgentID: t.ID, DryRun: t.DryRun}, utcDay, utcDay.Add(24*time.Hour))
	if err != nil {
		return time.Time{}, err
	}
	var reset time.Time
	if spent.CostUSD >= t.Limits.MaxUSDPerDay {
		reset = spendEnd
	}
	if done.Posts >= t.Limits.PostsPerDay && done.Replies >= t.Limits.RepliesPerDay && nextUTCDay(now).After(reset) {
		reset = nextUTCDay(now)
	}
	return reset, nil
}
