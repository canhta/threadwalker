package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/guard"
	"github.com/canhta/threadwalker/internal/llm"
	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/store"
	"github.com/canhta/threadwalker/internal/threads"
)

// Owner enqueues private owner messages; implementations must never wait for an answer.
type Owner interface {
	QueueOwner(context.Context, string, string, bool) (int64, error)
}

type Options struct {
	Threads     threads.Backend
	AllowLive   bool // Trusted harness capability; never exposed in a tool schema.
	Secrets     []config.Secret
	Owner       Owner
	Store       *store.Store
	Template    config.Template
	WakeID      int64
	Now         func() time.Time
	Logger      *slog.Logger
	FetchTrends func(context.Context) ([]Trend, error)
}
type stallStop struct{}

func (*stallStop) Error() string           { return "wake stopped: repeated identical tool call" }
func (*stallStop) TerminalToolError() bool { return true }

// ErrStallStop marks the third canonical identical tool+argument attempt.
// errors.Is works through the client's safe terminal-error wrapper.
var ErrStallStop = &stallStop{}

type Dispatcher struct {
	mu       sync.Mutex
	guard    *guard.Guard
	opts     Options
	registry map[string]builtin
	order    []string
	attempts map[[32]byte]int
	stopped  error
	nextWake time.Time
	secrets  []string
}

func NewDispatcher(g *guard.Guard, opts Options) (*Dispatcher, error) {
	if !g.Matches(opts.Store, opts.Template.ID, opts.WakeID) || !g.MatchesMode(opts.Template.DryRun) || (!opts.Template.DryRun && (!opts.AllowLive || opts.Threads == nil)) || opts.Template.Limits.MinMinutesBetweenWakes < 1 || opts.Template.Limits.MinMinutesBetweenWakes > 1440 {
		return nil, errors.New("configuring tools: matching guard scope, action capability and valid wake interval required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.FetchTrends == nil {
		opts.FetchTrends = func(ctx context.Context) ([]Trend, error) { return FetchTrends(ctx, TrendsOptions{}) }
	}
	d := &Dispatcher{guard: g, opts: opts, registry: map[string]builtin{}, attempts: map[[32]byte]int{}}
	secrets := append([]config.Secret{}, opts.Secrets...)
	secrets = append(secrets, opts.Template.LLM.APIKey, opts.Template.Telegram.BotToken, opts.Template.Telegram.OwnerChatID)
	for _, secret := range secrets {
		if raw := secret.Expose(); raw != "" {
			d.secrets = append(d.secrets, raw)
		}
	}
	for _, tool := range d.builtins() {
		d.registry[tool.name] = tool
		d.order = append(d.order, tool.name)
	}
	return d, nil
}
func (d *Dispatcher) Definitions() []llm.Tool {
	definitions := make([]llm.Tool, 0, len(d.order))
	for _, name := range d.order {
		definitions = append(definitions, d.registry[name].Definition())
	}
	return definitions
}

// ConfigureClient installs the audit route for malformed/unknown attempts which
// the client rejects before Dispatch. Valid attempts enter the same route from
// Dispatch. It replaces any earlier rejected-call hook to keep one audit owner.
func (d *Dispatcher) ConfigureClient(base llm.Options) llm.Options {
	base.RejectedToolCall = func(ctx context.Context, call llm.ToolCall, reason string) error {
		_, err := d.dispatch(ctx, call, safeRejection(reason))
		return err
	}
	return base
}

// NextWake reports whether this wake successfully scheduled itself. The runtime
// uses its 60-minute default only when this reports false.
func (d *Dispatcher) NextWake() (time.Time, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.nextWake, !d.nextWake.IsZero()
}
func (d *Dispatcher) Dispatch(ctx context.Context, call llm.ToolCall) (string, error) {
	return d.dispatch(ctx, call, "")
}
func (d *Dispatcher) dispatch(ctx context.Context, call llm.ToolCall, rejected string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	started := d.opts.Now()
	tool, known := d.registry[call.Function.Name]
	safeTool := "unknown"
	if known {
		safeTool = tool.name
	}
	d.event(ctx, "tool.started", "started", "", safeTool, started)
	key := canonicalCall(call)
	d.attempts[key]++
	block := rejected
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
		args, parseErr = tool.parse(call.Function.Arguments)
		if parseErr != nil {
			block = "invalid_tool_arguments"
		}
	}
	if block == "" && d.containsSecret(call.Function.Arguments, args) {
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
	} else if d.attempts[key] >= 3 {
		block = "stall_stop"
		d.stopped = ErrStallStop
	}
	var reservedID int64
	output, err := d.guard.Execute(ctx, guard.Attempt{Tool: safeTool, Arguments: call.Function.Arguments, BlockedBy: block, ReservedActionID: &reservedID}, func() (string, error) {
		args.ReservedActionID = reservedID
		data, err := tool.execute(ctx, args)
		if err != nil {
			return "", err
		}
		if tool.external() {
			data = struct {
				Untrusted bool   `json:"untrusted"`
				Source    string `json:"source"`
				Data      any    `json:"data"`
			}{true, tool.name, data}
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
	if d.stopped != nil {
		err = d.stopped
	}
	why := "completed"
	if err != nil {
		why = "operation_failed"
		if block != "" {
			why = block
		}
		if errors.Is(err, guard.ErrAudit) {
			why = "audit_failure"
		}
	}
	d.event(ctx, "tool.finished", outcome(err), why, safeTool, started)
	return output, err
}
func (d *Dispatcher) containsSecret(raw string, a arguments) bool {
	for _, secret := range d.secrets {
		if strings.Contains(raw, secret) {
			return true
		}
		for _, value := range []string{a.Text, a.Query, a.At, a.URL, a.TargetURL, a.TargetUsername, a.Username} {
			if strings.Contains(value, secret) {
				return true
			}
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
func safeRejection(reason string) string {
	switch reason {
	case "unsupported_tool_type", "unknown_tool", "invalid_tool_arguments", "invalid_tool_call_id", "incomplete_tool_calls", "invalid_response":
		return reason
	default:
		return "invalid_tool_arguments"
	}
}
func (d *Dispatcher) event(ctx context.Context, event, outcome, reason, tool string, started time.Time) {
	if tool == "unknown" {
		tool = ""
	}
	duration := int64(0)
	if !started.IsZero() {
		duration = d.opts.Now().Sub(started).Milliseconds()
	}
	logging.Emit(ctx, d.opts.Logger, slog.LevelInfo, logging.Record{Event: event, AgentID: d.opts.Template.ID, WakeID: d.opts.WakeID, Tool: tool, Operation: "tool", Outcome: outcome, Reason: reason, DurationMS: duration})
}

var _ llm.Dispatcher = (*Dispatcher)(nil)
var _ Tool = builtin{}
var _ error = ErrStallStop
