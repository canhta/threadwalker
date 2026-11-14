package telegram

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/canhta/threadz/internal/store"
)

const (
	retiredShown   = 5
	retiredRunes   = 50
	episodeRunes   = 50
	reportExamples = 2
)

// clip shortens text to n runes for a list line.
func clip(text string, n int) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	return string([]rune(text)[:n-1]) + "…"
}

func rankText(r float64) string { return fmt.Sprintf("%.2f", r) }

// lessonsText answers /lessons: the active lessons with their use, then the
// few most recently retired.
func (s *Service) lessonsText(ctx context.Context, id string) (string, error) {
	all, err := s.opts.Store.Lessons(ctx, id, true)
	if err != nil {
		return "", err
	}
	var active, retired []store.Lesson
	for _, l := range all {
		if l.Active() {
			active = append(active, l)
		} else {
			retired = append(retired, l)
		}
	}
	uses, err := s.opts.Store.LessonUses(ctx, id, active)
	if err != nil {
		return "", err
	}
	lines := []string{fmt.Sprintf("Lessons (%d active)", len(active))}
	if len(active) == 0 {
		lines = append(lines, "None yet. The agent adds one after its daily review when the results support it.")
	}
	for _, l := range active {
		lines = append(lines, "", fmt.Sprintf("#%d %s, %s", l.ID, l.Kind, l.Polarity), l.Text, uses[l.ID].Summary())
	}
	if len(retired) > 0 {
		slices.SortStableFunc(retired, func(a, b store.Lesson) int { return b.RetiredAt.Compare(a.RetiredAt) })
		lines = append(lines, "", "Recently retired")
		for _, l := range retired[:min(retiredShown, len(retired))] {
			lines = append(lines, fmt.Sprintf("#%d %s, retired by %s on %s: %s", l.ID, l.Polarity, l.RetiredBy, l.RetiredAt.In(s.location(id)).Format("2 Jan"), clip(l.Text, retiredRunes)))
		}
	}
	lines = append(lines, "", "Send /forget <id> to remove a lesson for good.")
	return strings.Join(lines, "\n"), nil
}

// forget answers /forget: the lesson is retired by the owner, which also keeps
// the agent from adding it again.
func (s *Service) forget(ctx context.Context, id, arg string) (string, error) {
	n, err := strconv.ParseInt(arg, 10, 64)
	if err != nil || n < 1 {
		return "Lesson not forgotten\nThe id must be a number from /lessons.", nil
	}
	err = s.opts.Store.ForgetLesson(ctx, id, n, s.opts.Now())
	var refusal *store.LessonRefusal
	if errors.As(err, &refusal) {
		return fmt.Sprintf("Lesson not forgotten\nThere is no active lesson #%d.", n), nil
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Lesson #%d forgotten\nThe agent will not add it again.", n), nil
}

// learning is what the daily report says about episodes and lessons.
type learning struct {
	best, worst               []store.Episode
	added, confirmed, retired []store.Lesson
}

// learning gathers the episodes measured and the lessons changed in the 24
// hours up to now.
func (s *Service) learning(ctx context.Context, id string, now time.Time) (learning, error) {
	var out learning
	since := now.Add(-24 * time.Hour)
	eps, err := s.opts.Store.Episodes(ctx, id, store.EpisodeQuery{})
	if err != nil {
		return out, err
	}
	fresh := eps[:0]
	for _, e := range eps {
		if e.MeasuredAt.After(since) {
			fresh = append(fresh, e)
		}
	}
	out.best = store.Best(fresh, reportExamples)
	taken := map[int64]bool{}
	for _, e := range out.best {
		taken[e.ActionID] = true
	}
	for _, e := range store.Worst(fresh, reportExamples+len(out.best)) {
		if !taken[e.ActionID] && len(out.worst) < reportExamples {
			out.worst = append(out.worst, e)
		}
	}
	lessons, err := s.opts.Store.Lessons(ctx, id, true)
	if err != nil {
		return out, err
	}
	for _, l := range lessons {
		switch {
		case !l.CreatedAt.Before(since):
			out.added = append(out.added, l)
		case !l.ConfirmedAt.Before(since):
			out.confirmed = append(out.confirmed, l)
		}
		if !l.Active() && !l.RetiredAt.Before(since) {
			out.retired = append(out.retired, l)
		}
	}
	return out, nil
}

func episodeLine(e store.Episode) string {
	return fmt.Sprintf("#%d %s at %dh, rank %s of %d: %d views, %d likes, %d replies. %s", e.ActionID, e.Kind, e.Window, rankText(e.Rank), e.Pool, e.Views, e.Likes, e.Replies, clip(e.Text, episodeRunes))
}

// lines renders the episode and lesson part of the report.
func (l learning) lines() []string {
	var out []string
	if len(l.best) == 0 {
		out = append(out, "Episodes: none ranked in this period")
	} else {
		out = append(out, "Best episodes")
		for _, e := range l.best {
			out = append(out, episodeLine(e))
		}
	}
	if len(l.worst) > 0 {
		out = append(out, "Weakest episodes")
		for _, e := range l.worst {
			out = append(out, episodeLine(e))
		}
	}
	out = append(out, fmt.Sprintf("Lessons: %d added, %d confirmed, %d retired", len(l.added), len(l.confirmed), len(l.retired)))
	for _, x := range l.added {
		out = append(out, fmt.Sprintf("Added #%d %s, %s: %s", x.ID, x.Kind, x.Polarity, clip(x.Text, 2*retiredRunes)))
	}
	for _, x := range l.confirmed {
		out = append(out, fmt.Sprintf("Confirmed #%d %s, %s: %s", x.ID, x.Kind, x.Polarity, clip(x.Text, 2*retiredRunes)))
	}
	for _, x := range l.retired {
		out = append(out, fmt.Sprintf("Retired #%d by %s: %s", x.ID, x.RetiredBy, clip(x.Text, 2*retiredRunes)))
	}
	return out
}
