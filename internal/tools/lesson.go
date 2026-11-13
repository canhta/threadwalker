package tools

import (
	"cmp"
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/logging"

	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
)

var (
	errLessonNoCitation = errors.New("lesson cites no action_id")
	errLessonForeign    = errors.New("lesson cites an action that is not yours")
	errLessonNumber     = errors.New("lesson number is not a counter on the cited action")
	errLessonURL        = errors.New("lesson url is not one of your action urls")
	errLessonSecret     = errors.New("lesson looks like a secret")
	errLessonCap        = errors.New("dream lesson cap reached")
)

// lessonReasons is the one table from a rejection to its logged reason code.
var lessonReasons = []struct {
	err    error
	reason string
}{
	{errLessonNoCitation, "lesson_missing_citation"},
	{errLessonForeign, "lesson_foreign_citation"},
	{errLessonNumber, "lesson_number_mismatch"},
	{errLessonURL, "lesson_foreign_url"},
	{errLessonSecret, "lesson_secret"},
	{errLessonCap, "lesson_cap"},
}

// failureReason is the audit reason code of a failed tool: the lesson gate's
// code, the Threads backend's code, or operation_failed.
func failureReason(err error) string {
	if r := lessonReason(err); r != "operation_failed" {
		return r
	}
	return cmp.Or(threads.Reason(err), "operation_failed")
}

func lessonReason(err error) string {
	for _, r := range lessonReasons {
		if errors.Is(err, r.err) {
			return r.reason
		}
	}
	return "operation_failed"
}

var (
	urlPattern    = regexp.MustCompile(`https?://[^\s)\]>"']+`)
	numberPattern = regexp.MustCompile(`\d+(?:[.,]\d+)*`)
	secretPattern = regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}|(?i:bearer\s+[A-Za-z0-9._~+/=-]{16,})|[A-Za-z0-9_-]{40,}|-----BEGIN`)
)

// checkLesson accepts text only when every URL is one of the agent's own
// action URLs and every number equals a counter measured on the cited action.
func checkLesson(text string, ev store.ActionEvidence) error {
	if secretPattern.MatchString(text) {
		return errLessonSecret
	}
	for _, u := range urlPattern.FindAllString(text, -1) {
		if !ev.URLs[strings.TrimRight(u, ".,;:!?")] {
			return errLessonURL
		}
	}
	for _, n := range numberPattern.FindAllString(urlPattern.ReplaceAllString(text, " "), -1) {
		v, err := strconv.ParseInt(strings.ReplaceAll(n, ",", ""), 10, 64)
		if err != nil || strings.Contains(n, ".") || !ev.Counters[v] {
			return errLessonNumber
		}
	}
	return nil
}

func (d *Dispatcher) memoryActor() store.MemoryActor {
	return store.MemoryActor{WakeID: d.opts.WakeID, Phase: d.opts.Phase}
}

// checkDreamLesson gates memory_add during a dream; other phases are unchecked.
// A rejection is logged with its reason code and never reaches the memory index.
func (d *Dispatcher) checkDreamLesson(ctx context.Context, a arguments) error {
	if d.opts.Phase != guard.PhaseDream {
		return nil
	}
	err := d.lessonVerdict(ctx, a)
	if err != nil {
		d.event(ctx, logging.LessonRejected, "rejected", lessonReason(err), "memory_add", time.Time{})
	}
	return err
}

func (d *Dispatcher) lessonVerdict(ctx context.Context, a arguments) error {
	if d.lessons >= guard.MaxDreamLessons {
		return errLessonCap
	}
	if a.Int("action_id") == 0 {
		return errLessonNoCitation
	}
	ev, err := d.opts.Store.ActionEvidence(ctx, d.opts.Template.ID, a.Int("action_id"))
	if err != nil {
		return err
	}
	if !ev.Owned {
		return errLessonForeign
	}
	return checkLesson(a.Text("text"), ev)
}
