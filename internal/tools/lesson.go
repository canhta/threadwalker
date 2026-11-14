package tools

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/canhta/threadz/internal/guard"

	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
)

var (
	errLessonNumber = errors.New("lesson number is not a counter on the cited action")
	errLessonURL    = errors.New("lesson url is not one of your action urls")
	errLessonSecret = errors.New("lesson looks like a secret")
	errLessonRef    = errors.New("lesson names a date or an action id")
)

// lessonReasons is the one table from a rejection to its logged reason code.
var lessonReasons = []struct {
	err    error
	reason string
}{
	{errLessonNumber, "lesson_number_mismatch"},
	{errLessonURL, "lesson_foreign_url"},
	{errLessonSecret, "lesson_secret"},
	{errLessonRef, "lesson_has_ref"},
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
	case errors.Is(err, errStaleRef):
		return "stale_ref"
	case errors.Is(err, errBudgetSpent):
		return "budget"
	}
	if r := lessonReason(err); r != "operation_failed" {
		return r
	}
	return cmp.Or(threads.Reason(err), "operation_failed")
}

var (
	errStaleRef    = errors.New("text names a post or trend ref")
	errBudgetSpent = errors.New("daily budget spent")
)

// modelFixable are the codes the model can act on; each has a guard hint.
var modelFixable = map[string]bool{
	"lesson_number_mismatch": true, "lesson_foreign_url": true, "lesson_secret": true, "lesson_has_ref": true, "budget": true, "stale_ref": true,
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
	// refPattern is a name a lesson must not carry: an ISO date or an action id.
	refPattern = regexp.MustCompile(`(?i)\b\d{4}-\d{2}-\d{2}\b|\baction(?:_id)?[\s:#]*\d+`)
	// idRefPattern is a ref such as p58 or t3: an id, not content. A trend ref
	// restarts every wake and a post ref expires after 7 days, so stored text
	// that names one points at nothing later.
	idRefPattern  = regexp.MustCompile(`\b[pt]\d+\b`)
	secretPattern = regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}|(?i:bearer\s+[A-Za-z0-9._~+/=-]{16,})|[A-Za-z0-9_-]{40,}|-----BEGIN`)
)

// checkLesson accepts text only when every URL is one of the agent's own
// action URLs and every number equals a counter measured on the cited action.
// Text that names a date, an action id or a post or trend ref is refused: a
// lesson states what to do, not when or from which post.
func checkLesson(text string, ev store.ActionEvidence) error {
	if secretPattern.MatchString(text) {
		return errLessonSecret
	}
	if idRefPattern.MatchString(text) {
		return errStaleRef
	}
	if refPattern.MatchString(text) {
		return errLessonRef
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

// lessonFailure is what a failed lesson tool returns: a refusal becomes the
// blocked hint the model reads, anything else stays generic.
func lessonFailure(err error, tool string) error {
	var refusal *store.LessonRefusal
	if errors.As(err, &refusal) {
		return &guard.BlockedError{Reason: refusal.Reason, Detail: refusal.Detail}
	}
	return errors.New(tool + ": failed")
}

// citedEvidence merges what the cited actions can back a claim with.
func (d *Dispatcher) citedEvidence(ctx context.Context, ids []int64) (store.ActionEvidence, error) {
	merged := store.ActionEvidence{Owned: true, Counters: map[int64]bool{}, URLs: map[string]bool{}}
	for _, id := range ids {
		ev, err := d.opts.Store.ActionEvidence(ctx, d.opts.Template.ID, id)
		if err != nil {
			return merged, err
		}
		maps.Copy(merged.Counters, ev.Counters)
		maps.Copy(merged.URLs, ev.URLs)
	}
	return merged, nil
}

// lessonTools are the dream's three tools; the guard offers them to no other phase.
func (d *Dispatcher) lessonTools() map[string]executor {
	agent := d.opts.Template.ID
	return map[string]executor{
		"lesson_add": func(ctx context.Context, a arguments) (any, error) {
			kind, polarity, ids := a.Text("kind"), a.Text("polarity"), a.IDList("action_ids")
			if !slices.Contains([]string{"post", "reply", "any"}, kind) || !slices.Contains([]string{"do", "avoid"}, polarity) {
				return nil, &guard.BlockedError{Reason: "lesson_enum"}
			}
			if err := d.opts.Store.CheckCitations(ctx, agent, kind, polarity, ids); err != nil {
				return nil, lessonFailure(err, "adding lesson")
			}
			ev, err := d.citedEvidence(ctx, ids)
			if err != nil {
				return nil, errors.New("adding lesson: failed")
			}
			if err = checkLesson(a.Text("text"), ev); err != nil {
				return nil, modelFacing(err)
			}
			id, err := d.opts.Store.AddLesson(ctx, agent, kind, polarity, a.Text("text"), ids, d.opts.Now())
			if err != nil {
				return nil, lessonFailure(err, "adding lesson")
			}
			return map[string]int64{"lesson_id": id}, nil
		},
		"lesson_confirm": func(ctx context.Context, a arguments) (any, error) {
			if err := d.opts.Store.ConfirmLesson(ctx, agent, a.Int("id"), a.IDList("action_ids"), d.opts.Now()); err != nil {
				return nil, lessonFailure(err, "confirming lesson")
			}
			return map[string]bool{"confirmed": true}, nil
		},
		"lesson_retire": func(ctx context.Context, a arguments) (any, error) {
			if err := d.opts.Store.RetireLesson(ctx, agent, a.Int("id"), store.RetiredByDream, a.Text("reason"), d.opts.Now()); err != nil {
				return nil, lessonFailure(err, "retiring lesson")
			}
			return map[string]bool{"retired": true}, nil
		},
	}
}
