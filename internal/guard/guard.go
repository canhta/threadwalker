// Package guard owns the mandatory policy, audit and pre-effect intent boundary.
package guard

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/quality"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/trends"
)

type Scope struct {
	// OwnUsernames is a complete immutable operator identity set, never model data.
	OwnUsernames []string
	// AllowLive is an operator capability, never a model tool argument.
	AllowLive bool
	// Phase is the harness-owned wake phase: agent (default), dream or study.
	// Dream and study allow only read and note/playbook tools.
	Phase    string
	Store    *store.Store
	Template config.Template
	WakeID   int64
	Now      func() time.Time
	Logger   *slog.Logger
	// APIQuota reads the Threads API's documented publishing quota. It is an
	// extra limit beside the template's own; nil when writes never publish.
	APIQuota func(context.Context) (APIQuota, error)
	// Critic reviews a post or reply before it is reserved. Nil skips the
	// review; the agent runtime always sets it.
	Critic func(context.Context, quality.CriticInput) (quality.CriticVerdict, error)
	// Writer drafts the text of a post or reply from the agent's brief; nil
	// publishes the agent's own text. It runs only together with Critic.
	Writer func(context.Context, quality.WriterInput) ([]string, error)
	// Ideas backs the ideas tool; nil leaves the tool out.
	Ideas func(context.Context, []quality.Item) ([]quality.Idea, error)
	// Sleep, when set, lets a draft the critic passed wait out the pacing rule
	// (limits.min_seconds_between_actions) instead of being thrown away.
	Sleep func(context.Context, time.Duration) error
}

// MaxPacingWait bounds how long a reviewed draft waits for the pacing rule.
const MaxPacingWait = 10 * time.Minute

// DefaultCriticRejections is limits.critic_rejections_per_wake when unset.
const DefaultCriticRejections = 2

// APIQuota is what the Threads API still allows in its window; a negative
// value means the API names no limit.
type APIQuota struct{ Posts, Replies int64 }

type Guard struct {
	opts        Scope
	stopPhrases []string
	own         map[string]bool
	mu          sync.Mutex
	read        map[string]string
	// inbox holds the canonical threads of replies to the account noted this
	// wake, each with the root post of its conversation.
	inbox     map[string]string
	readText  map[string]string
	trendText map[string]string
	facts     map[string]trends.ItemFacts
	trends    map[string]bool
	// shortRef maps a noted item to its short ref; refItem is the inverse.
	shortRef, refItem map[string]string
	statPattern       *regexp.Regexp
	rejections        int
}

// Attempt is one tool call. Arguments are the raw model text and are never
// persisted. Logged is what the audit stores in their place: the dispatcher's
// redacted, per-field-capped copy, empty when the arguments hold a secret.
type Attempt struct {
	Tool, Arguments, BlockedBy string
	Logged                     string
	// ReservedActionID receives the committed intent ID before effect executes.
	// The dispatcher uses it to report the action without inserting a second row.
	ReservedActionID *int64
	// Final receives the texts to publish when the writer rewrote them.
	Final *[]string
	// Target receives the permalink a reply's target_url short ref resolved to.
	Target *string
}

var ErrAudit = errors.New("tool audit failed")
var ErrBlocked = errors.New(logging.ToolBlocked)

// ErrActionLimit stops this wake after a daily action quota refuses an intent.
// The agent stays running; its next wake can use the next UTC day quota.
var ErrActionLimit = errors.New("daily action limit")

type actionLimitError struct{ rule string }

func (e *actionLimitError) Error() string {
	return "executing guarded tool: " + e.rule + ": daily action limit"
}
func (e *actionLimitError) Unwrap() error           { return errors.Join(ErrBlocked, ErrActionLimit) }
func (e *actionLimitError) TerminalToolError() bool { return true }

// ReasonedError carries the reason code of a failed effect into the audit row.
type ReasonedError struct {
	Err  error
	Code string
	// Stage and Evidence name the browser step that failed and the saved page snapshot.
	Stage, Evidence string
}

func (e *ReasonedError) Error() string { return e.Err.Error() }
func (e *ReasonedError) Unwrap() error { return e.Err }

type auditError struct{ cause error }

func (e *auditError) Error() string           { return "recording tool audit: failed" }
func (e *auditError) Unwrap() error           { return errors.Join(ErrAudit, e.cause) }
func (e *auditError) TerminalToolError() bool { return true }

func New(opts Scope) (*Guard, error) {
	if opts.Store == nil || opts.Template.ID == "" || opts.WakeID <= 0 || (!opts.Template.DryRun && !opts.AllowLive) {
		return nil, errors.New("configuring guard: store, agent, wake and action capability required")
	}
	if opts.Phase != "" && opts.Phase != PhaseAgent && opts.Phase != PhaseDream && opts.Phase != PhaseStudy {
		return nil, errors.New("configuring guard: unknown wake phase")
	}
	if opts.Phase == "" {
		opts.Phase = PhaseAgent
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	phrases := opts.Template.Guard.StopPhrases
	if phrases == nil {
		phrases = []string{"stop", "stop replying", "do not contact", "don’t contact", "don't contact", "leave me alone", "đừng trả lời", "đừng liên hệ", "ngừng trả lời", "dừng trả lời", "đừng làm phiền"}
	}
	if len(phrases) == 0 {
		return nil, errors.New("configuring guard: empty stop phrase list")
	}
	for _, phrase := range phrases {
		if strings.TrimSpace(phrase) == "" {
			return nil, errors.New("configuring guard: empty stop phrase")
		}
	}
	opts.Template.Guard.BlockedPhrases = append([]string(nil), opts.Template.Guard.BlockedPhrases...)
	own := map[string]bool{}
	users := append([]string(nil), opts.OwnUsernames...)
	if opts.Template.Threads.Username != "" {
		users = append(users, opts.Template.Threads.Username)
	}
	for _, raw := range users {
		user, err := store.NormalizeUsername(raw)
		if err != nil {
			return nil, errors.New("configuring guard: invalid own account identity")
		}
		own[user] = true
	}
	opts.OwnUsernames = users
	g := &Guard{opts: opts, stopPhrases: append([]string(nil), phrases...), own: own, read: map[string]string{}, inbox: map[string]string{}, readText: map[string]string{}, trendText: map[string]string{}, facts: map[string]trends.ItemFacts{}, trends: map[string]bool{}, shortRef: map[string]string{}, refItem: map[string]string{}}
	if units := opts.Template.Guard.SelfStatUnits; len(units) > 0 {
		g.statPattern = statPattern(units)
	}
	return g, nil
}

func (g *Guard) event(ctx context.Context, event, outcome, reason, tool string) {
	if tool == "unknown" {
		tool = ""
	}
	logging.Emit(ctx, g.opts.Logger, slog.LevelInfo, logging.Record{Event: event, AgentID: g.opts.Template.ID, Phase: g.opts.Phase, WakeID: g.opts.WakeID, Tool: tool, Operation: "tool_guard", Outcome: outcome, Reason: reason})
}

// Execute commits an attempt row before invoking effect. An unfinished audit row
// deliberately survives crashes; callers must never replay it. Result audit
// contains sizes/outcomes, never external bodies or profiles of strangers.
func (g *Guard) Execute(ctx context.Context, a Attempt, effect func() (string, error)) (output string, err error) {
	if _, ok := Spec(a.Tool); !ok {
		a.Tool = "unknown"
	}
	if effect == nil {
		return "", fmt.Errorf("executing guarded tool: %w", ErrBlocked)
	}
	reason := a.BlockedBy
	if reason == "" && a.Tool == "unknown" {
		reason = "unknown_tool"
	}
	var detail store.CallDetail
	running, open, err := g.opts.Store.AgentWakeOpen(ctx, g.opts.Template.ID, g.opts.WakeID)
	if err != nil {
		g.event(ctx, "guard.checked", "error", "state_read_failed", a.Tool)
		return "", &auditError{err}
	}
	if reason == "" {
		if !running {
			reason = "agent_not_running"
		} else if !open {
			reason = "wake_not_open"
		}
	}
	var intent *actionIntent
	if reason == "" {
		reason = g.phaseBlock(a.Tool)
	}
	if reason == "" {
		intent, reason = g.content(a, g.opts.Writer != nil && g.opts.Critic != nil)
	}
	if intent != nil {
		detail.Ref = intent.refTrace
	}
	if reason == "text_length" && intent != nil {
		intent.tries = []store.TryTrace{draftTrace("agent", intent.texts(), nil)}
	}
	if reason == "" && intent != nil && g.opts.Critic != nil {
		// Limits first: the writer and critic are the expensive part.
		_, early, err := g.reserve(ctx, a, intent, 0, g.opts.Now().UTC(), true)
		if err != nil {
			g.event(ctx, "guard.checked", "error", "reservation_failed", a.Tool)
			return "", &auditError{err}
		}
		if early != "min_seconds_between_actions" {
			reason = early
		}
	}
	if reason == "" && intent != nil && g.opts.Critic != nil {
		reason, intent.detail, intent.criticJSON = g.review(ctx, a, intent)
		if a.Final != nil && reason == "" {
			*a.Final = intent.texts()
		}
	}
	if reason == "" && intent != nil {
		reason = g.apiQuota(ctx, a.Tool, len(intent.Parts))
	}
	now := g.opts.Now().UTC()
	if reason == "" && intent != nil && g.statPattern != nil {
		facts, err := FactSheet(ctx, g.opts.Store, g.opts.Template, now)
		if err != nil {
			g.event(ctx, "guard.checked", "error", "state_read_failed", a.Tool)
			return "", &auditError{err}
		}
		g.mu.Lock()
		quoted := g.readText[intent.thread] // a reply may quote the post it answers
		g.mu.Unlock()
		for _, text := range intent.texts() {
			if n := ungroundedNumber(g.statPattern, text, facts, quoted); n != "" {
				intent.detail, reason = n, "ungrounded_number"
				break
			}
		}
	}
	var object map[string]json.RawMessage
	valid := json.Unmarshal([]byte(a.Arguments), &object) == nil && object != nil
	metadata, _ := json.Marshal(struct {
		Bytes       int  `json:"bytes"`
		ValidObject bool `json:"valid_object"`
	}{len(a.Arguments), valid})
	id, err := g.opts.Store.RecordToolAttempt(ctx, g.opts.Template.ID, g.opts.WakeID, a.Tool, string(metadata), reason, now)
	if err == nil {
		err = g.opts.Store.RecordCallArgs(ctx, g.opts.Template.ID, g.opts.WakeID, id, cmp.Or(a.Logged, "{}"), now)
	}
	if err != nil {
		g.event(ctx, logging.AuditRecorded, "error", "audit_insert_failed", a.Tool)
		return "", &auditError{err}
	}
	// The detail is written on every way out once the call is audited, from
	// the cause of the refusal or the effect's own result.
	defer func() {
		if intent != nil {
			detail.Detail, detail.Tries = intent.detail, intent.tries
		}
		g.recordDetail(ctx, id, detail)
	}()
	g.event(ctx, logging.AuditRecorded, "success", "attempt_recorded", a.Tool)
	if reason == "" && intent != nil {
		var reserved int64
		if intent.posting, err = trends.ContextOf(ctx, g.opts.Store, g.opts.Template, g.opts.OwnUsernames, intent.Text, intent.TargetURL, now); err != nil {
			g.event(ctx, "guard.checked", "error", "reservation_failed", a.Tool)
			return "", &auditError{err}
		}
		g.overlayFacts(&intent.posting, intent, a.Tool)
		reserved, reason, err = g.reserve(ctx, a, intent, id, now, false)
		if err == nil && reason == "min_seconds_between_actions" && intent.criticJSON != "" && g.opts.Sleep != nil {
			if allowed, perr := time.Parse(time.RFC3339, intent.detail); perr == nil {
				if wait := allowed.Sub(now) + time.Second; wait > 0 && wait <= MaxPacingWait {
					g.event(ctx, "guard.checked", "waiting", "min_seconds_between_actions", a.Tool)
					if serr := g.opts.Sleep(ctx, wait); serr == nil {
						now = g.opts.Now().UTC()
						reserved, reason, err = g.reserve(ctx, a, intent, id, now, false)
					}
				}
			}
		}
		if err != nil {
			g.event(ctx, "guard.checked", "error", "reservation_failed", a.Tool)
			return "", &auditError{err}
		}
		detail.ActionID = reserved
		if a.ReservedActionID != nil {
			*a.ReservedActionID = reserved
		}
	}
	if reason != "" {
		g.event(ctx, "guard.checked", "blocked", reason, a.Tool)
		g.event(ctx, logging.ToolBlocked, "blocked", reason, a.Tool)
		if reason == "posts_per_day" || reason == "replies_per_day" {
			return "", &actionLimitError{rule: reason}
		}
		detail := ""
		if intent != nil {
			detail = intent.detail
		}
		return "", &BlockedError{Reason: reason, Detail: detail}
	}
	g.event(ctx, "guard.checked", "allowed", "policy_allowed", a.Tool)
	output, effectErr := effect()
	outcome, failure := "success", ""
	if effectErr != nil {
		outcome = "error"
		var reasoned *ReasonedError
		if errors.As(effectErr, &reasoned) {
			failure, detail.Stage, detail.Evidence = reasoned.Code, reasoned.Stage, reasoned.Evidence
		}
	}
	detail.Counts = listCounts(output)
	result, _ := json.Marshal(struct {
		Outcome string `json:"outcome"`
		Bytes   int    `json:"bytes"`
		Reason  string `json:"reason,omitempty"`
	}{outcome, len(output), failure})
	// Completion accounting must survive parent cancellation after an effect.
	// The pre-effect audit still uses the caller context and fails closed.
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := g.opts.Store.RecordToolResult(finalCtx, id, string(result)); err != nil {
		g.event(ctx, logging.AuditRecorded, "error", "audit_update_failed", a.Tool)
		return "", &auditError{err}
	}
	g.event(ctx, logging.AuditRecorded, "success", "result_recorded", a.Tool)
	return output, effectErr
}

// MaxRevisions is how many times the writer may revise a draft the editor
// (or a guard check) turned down, inside one tool call.
const MaxRevisions = 2

// review has the writer draft the text from the agent's brief (when a writer
// is configured) and the critic gate it; a turned-down draft goes back to the
// writer with the problems, up to MaxRevisions times. A draft still failing
// counts as one rejection against the wake's cap; once the cap is spent no
// further draft is written. It fails closed.
func (g *Guard) review(ctx context.Context, a Attempt, intent *actionIntent) (reason, detail, verdictJSON string) {
	g.mu.Lock()
	rejected, parent := g.rejections, g.readText[intent.thread]
	if a.Tool == "post" {
		parent = g.trendText[strings.TrimSpace(intent.TrendRef)]
		if parent == "" {
			parent = strings.TrimSpace(intent.TrendRef)
		}
	}
	g.mu.Unlock()
	limit := g.opts.Template.Limits.CriticRejectionsPerWake
	if limit <= 0 {
		limit = DefaultCriticRejections
	}
	if rejected >= limit {
		return "critic_limit", strconv.Itoa(rejected), ""
	}
	recent, err := g.opts.Store.RecentPostTexts(ctx, g.opts.Template.ID, g.opts.Template.DryRun, 10)
	if err != nil {
		return "critic_unavailable", "", ""
	}
	check := func(texts []string) []string {
		if why, d := g.recheck(a, texts); why != "" {
			return []string{Hint(why, d)}
		}
		return nil
	}
	source := "agent"
	if g.opts.Writer != nil {
		source = "writer"
		intent.tries = append(intent.tries, draftTrace("agent", intent.texts(), nil))
	}
	tries, ok, err := quality.Revise(ctx, g.opts.Writer, g.opts.Critic, check, quality.WriterInput{Kind: a.Tool, Angle: intent.Angle, Fact: intent.Fact, Parent: parent, Draft: intent.texts(), Recent: recent}, MaxRevisions)
	for _, t := range tries {
		v := t.Verdict
		intent.tries = append(intent.tries, draftTrace(source, t.Texts, &v))
	}
	if err != nil {
		return "critic_unavailable", "", ""
	}
	last := tries[len(tries)-1]
	encoded, _ := json.Marshal(last.Verdict)
	if ok {
		intent.Text, intent.Parts = last.Texts[0], last.Texts[1:]
		return "", "", string(encoded)
	}
	g.mu.Lock()
	g.rejections++
	g.mu.Unlock()
	clean := make([]string, 0, len(last.Verdict.Problems))
	for _, p := range last.Verdict.Problems {
		p = strings.Join(strings.Fields(p), " ")
		if r := []rune(p); len(r) > 200 {
			p = string(r[:200])
		}
		if p != "" {
			clean = append(clean, p)
		}
	}
	if len(clean) == 0 {
		clean = append(clean, "no reason given")
	}
	return "critic_rejected", strings.Join(clean, "; "), string(encoded)
}

// draftTrace is the operator's record of one draft; a nil verdict is a draft no editor has judged.
func draftTrace(source string, texts []string, v *quality.CriticVerdict) store.TryTrace {
	t := store.TryTrace{Source: source, Runes: make([]int, len(texts))}
	for i, text := range texts {
		t.Runes[i] = utf8.RuneCountInString(text)
	}
	first, _, _ := strings.Cut(strings.TrimSpace(texts[0]), "\n")
	t.First = capLine(first)
	if v != nil {
		t.Pass, t.Problems = v.Pass, v.Problems
		if v.Scores != (quality.CriticScores{}) {
			t.Scores, _ = json.Marshal(v.Scores)
		}
	}
	return t
}

// capLine keeps a stored first line to 120 runes.
func capLine(s string) string {
	if r := []rune(s); len(r) > 120 {
		return string(r[:120])
	}
	return s
}

// listCounts is the length of each list in a tool result, looking inside the
// untrusted wrapper; a top-level list is "items".
func listCounts(output string) map[string]int {
	var v any
	if json.Unmarshal([]byte(output), &v) != nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok && m["untrusted"] == true {
		v = m["data"]
	}
	counts := map[string]int{}
	switch v := v.(type) {
	case []any:
		counts["items"] = len(v)
	case map[string]any:
		for k, list := range v {
			if l, ok := list.([]any); ok {
				counts[k] = len(l)
			}
		}
	}
	if len(counts) == 0 {
		return nil
	}
	return counts
}

// recordDetail is best effort: a lost detail never fails or changes the call.
func (g *Guard) recordDetail(ctx context.Context, id int64, d store.CallDetail) {
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := g.opts.Store.RecordCallDetail(finalCtx, id, d); err != nil {
		g.event(ctx, logging.AuditRecorded, "error", "detail_update_failed", "")
	}
}

// recheck runs the content rules on the writer's texts in place of the agent's.
func (g *Guard) recheck(a Attempt, texts []string) (reason, detail string) {
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(a.Arguments), &object) != nil {
		return "invalid_tool_arguments", ""
	}
	object["text"], _ = json.Marshal(texts[0])
	object["parts"], _ = json.Marshal(texts[1:])
	raw, _ := json.Marshal(object)
	checked, why := g.content(Attempt{Tool: a.Tool, Arguments: string(raw)}, false)
	if checked != nil {
		detail = checked.detail
	}
	return why, detail
}

// Phase identities are harness-owned, never model arguments.
const (
	PhaseAgent = "agent"
	PhaseDream = "dream"
	PhaseStudy = "study"
)

// phaseBlock is the hard phase boundary: a tool runs only in the phases its
// spec lists, so dream and study cannot write to Threads, message the owner or
// change the schedule. It is enforced here, not by prompting.
func (g *Guard) phaseBlock(tool string) string {
	if spec, ok := Spec(tool); ok && spec.AllowedIn(g.opts.Phase) {
		return ""
	}
	return "phase_read_only"
}

// apiQuota refuses a write the Threads API's own publishing quota would
// reject, and fails closed when the quota cannot be read.
func (g *Guard) apiQuota(ctx context.Context, tool string, laterParts int) string {
	if g.opts.APIQuota == nil {
		return ""
	}
	left, err := g.opts.APIQuota(ctx)
	if err != nil {
		return "api_quota_unreadable"
	}
	// Each later part of a series is a reply to the API.
	if (tool == "post" && left.Posts == 0) || (tool == "reply" && left.Replies == 0) || (left.Replies >= 0 && left.Replies < int64(laterParts)) {
		return "api_quota_exhausted"
	}
	return ""
}
