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

func (*stallStop) Error() string           { return "wake stopped: repeated identical tool call" }
func (*stallStop) TerminalToolError() bool { return true }

// Repeat policy: a call that failed the same way repeatFailures times is
// refused; the wake ends at the stallRefusals-th refusal or when one call
// returns the same successful result stallSameResults times.
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
	webSearches  int
	topicReads   int
}

// MaxTopicReadsPerWake bounds read_topic in a normal wake: each opens a page in
// the wake's one browser.
const MaxTopicReadsPerWake = 3

// MaxWebSearchesPerWake bounds web_search in a normal wake; each call also
// counts against the daily budget.
const MaxWebSearchesPerWake = 3

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
	d := &Dispatcher{guard: g, opts: opts, registry: map[string]executor{}, seen: map[[32]byte]seenCall{}}
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
// OpenTargetScore is the target score from which a post read this wake counts
// as worth answering when the agent wants to stop.
const OpenTargetScore = 0.5

// OpenTargets lists the refs of posts read this wake that score at least
// OpenTargetScore and that the account has not replied to in the last day,
// best first. The agent decides which deserve a reply; code only shows them.
func (d *Dispatcher) OpenTargets(ctx context.Context) []string {
	replied, err := d.opts.Store.RepliedSince(ctx, d.opts.Template.ID, d.opts.Template.DryRun, d.opts.Now().Add(-24*time.Hour))
	if err != nil {
		return nil
	}
	var refs []string
	for _, it := range d.guard.Seen() {
		if it.Score >= OpenTargetScore && strings.Contains(it.URL, "/post/") && !replied[it.URL] {
			refs = append(refs, it.Ref)
		}
	}
	return refs
}

func (d *Dispatcher) Definitions() []llm.Tool {
	definitions := []llm.Tool{}
	for _, spec := range guard.Specs() {
		if _, ok := d.registry[spec.Name]; ok && !spec.Harness && spec.AllowedIn(d.opts.Phase) && !slices.Contains(d.opts.Hidden, spec.Name) {
			definitions = append(definitions, definition(spec))
		}
	}
	return definitions
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
	if harness, _ := ctx.Value(harnessKey{}).(bool); known && spec.Harness && !harness {
		known = false
	}
	safeTool := "unknown"
	if known {
		safeTool = spec.Name
	}
	d.event(ctx, logging.ToolStarted, "started", "", safeTool, started)
	key := canonicalCall(call)
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
		} else if errors.As(d.stopped, new(*browserStop)) {
			block = "browser_stopped"
		} else {
			block = "audit_failure"
		}
	} else if prior := d.seen[key]; !prior.ok && prior.streak >= repeatFailures {
		block = "repeat_refused"
		if d.refusals++; d.refusals >= stallRefusals {
			d.stopped = ErrStallStop
		}
	}
	var reservedID int64
	var final []string
	var target string
	output, err := d.guard.Execute(ctx, guard.Attempt{Tool: safeTool, Arguments: call.Function.Arguments, Logged: d.redactedArgs(call.Function.Arguments, block == "sensitive_arguments"), BlockedBy: block, ReservedActionID: &reservedID, Final: &final, Target: &target}, func() (string, error) {
		args.ReservedActionID = reservedID
		if target != "" {
			args.Args = args.Args.WithText("target_url", target)
		}
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
	if block != "repeat_refused" {
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
	return output, err
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
