package agent

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/tools"
)

// wakeState is what the write gates allow at the start of a wake, from the
// guard's own helpers and the store's counts.
type wakeState struct {
	Now time.Time
	Loc *time.Location
	// Today's posts and replies still allowed, and the caps.
	PostsLeft, PostsLimit, RepliesLeft, RepliesLimit int
	// BrowserLeft is the replies still allowed today to posts only a browser read showed.
	BrowserLeft, BrowserLimit int
	// HourLeft is replies left in the rolling hour, HourLeftAtEnd the same when
	// the wake ends; both are meaningless when HourLimit is 0 (off).
	HourLeft, HourLeftAtEnd, HourLimit int
	// NextPost is when min_minutes_between_posts allows a post; zero when allowed now.
	NextPost time.Time
	// RepliesPausedUntil is when the reply breaker lifts; zero when not tripped.
	RepliesPausedUntil time.Time
}

func (r *Runtime) wakeState(ctx context.Context, now time.Time) (wakeState, error) {
	t := r.opts.Template
	l := t.Limits
	scope := store.ActionScope{AgentID: t.ID, DryRun: t.DryRun}
	s := wakeState{Now: now, Loc: l.Location(), PostsLimit: l.PostsPerDay, RepliesLimit: l.RepliesPerDay, HourLimit: l.RepliesPerHour, BrowserLimit: l.BrowserRepliesPerDay}
	dayStart, dayEnd, _ := l.DayBounds(now)
	today, err := r.opts.Store.CountActions(ctx, scope, dayStart, dayEnd)
	if err != nil {
		return s, err
	}
	s.PostsLeft, s.RepliesLeft = max(l.PostsPerDay-today.Posts, 0), max(l.RepliesPerDay-today.Replies, 0)
	s.BrowserLeft = max(l.BrowserRepliesPerDay-today.BrowserReplies, 0)
	if l.RepliesPerHour > 0 {
		for _, w := range []struct {
			from time.Time
			left *int
		}{{now.Add(-time.Hour), &s.HourLeft}, {now.Add(tools.MaxWakeSpan - time.Hour), &s.HourLeftAtEnd}} {
			hour, err := r.opts.Store.CountActions(ctx, scope, w.from, now.Add(time.Second))
			if err != nil {
				return s, err
			}
			*w.left = max(l.RepliesPerHour-hour.Replies, 0)
		}
	}
	lastPost, err := r.opts.Store.LastPublish(ctx, t.ID, t.DryRun, "post")
	if err != nil {
		return s, err
	}
	if next := guard.NextPostAt(l, lastPost); now.Before(next) {
		s.NextPost = next
	}
	_, s.RepliesPausedUntil, err = guard.ReplyPause(ctx, r.opts.Store, t.ID, l, now)
	return s, err
}

// postClosed: no post can go out before the wake must end.
func (s wakeState) postClosed() bool {
	return s.PostsLeft == 0 || s.NextPost.After(s.Now.Add(tools.MaxWakeSpan))
}

// replyClosed: no reply can go out before the wake must end.
func (s wakeState) replyClosed() bool {
	return s.RepliesLeft == 0 || (s.HourLimit > 0 && s.HourLeftAtEnd == 0) || s.RepliesPausedUntil.After(s.Now.Add(tools.MaxWakeSpan))
}

// nothingToDo: no write tool is usable, so the wake could not answer anyone either.
func (s wakeState) nothingToDo() bool {
	return s.postClosed() && s.replyClosed()
}

// withClosed adds the write tools whose gates are closed for the whole wake to
// the tools hidden for other reasons.
func (s wakeState) withClosed(hidden []string) []string {
	if s.postClosed() && !slices.Contains(hidden, "post") {
		hidden = append(hidden, "post")
	}
	if s.replyClosed() && !slices.Contains(hidden, "reply") {
		hidden = append(hidden, "reply")
	}
	return hidden
}

// text is the limits block of an agent wake's user message.
func (s wakeState) text() string {
	var b strings.Builder
	b.WriteString("# Limits now (computed by code)\n")
	fmt.Fprintf(&b, "- Posts: %d of %d left today", s.PostsLeft, s.PostsLimit)
	if s.PostsLeft > 0 && !s.NextPost.IsZero() {
		fmt.Fprintf(&b, "; next post allowed at %s local (in %d min)", s.NextPost.In(s.Loc).Format("15:04"), int(math.Ceil(s.NextPost.Sub(s.Now).Minutes())))
	}
	fmt.Fprintf(&b, ".\n- Replies: %d of %d left today", s.RepliesLeft, s.RepliesLimit)
	if s.HourLimit > 0 {
		fmt.Fprintf(&b, ", %d of %d left this hour", s.HourLeft, s.HourLimit)
	}
	fmt.Fprintf(&b, ".\n- Browser replies (to feed posts): %d of %d left today; at 0, reply only to the inbox and the open reply candidates.\n", s.BrowserLeft, s.BrowserLimit)
	if !s.RepliesPausedUntil.IsZero() {
		fmt.Fprintf(&b, "- Replies are paused by the breaker until %s local; posts continue.\n", s.RepliesPausedUntil.In(s.Loc).Format("15:04"))
	}
	return b.String() + "\n"
}
