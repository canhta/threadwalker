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
//
// It is the one map from an executor's error to its code: the audit row, the
// tool.finished event and the per-tool events all read it, and a code listed in
// modelFixable reaches the model as a blocked hint instead of a generic error.
func failureReason(err error) string {
	var blocked *guard.BlockedError
	switch {
	case err == nil:
		return "completed"
	case errors.As(err, &blocked):
		return blocked.Reason
	case errors.Is(err, store.ErrStrategyInput):
		return "strategy_input"
	case errors.Is(err, store.ErrPlaybookInput):
		return "playbook_input"
	case errors.Is(err, errUnknownKnowledge):
		return "unknown_knowledge"
	case errors.Is(err, errBudgetSpent):
		return "budget"
	}
	if r := lessonReason(err); r != "operation_failed" {
		return r
	}
	return cmp.Or(threads.Reason(err), "operation_failed")
}

var (
	errUnknownKnowledge = errors.New("unknown knowledge_id")
	errBudgetSpent      = errors.New("daily budget spent")
)

// modelFixable are the codes the model can act on; each has a guard hint.
var modelFixable = map[string]bool{
	"lesson_missing_citation": true, "lesson_foreign_citation": true, "lesson_number_mismatch": true,
	"lesson_foreign_url": true, "lesson_secret": true, "lesson_cap": true,
	"strategy_input": true, "playbook_input": true, "unknown_knowledge": true, "budget": true,
}

// modelFacing gives a model-fixable failure the blocked hint the model reads.
func modelFacing(err error) error {
	var blocked *guard.BlockedError
	if code := failureReason(err); modelFixable[code] && !errors.As(err, &blocked) {
		return errors.Join(err, &guard.BlockedError{Reason: code})
	}
	return err
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
	// refPattern is what a lesson may name without it being a claimed counter:
	// an ISO date, an action or note id, and a short ref such as r69.
	refPattern    = regexp.MustCompile(`(?i)\b\d{4}-\d{2}-\d{2}\b|\b(?:action|note)(?:_id)?[\s:#]*\d+|\br\d+\b`)
	secretPattern = regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}|(?i:bearer\s+[A-Za-z0-9._~+/=-]{16,})|[A-Za-z0-9_-]{40,}|-----BEGIN`)
)

// checkLesson accepts text only when every URL is one of the agent's own
// action URLs and every number equals a counter measured on the cited action.
// Dates, action and note ids and short refs are names, not counters.
func checkLesson(text string, ev store.ActionEvidence) error {
	if secretPattern.MatchString(text) {
		return errLessonSecret
	}
	for _, u := range urlPattern.FindAllString(text, -1) {
		if !ev.URLs[strings.TrimRight(u, ".,;:!?")] {
			return errLessonURL
		}
	}
	for _, n := range numberPattern.FindAllString(refPattern.ReplaceAllString(urlPattern.ReplaceAllString(text, " "), " "), -1) {
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
