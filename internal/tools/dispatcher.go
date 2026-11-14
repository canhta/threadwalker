package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/stdx"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/trends"
)

// Owner enqueues private owner messages; implementations must never wait for an answer.
type Owner interface {
	QueueOwner(context.Context, string, string, bool) (int64, error)
}

type Options struct {
	// Scope is the one statement of whose wake this is; the dispatcher builds
	// its guard from it.
	guard.Scope
	Threads     threads.Backend
	Redactor    *config.Redactor
	Owner       Owner
	FetchTrends func(context.Context) ([]trends.Trend, error)
	// SearchWeb is required when the template enables web_search: the
	// dispatcher never builds a default that could reach the network.
	SearchWeb trends.WebSearcher
	// Wait spaces the parts of a series; nil sleeps for real.
	Wait func(context.Context, time.Duration) error
	// EvidenceDir holds the page snapshots of failed browser steps, one folder
	// per agent; empty saves none.
	EvidenceDir string
	// Hidden names tools left out of Definitions, such as reads the Meta app may not use.
	Hidden []string
}
type stallStop struct{}

func (*stallStop) Error() string {
	return "wake stopped: the same tool call kept failing or returning the same result"
}
func (*stallStop) TerminalToolError() bool { return true }

// ErrNothingToDo marks a wake ended because post and reply are both closed:
// a model call could only read. It is not a failure.
var ErrNothingToDo = &nothingToDoStop{}

type nothingToDoStop struct{}

func (*nothingToDoStop) Error() string           { return "wake stopped: no write tool is left open" }
func (*nothingToDoStop) TerminalToolError() bool { return true }

type callCapStop struct{}

func (*callCapStop) Error() string           { return "wake stopped: tool call cap reached" }
func (*callCapStop) TerminalToolError() bool { return true }

// Repeat policy: a call that failed the same way repeatFailures times is
// refused without running; the wake ends at the stallRefusals-th such refusal
// or when one call returns the same successful result stallSameResults times.
const (
	repeatFailures   = 3
	stallRefusals    = 2
	stallSameResults = 10
)

type seenCall struct {
	ok     bool
	answer [32]byte
	streak int
}

// ErrToolCallCap marks a wake ended because the model kept calling tools past MaxToolCallsPerWake.
var ErrToolCallCap = &callCapStop{}

// ErrStallStop marks a wake ended for repeating itself. errors.Is works through the client's safe terminal-error wrapper.
var ErrStallStop = &stallStop{}

type Dispatcher struct {
	mu           sync.Mutex
	guard        *guard.Guard
	opts         Options
	registry     map[string]executor
	seen         map[[32]byte]seenCall
	refusals     int
	stopped      error
	redactor     *config.Redactor
	trendProfile trends.TrendProfile
	reads        map[string]int
	refused      map[string]int // refusals per call scope and reason
	closed       map[string]bool
	started      time.Time
	calls        int // model tool calls this wake, refused ones included
	capRefusals  int
}

// MaxWakeSpan is the longest a wake is expected to run. A gate that opens
// inside it is worth keeping the tool for; one that stays shut past it is not.
// Limit: a wake has no deadline of its own; derive this from one if it gets one.
const MaxWakeSpan = 30 * time.Minute

// MaxTopicReadsPerWake bounds find_posts source topic in a normal wake: each
// opens a page in the wake's one browser.
const MaxTopicReadsPerWake = 3

// MaxWebSearchesPerWake bounds web_search in a normal wake; each call also
// counts against the daily budget.
const MaxWebSearchesPerWake = 3

// MaxFeedReadsPerWake and MaxTrendFetchesPerWake: the home feed (find_posts
// source feed) and the trend list are snapshots, so a second look in the same wake is retry room at most.
const (
	MaxFeedReadsPerWake    = 2
	MaxTrendFetchesPerWake = 1
)

// MaxToolCallsPerWake bounds the tool calls of a normal wake: steps are model
// requests, and one request may return several calls. The first call past it is
// refused; the next ends the wake.
const MaxToolCallsPerWake = 40

// MaxResultBytes clips every tool result the model reads.
const MaxResultBytes = 6 << 10

// wakeCaps are the per-wake call caps of the tools that have one: paid or
// slow reads, and the tools that reach the owner or add a permanent record.
var wakeCaps = map[string]int{
	"find_posts:topic":    MaxTopicReadsPerWake,
	"web_search":          MaxWebSearchesPerWake,
	"find_posts:feed":     MaxFeedReadsPerWake,
	"fetch_trends":        MaxTrendFetchesPerWake,
	"notify_owner":        2,
	"ask_owner":           1,
	"mark_do_not_contact": 3,
	"ideas":               3,
}

// callScope is what a refusal counter, a close and a hide apply to: the tool,
// or for find_posts the tool and its source, so a closed feed leaves topic open.
func callScope(tool, arguments string) string {
	if tool != "find_posts" {
		return tool
	}
	var a struct {
		Source string `json:"source"`
	}
	_ = json.Unmarshal([]byte(arguments), &a)
	if a.Source == "" {
		return tool
	}
	return guard.FindScope(a.Source)
}

// countCall counts one call of a capped tool in a normal wake and refuses
// the one past its cap. Dispatch already holds d.mu.
func (d *Dispatcher) countCall(name string) error {
	limit, capped := wakeCaps[name]
	if d.opts.Phase != guard.PhaseAgent || !capped {
		return nil
	}
	if d.reads[name]++; d.reads[name] > limit {
		return &guard.BlockedError{Reason: "limit_per_wake", Detail: name}
	}
	return nil
}

// A tool refused the third time for the same reason is closed for the wake.
// The second refusal also names the next valid action.
const closeAtRefusals = 3

// uncounted are refusals the dispatcher itself raises about the wake or the
// repeat, not about what the model asked for, and browser_replies_per_day: it
// shuts only the browser reply path, never the reply tool, whose API replies stay allowed.
var uncounted = []string{"tool_closed", "tool_hidden", "tool_call_cap", "repeat_refused", "stall_stop", "browser_stopped", "audit_failure", "browser_replies_per_day"}

// timeGated refusals lift at a time the refusal names: they close a tool only
// when that is past the wake's horizon, never by counting.
var timeGated = []string{"min_minutes_between_posts", "replies_per_hour", "min_seconds_between_actions", "reply_breaker"}

// perTarget refusals are about one post or person: another target starts a new count.
var perTarget = []string{"unknown_target", "target_outside_window", "thread_per_day", "replies_per_user_7d", "replies_per_conversation", "stale_ref", "do_not_contact", "own_account", "reply_target_host"}

// countRefusal tallies a refusal by (scope, reason), or (scope, reason, target)
// for a per-target reason, annotating it on the 2nd and closing the scope on
// the 3rd.
func (d *Dispatcher) countRefusal(tool, arguments string, err error) {
	var blocked *guard.BlockedError
	if !errors.As(err, &blocked) || slices.Contains(uncounted, blocked.Reason) {
		return
	}
	scope := callScope(tool, arguments)
	if slices.Contains(timeGated, blocked.Reason) {
		if until, perr := time.Parse(time.RFC3339, blocked.Detail); perr == nil && until.After(d.started.Add(MaxWakeSpan)) {
			d.closed[scope] = true
			blocked.Next = "this tool is closed for the rest of the wake; it reopens at " + blocked.Detail
		}
		return
	}
	if blocked.Reason == "api_quota_exhausted" { // a daily window: it does not reopen today
		d.closed[scope] = true
		blocked.Next = "this tool is closed for the rest of the wake"
		return
	}
	key := scope + "\x00" + blocked.Reason
	if slices.Contains(perTarget, blocked.Reason) {
		var a struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal([]byte(arguments), &a)
		key += "\x00" + a.Target
	}
	switch d.refused[key]++; {
	case d.refused[key] == 2:
		blocked.Next = guard.NextAction(blocked.Reason)
	case d.refused[key] >= closeAtRefusals:
		d.closed[scope] = true
		blocked.Next = "this tool is closed for the rest of the wake"
	}
}

func NewDispatcher(opts Options) (*Dispatcher, error) {
	if opts.Phase == "" {
		opts.Phase = guard.PhaseAgent
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Wait == nil {
		opts.Wait = stdx.Sleep
	}
	if opts.Template.Limits.MinMinutesBetweenWakes < 1 || opts.Template.Limits.MinMinutesBetweenWakes > 1440 {
		return nil, errors.New("configuring tools: valid wake interval required")
	}
	if opts.Threads != nil && !opts.Template.DryRun {
		opts.Scope.APIQuota = publishQuota(opts.Threads)
	}
	g, err := guard.New(opts.Scope)
	if err != nil {
		return nil, fmt.Errorf("configuring tools: %w", err)
	}
	if opts.FetchTrends == nil || (opts.Template.WebSearch.Enabled && opts.SearchWeb == nil) {
		return nil, errors.New("configuring tools: injected trend and web-search fetchers required")
	}
	d := &Dispatcher{started: opts.Now(), guard: g, opts: opts, registry: map[string]executor{}, seen: map[[32]byte]seenCall{}, reads: map[string]int{}, refused: map[string]int{}, closed: map[string]bool{}}
	include, avoid := opts.Template.TrendKeywords()
	d.trendProfile = trends.TrendProfile{Include: include, Avoid: avoid}
	d.redactor = config.NewRedactor(opts.Redactor, opts.Template)
	if err := d.attach(d.executors()); err != nil {
		return nil, err
	}
	return d, nil
}

// attach installs executors, refusing any tool the guard's spec table does not list.
func (d *Dispatcher) attach(executors map[string]executor) error {
	for name, run := range executors {
		if _, ok := guard.Spec(name); !ok {
			return fmt.Errorf("configuring tools: %q is not in the guard tool table", name)
		}
		d.registry[name] = run
	}
	return nil
}

// Definitions lists the tools this wake's phase may call; a tool the guard
// would refuse is never offered.
func (d *Dispatcher) Definitions() []llm.Tool {
	definitions := []llm.Tool{}
	for _, spec := range guard.Specs() {
		if _, ok := d.registry[spec.Name]; ok && !spec.Harness && spec.AllowedIn(d.opts.Phase) && !slices.Contains(d.opts.Hidden, spec.Name) {
			if spec.Name == "find_posts" {
				var open bool
				if spec, open = d.withOpenSources(spec); !open {
					continue
				}
			}
			definitions = append(definitions, definition(spec))
		}
	}
	return definitions
}

// withOpenSources drops the hidden sources from find_posts' source enum; ok is
// false when none is left and the tool is not offered at all.
func (d *Dispatcher) withOpenSources(spec guard.ToolSpec) (guard.ToolSpec, bool) {
	spec.Args = slices.Clone(spec.Args)
	for i, a := range spec.Args {
		if a.Name == "source" {
			spec.Args[i].Enum = slices.DeleteFunc(slices.Clone(a.Enum), func(source string) bool { return slices.Contains(d.opts.Hidden, guard.FindScope(source)) })
			return spec, len(spec.Args[i].Enum) > 0
		}
	}
	return spec, true
}

// Guard is the guard this dispatcher built from its scope, for harness-side reads and stop-request ingestion.
func (d *Dispatcher) Guard() *guard.Guard { return d.guard }

// ConfigureClient installs the dispatcher's redactor on the client.
func (d *Dispatcher) ConfigureClient(base llm.Options) llm.Options {
	base.Redactor = d.redactor
	return base
}

type harnessKey struct{}

// DispatchHarness is Dispatch for the harness's own reads (Spec.Harness): the
// metrics wake and the follower count. A model call goes through Dispatch,
// which refuses them.
func (d *Dispatcher) DispatchHarness(ctx context.Context, call llm.ToolCall) (string, error) {
	return d.Dispatch(context.WithValue(ctx, harnessKey{}, true), call)
}

func (d *Dispatcher) Dispatch(ctx context.Context, call llm.ToolCall) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	started := d.opts.Now()
	run, known := d.registry[call.Function.Name]
	spec, _ := guard.Spec(call.Function.Name)
	harness, _ := ctx.Value(harnessKey{}).(bool)
	if known && spec.Harness && !harness {
		known = false
	}
	if !harness && d.opts.Phase == guard.PhaseAgent {
		d.calls++
	}
	safeTool := "unknown"
	if known {
		safeTool = spec.Name
	}
	d.event(ctx, logging.ToolStarted, "started", "", safeTool, started)
	key := canonicalCall(call)
	scope := callScope(safeTool, call.Function.Arguments)
	block := llm.RejectedReason(ctx)
	var args arguments
	var parseErr error
	switch {
	case call.ID == "":
		block = "invalid_tool_call_id"
	case call.Type != "function":
		block = "unsupported_tool_type"
	case !known:
		block = "unknown_tool"
	default:
		var parsed guard.Args
		parsed, parseErr = spec.Parse(call.Function.Arguments)
		args.Args = parsed
		if parseErr != nil {
			block = "invalid_tool_arguments"
		}
	}
	if block == "" && d.containsSecret(call.Function.Arguments, args.Args) {
		block = "sensitive_arguments"
	}
	if d.stopped != nil {
		if errors.Is(d.stopped, ErrStallStop) {
			block = "stall_stop"
		} else if errors.Is(d.stopped, ErrToolCallCap) {
			block = "tool_call_cap"
		} else if errors.Is(d.stopped, ErrNothingToDo) {
			block = "tool_closed"
		} else if errors.As(d.stopped, new(*browserStop)) {
			block = "browser_stopped"
		} else {
			block = "audit_failure"
		}
	} else if d.calls > MaxToolCallsPerWake {
		block = "tool_call_cap"
	} else if !harness && slices.Contains(d.opts.Hidden, scope) {
		block = "tool_hidden"
	} else if prior := d.seen[key]; !prior.ok && prior.streak >= repeatFailures {
		block = "repeat_refused"
	} else if d.closed[scope] {
		block = "tool_closed"
	}
	// A thrice-failed call is refused without running; the second such
	// refusal ends the wake. The second call past the cap does too.
	if block == "repeat_refused" {
		if d.refusals++; d.refusals >= stallRefusals {
			d.stopped = ErrStallStop
		}
	}
	if block == "tool_call_cap" {
		if d.capRefusals++; d.capRefusals >= 2 {
			d.stopped = ErrToolCallCap
		}
	}
	var reservedID int64
	var final []string
	var target guard.ResolvedTarget
	output, err := d.guard.Execute(ctx, guard.Attempt{Tool: safeTool, Arguments: call.Function.Arguments, Logged: d.redactedArgs(call.Function.Arguments, block == "sensitive_arguments"), BlockedBy: block, ReservedActionID: &reservedID, Final: &final, Target: &target}, func() (string, error) {
		args.ReservedActionID = reservedID
		args.Target = target
		if len(final) > 0 {
			args.Args = args.Args.Rewrite(final)
		}
		data, err := run(ctx, args)
		if err != nil {
			failure := &guard.ReasonedError{Err: modelFacing(err), Code: failureReason(err)}
			if se := threads.StageOf(err); se != nil {
				failure.Stage, failure.Evidence = se.Stage, d.saveEvidence(ctx, safeTool, se)
			}
			return "", failure
		}
		if spec.Untrusted {
			data = struct {
				Untrusted bool   `json:"untrusted"`
				Source    string `json:"source"`
				Data      any    `json:"data"`
			}{true, spec.Name, data}
		}
		result, err := json.Marshal(data)
		if err != nil {
			return "", errors.New("encoding tool result: failed")
		}
		// Decode before redaction so JSON escaping cannot hide a configured
		// credential in outside content. Audit stores only outcome and byte count.
		var safe any
		decoder := json.NewDecoder(strings.NewReader(string(result)))
		decoder.UseNumber()
		if decoder.Decode(&safe) != nil {
			return "", errors.New("encoding tool result: failed")
		}
		result, err = json.Marshal(d.redact(safe))
		if err != nil {
			return "", errors.New("encoding tool result: failed")
		}
		return string(result), nil
	})
	if errors.Is(err, guard.ErrAudit) {
		d.stopped = err
	}
	d.countRefusal(safeTool, call.Function.Arguments, err)
	var gated *guard.BlockedError
	if block != "repeat_refused" && !(errors.As(err, &gated) && slices.Contains(timeGated, gated.Reason)) {
		answer := sha256.Sum256([]byte(output))
		if err != nil {
			answer = sha256.Sum256([]byte(err.Error()))
		}
		prior := d.seen[key]
		next := seenCall{ok: err == nil, answer: answer, streak: 1}
		if prior.ok == next.ok && prior.answer == answer {
			next.streak = prior.streak + 1
		}
		d.seen[key] = next
		if next.ok && next.streak >= stallSameResults {
			d.stopped = ErrStallStop
		}
	}
	if d.stopped == nil && !harness && d.opts.Phase == guard.PhaseAgent && d.isClosed("post") && d.isClosed("reply") {
		d.stopped = ErrNothingToDo
	}
	if d.stopped != nil {
		err = d.stopped
	}
	why := "completed"
	if err != nil {
		why = failureReason(err)
		if block != "" {
			why = block
		}
		if errors.Is(err, guard.ErrAudit) {
			why = "audit_failure"
		}
	}
	d.event(ctx, logging.ToolFinished, outcome(err), why, safeTool, started)
	if !harness && err == nil {
		output = clipResult(output, MaxResultBytes)
	}
	return output, err
}

// isClosed: the tool is hidden or closed for the rest of the wake. Dispatch holds d.mu.
func (d *Dispatcher) isClosed(tool string) bool {
	return d.closed[tool] || slices.Contains(d.opts.Hidden, tool)
}

// Closed is isClosed for the stop check, which must not nudge toward a closed tool.
func (d *Dispatcher) Closed(tool string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.isClosed(tool)
}

func (d *Dispatcher) containsSecret(raw string, a guard.Args) bool {
	for _, value := range append(a.Texts(), raw) {
		if d.redactor.Contains(value) {
			return true
		}
	}
	return false
}
func canonicalCall(call llm.ToolCall) [32]byte {
	var value any
	decoder := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	decoder.UseNumber()
	args := strings.TrimSpace(call.Function.Arguments)
	// Unmarshal first rejects trailing values, then UseNumber preserves integer
	// precision while Marshal recursively sorts object keys.
	if json.Valid([]byte(args)) && decoder.Decode(&value) == nil {
		if raw, err := json.Marshal(value); err == nil {
			args = string(raw)
		}
	}
	return sha256.Sum256([]byte(call.Function.Name + "\x00" + args))
}
func (d *Dispatcher) event(ctx context.Context, event, outcome, reason, tool string, started time.Time) {
	if tool == "unknown" {
		tool = ""
	}
	duration := int64(0)
	if !started.IsZero() {
		duration = d.opts.Now().Sub(started).Milliseconds()
	}
	logging.Emit(ctx, d.opts.Logger, slog.LevelInfo, logging.Record{Event: event, AgentID: d.opts.Template.ID, Phase: d.opts.Phase, WakeID: d.opts.WakeID, Tool: tool, Operation: "tool", Outcome: outcome, Reason: reason, DurationMS: duration})
}

var _ llm.Dispatcher = (*Dispatcher)(nil)
var _ error = ErrStallStop

// publishQuota maps the API's publishing limit to what is left in its window.
func publishQuota(b threads.Backend) func(context.Context) (guard.APIQuota, error) {
	return func(ctx context.Context) (guard.APIQuota, error) {
		q, err := b.PublishingLimit(ctx)
		if err != nil {
			return guard.APIQuota{}, err
		}
		left := func(used, total int64) int64 {
			if total <= 0 {
				return -1
			}
			return max(total-used, 0)
		}
		return guard.APIQuota{Posts: left(q.PostsUsed, q.PostsTotal), Replies: left(q.RepliesUsed, q.RepliesTotal)}, nil
	}
}
