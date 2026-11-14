// Package llm implements a bounded tool loop over configured LLM protocols.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/canhta/threadz/internal/stdx"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/store"
)

type Function struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict,omitempty"`
}
type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}
type CallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function CallFunction `json:"function"`
}
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolError  bool       `json:"-"`
	// thinking holds the provider's signed reasoning blocks, which the next
	// request of the same turn must echo unchanged. Unexported and untagged so
	// it never reaches another protocol, a log, or a stored transcript.
	thinking []json.RawMessage
}

// Dispatcher receives every tool call the model makes and alone validates and
// audits it; the client forwards calls from a malformed response with
// RejectedReason set, and the dispatcher must refuse those. This package
// provides no tool implementations or registry; tests use fake dispatchers.
type Dispatcher interface {
	Dispatch(context.Context, ToolCall) (string, error)
}
type DispatchFunc func(context.Context, ToolCall) (string, error)

func (f DispatchFunc) Dispatch(ctx context.Context, call ToolCall) (string, error) {
	return f(ctx, call)
}

// Usage aliases the transactional store accounting contract.
type Usage = store.LLMUsage
type Ledger interface {
	LLMUsageForDay(context.Context, string, string) (Usage, error)
	RecordLLMUsage(context.Context, string, int64, string, Usage) error
}

var ErrStepLimit = errors.New("wake step limit reached")
var ErrBudget = errors.New("daily LLM budget spent")

type Options struct {
	Logger *slog.Logger
	// Redactor scrubs messages before they are stored or logged; the client's
	// own API key is always included.
	Redactor *config.Redactor
	// Transcript receives the redacted record of the run as it happens: the
	// context, each model turn, each tool result and each request's usage. A
	// failed write must not stop the run, so the hook returns nothing.
	Transcript func(ctx context.Context, kind, body string)
	ProviderID string
	ProfileID  string
	// Phase selects phase-scoped accounting: agent (default), study or dream.
	// Dream spend is measured against CapUSD, not limits.MaxUSDPerDay.
	Phase string
	// CapUSD overrides the spend cap for this phase. Zero uses
	// limits.MaxUSDPerDay (agent/study) or requires a positive value for dream.
	CapUSD float64
	// MaxSteps overrides limits.MaxStepsPerWake when positive.
	MaxSteps int
	// StopCheck runs when the model stops calling tools. It gets the tools called
	// so far and how many calls of each succeeded; a non-empty return is sent to
	// the model as an extra turn, at most MaxStopNudges times per run, together
	// with the note reminder.
	StopCheck func(called map[string]bool, succeeded map[string]int) string
	// Zero omits provider-specific output/reasoning options.
	MaxCompletionTokens int
	ReasoningEffort     string
	HTTPClient          *http.Client
	Now                 func() time.Time
	Sleep               func(context.Context, time.Duration) error
	// Jitter returns a value in [0,1) that stretches each backoff wait by up
	// to a quarter; nil uses math/rand.
	Jitter         func() float64
	RequestTimeout time.Duration
}
type Client struct {
	logger                *slog.Logger
	providerID, profileID string
	config                config.LLMConfig
	redactor              *config.Redactor
	limits                config.LimitsConfig
	http                  *http.Client
	now                   func() time.Time
	timeout               time.Duration
	ledger                Ledger
	sleep                 func(context.Context, time.Duration) error
	jitter                func() float64
	outputLimit           int
	reasoning             string
	adapter               protocolAdapter
	phase                 string
	capUSD                float64
	maxSteps              int
	stopCheck             func(called map[string]bool, succeeded map[string]int) string
	transcript            func(ctx context.Context, kind, body string)
}

// Result contains the completed messages and steps consumed before stopping.
// Each tool call (including a malformed call) consumes a step; a final
// assistant response without calls consumes one step. HTTP retries do not.
type Result struct {
	Content  string
	Steps    int
	Messages []Message
}

// New constructs a client with a 30-second request timeout unless overridden.
func New(cfg config.LLMConfig, limits config.LimitsConfig, ledger Ledger, opts Options) (*Client, error) {
	adapter, err := adapterFor(cfg.Protocol)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("configuring LLM client: invalid base URL")
	}
	if strings.TrimSpace(cfg.Model) == "" || cfg.APIKey.Expose() == "" {
		return nil, errors.New("configuring LLM client: model and API key are required")
	}
	if !stdx.FiniteNonNegative(cfg.PricePerMTokInUSD) || !stdx.FiniteNonNegative(cfg.PricePerMTokOutUSD) || (cfg.PricePerMTokCachedInUSD != nil && !stdx.FiniteNonNegative(*cfg.PricePerMTokCachedInUSD)) || !stdx.FiniteNonNegative(limits.MaxUSDPerDay) || limits.MaxUSDPerDay == 0 || limits.MaxStepsPerWake <= 0 {
		return nil, errors.New("configuring LLM client: invalid prices or limits")
	}
	phase, capUSD, err := resolvePhase(opts, limits)
	if err != nil {
		return nil, err
	}
	maxSteps := opts.MaxSteps
	if maxSteps == 0 {
		maxSteps = limits.MaxStepsPerWake
	}
	if maxSteps <= 0 {
		return nil, errors.New("configuring LLM client: invalid step limit")
	}
	if ledger == nil || opts.RequestTimeout < 0 || opts.MaxCompletionTokens < 0 {
		return nil, errors.New("configuring LLM client: ledger and nonnegative timeout required")
	}

	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{}
	}
	// Redirects would be additional requests outside budget/retry accounting.
	httpClient := *opts.HTTPClient
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	opts.HTTPClient = &httpClient
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Sleep == nil {
		opts.Sleep = stdx.Sleep
	}
	if opts.Jitter == nil {
		opts.Jitter = rand.Float64
	}
	if opts.RequestTimeout == 0 {
		opts.RequestTimeout = 30 * time.Second
	}
	if cfg.Protocol == "" {
		cfg.Protocol = "openai"
	}
	return &Client{logger: opts.Logger, providerID: opts.ProviderID, profileID: opts.ProfileID, config: cfg, redactor: config.NewRedactor(cfg, opts.Redactor), limits: limits, http: opts.HTTPClient, now: opts.Now, timeout: opts.RequestTimeout, ledger: ledger, sleep: opts.Sleep, jitter: opts.Jitter, outputLimit: opts.MaxCompletionTokens, reasoning: opts.ReasoningEffort, adapter: adapter, phase: phase, capUSD: capUSD, maxSteps: maxSteps, stopCheck: opts.StopCheck, transcript: opts.Transcript}, nil
}

// MaxStopNudges bounds the extra turns a run gets when it stops early.
const MaxStopNudges = 1

// completion is a provider response decoded by its adapter. raw holds the
// provider's bytes for the audit of a refused response; they are never re-encoded.
type completion struct {
	Usage *struct {
		PromptTokens     *int64 `json:"prompt_tokens"`
		CompletionTokens *int64 `json:"completion_tokens"`
	}
	// cached is the cached share of the prompt tokens, priced at the cached rate.
	cached       int64
	Message      Message
	FinishReason string
	// assistant is true when the response carried exactly one assistant message.
	assistant bool
	// MessageError rejects a response the adapter could not normalize;
	// choicesError rejects one whose choices were not decodable.
	MessageError error
	choicesError error
	raw          json.RawMessage
	thinking     []json.RawMessage
	usdLeft      float64
}

// Run performs one wake's bounded conversation. The caller owns wake creation,
// tool auditing and guard wrapping; an error must not trigger wake replay.
func (c *Client) Run(ctx context.Context, agentID string, wakeID int64, messages []Message, tools []Tool, dispatcher Dispatcher) (result Result, runErr error) {
	started := c.now()
	requestIndex := 0
	c.event(ctx, slog.LevelInfo, logging.LLMRunStarted, agentID, wakeID, 0, 0, started, "started", "", Usage{}, 0, 0)
	defer func() {
		event, level, outcome, reason := logging.LLMRunFinished, slog.LevelInfo, "success", "completed"
		if runErr != nil {
			event, level, outcome, reason = logging.LLMRunStopped, slog.LevelError, "error", errorReason(runErr)
		}
		c.event(ctx, level, event, agentID, wakeID, requestIndex, 0, started, outcome, reason, Usage{}, 0, result.Steps)
	}()
	if len(tools) > 0 && dispatcher == nil {
		return Result{}, failure("running chat loop", "missing_dispatcher", nil)
	}
	known := make(map[string]bool, len(tools))
	for _, tool := range tools {
		known[tool.Function.Name] = true
	}
	result = Result{Messages: make([]Message, len(messages))}
	for i, message := range messages {
		result.Messages[i] = c.redactMessage(message)
	}
	c.recordContext(ctx, result.Messages)
	nudges := 0
	called, succeeded := map[string]bool{}, map[string]int{}
	for result.Steps < c.maxSteps {
		requestIndex++
		body, err := c.adapter.encode(c.config.Model, result.Messages, tools, c.outputLimit, c.reasoning, c.cacheKey(agentID))
		if err != nil {
			return result, failure("encoding chat request", "invalid_request", err)
		}
		response, err := c.request(ctx, agentID, wakeID, requestIndex, body)
		if err != nil {
			return result, err
		}
		if response.MessageError != nil {
			if err := c.rejectResponse(ctx, dispatcher, response.raw, "invalid_response"); isTerminalToolError(err) {
				return result, terminalToolFailure(err)
			}
			return result, failure("reading chat response", "invalid_response", response.MessageError)
		}
		if response.choicesError != nil {
			if hookErr := c.rejectResponse(ctx, dispatcher, response.raw, "invalid_response"); isTerminalToolError(hookErr) {
				return result, terminalToolFailure(hookErr)
			}
			return result, failure("decoding chat choices", "malformed_choices", response.choicesError)
		}
		if !response.assistant {
			if err := c.rejectResponse(ctx, dispatcher, response.raw, "invalid_response"); isTerminalToolError(err) {
				return result, terminalToolFailure(err)
			}
			return result, failure("reading chat response", "invalid_choice", nil)
		}
		message := c.redactMessage(response.Message)
		message.thinking = response.thinking
		if len(message.ToolCalls) > 0 && response.FinishReason != "" && response.FinishReason != "tool_calls" {
			if err := c.rejectCalls(ctx, dispatcher, message.ToolCalls, "incomplete_tool_calls"); isTerminalToolError(err) {
				return result, terminalToolFailure(err)
			}
			return result, failure("reading chat response", "incomplete_tool_calls", nil)
		}
		ids := make(map[string]bool, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			if call.ID == "" || ids[call.ID] {
				if err := c.rejectCalls(ctx, dispatcher, message.ToolCalls, "invalid_tool_call_id"); isTerminalToolError(err) {
					return result, terminalToolFailure(err)
				}
				return result, failure("reading chat response", "invalid_tool_call_id", nil)
			}
			ids[call.ID] = true
		}
		result.Messages = append(result.Messages, message)
		c.recordAssistant(ctx, requestIndex, message)
		if len(message.ToolCalls) == 0 {
			result.Steps++
			if response.FinishReason != "stop" {
				return result, failure("reading chat response", "incomplete_response", nil)
			}
			if nudges < MaxStopNudges && c.stopCheck != nil {
				if ask := c.stopCheck(called, succeeded); ask != "" {
					nudges++
					nudge := Message{Role: "user", Content: ask}
					result.Messages = append(result.Messages, nudge)
					c.recordUser(ctx, nudge)
					continue
				}
			}
			result.Content = message.Content
			return result, nil
		}
		if dispatcher == nil {
			return result, failure("running chat loop", "missing_dispatcher", nil)
		}
		for _, call := range message.ToolCalls {
			if err := ctx.Err(); err != nil {
				return result, fmt.Errorf("dispatching tool call: %w", err)
			}
			if result.Steps >= c.maxSteps {
				return result, fmt.Errorf("running chat loop: %w", ErrStepLimit)
			}
			result.Steps++
			toolStarted := c.now()
			outcome, reason := "success", "completed"
			safeTool := ""
			var output string
			var toolErr error
			toolError := true
			if known[call.Function.Name] {
				safeTool = call.Function.Name
			}
			output, toolErr = dispatcher.Dispatch(ctx, call)
			toolError = toolErr != nil
			if toolErr != nil {
				outcome, reason = "error", "dispatch_failed"
				output = `{"error":"tool dispatcher rejected or failed the call"}`
				var blocked interface{ ModelResult() string }
				if errors.As(toolErr, &blocked) {
					output = blocked.ModelResult()
				}
			}
			level := slog.LevelInfo
			if outcome == "error" {
				level = slog.LevelError
			}
			logging.Emit(ctx, c.logger, level, logging.Record{Event: logging.LLMToolFinished, AgentID: agentID, Phase: c.phase, WakeID: wakeID, Request: requestIndex, ToolCallID: fmt.Sprintf("%d/%d", wakeID, result.Steps), Tool: safeTool, ProviderID: c.providerID, ProfileID: c.profileID, Model: c.config.Model, Protocol: c.config.Protocol, Operation: "dispatch", Outcome: outcome, Reason: reason, DurationMS: c.now().Sub(toolStarted).Milliseconds(), Steps: result.Steps})
			called[safeTool] = true
			if toolErr == nil {
				succeeded[safeTool]++
			}
			trailer, _ := json.Marshal(struct {
				StepsLeft int     `json:"steps_left"`
				USDLeft   float64 `json:"usd_left"`
			}{c.maxSteps - result.Steps, response.usdLeft})
			result.Messages = append(result.Messages, c.redactMessage(Message{Role: "tool", Content: output + "\n" + string(trailer), ToolCallID: call.ID, ToolError: toolError}))
			c.recordToolResult(ctx, requestIndex, call.Function.Name, c.redactMessage(Message{Role: "tool", Content: output, ToolCallID: call.ID, ToolError: toolError}), reason)
			if isTerminalToolError(toolErr) {
				return result, terminalToolFailure(toolErr)
			}
		}
	}
	return result, fmt.Errorf("running chat loop: %w", ErrStepLimit)
}

// maxRetryAfter caps how long a provider's Retry-After can stall a wake.
const maxRetryAfter = 60 * time.Second

// Requests retry explicit 429/5xx responses, honouring Retry-After, and a
// request that failed in transport or timed out (once). A chat completion has
// no side effect besides cost, so the one transport retry can at worst pay for
// the same request twice; publishing is guarded by the dispatcher, not here.
func (c *Client) request(ctx context.Context, agentID string, wakeID int64, requestIndex int, body []byte) (completion, error) {
	transportRetried := false
	for attempt := 1; attempt <= 3; attempt++ {
		response, status, retryAfter, err := c.requestAttempt(ctx, agentID, wakeID, requestIndex, attempt, body)
		reason := "retryable_http_status"
		if err != nil {
			r := errorReason(err)
			if (r != "transport_error" && r != "timeout") || transportRetried || ctx.Err() != nil || attempt == 3 {
				return completion{}, err
			}
			transportRetried, reason = true, "transport_retry"
		} else if status == http.StatusOK {
			return response, nil
		} else {
			retryable := status == 429 || status >= 500 && status <= 599
			if !retryable || attempt == 3 {
				reason := "http_error"
				if retryable {
					reason = "retry_exhausted"
				}
				return completion{}, failure("requesting chat completion", reason, nil)
			}
		}
		wait := min(retryAfter, maxRetryAfter)
		if wait <= 0 {
			base := time.Second << uint(attempt-1)
			wait = base + time.Duration(float64(base)/4*c.jitter())
		}
		c.event(ctx, slog.LevelWarn, logging.LLMRequestRetry, agentID, wakeID, requestIndex, attempt, time.Time{}, "retry", reason, Usage{}, status, 0)
		if err := c.sleep(ctx, wait); err != nil {
			return completion{}, failure("waiting to retry chat completion", "retry_wait_failed", err)
		}
	}
	return completion{}, failure("requesting chat completion", "retry_exhausted", nil)
}

// parseRetryAfter reads delay-seconds or an HTTP date; anything else is 0.
func (c *Client) parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(0, t.Sub(c.now()))
	}
	return 0
}

func (c *Client) requestAttempt(ctx context.Context, agentID string, wakeID int64, requestIndex, attempt int, body []byte) (response completion, status int, retryAfter time.Duration, requestErr error) {
	started := c.now()
	var usage Usage
	c.event(ctx, slog.LevelInfo, logging.LLMRequestStarted, agentID, wakeID, requestIndex, attempt, started, "started", "", usage, 0, 0)
	defer func() {
		outcome, reason, level := "success", "completed", slog.LevelInfo
		if requestErr != nil {
			outcome, reason, level = "error", errorReason(requestErr), slog.LevelError
		} else if status != http.StatusOK {
			outcome, reason, level = "error", "http_error", slog.LevelError
			if status == 429 || status >= 500 && status <= 599 {
				outcome, reason, level = "retry", "retryable_http_status", slog.LevelWarn
				if attempt == 3 {
					outcome, reason, level = "error", "retry_exhausted", slog.LevelError
				}
			}
		}
		c.event(ctx, level, logging.LLMRequestFinished, agentID, wakeID, requestIndex, attempt, started, outcome, reason, usage, status, 0)
	}()
	day := c.limits.Day(started)
	spent, err := c.spentForBudget(ctx, agentID, day)
	if err != nil {
		return completion{}, 0, 0, failure("checking daily LLM budget", "accounting_read_failed", err)
	}
	if !stdx.FiniteNonNegative(spent.CostUSD) {
		return completion{}, 0, 0, failure("checking daily LLM budget", "invalid_ledger_cost", nil)
	}
	if spent.CostUSD >= c.capUSD {
		return completion{}, 0, 0, fmt.Errorf("requesting chat completion: %w", ErrBudget)
	}
	response, status, retryAfter, err = c.attempt(ctx, body)
	if err != nil {
		return completion{}, status, 0, err
	}
	if response.Usage != nil {
		if response.Usage.PromptTokens == nil || response.Usage.CompletionTokens == nil {
			return completion{}, status, 0, failure("accounting chat response", "incomplete_usage", nil)
		}
		usage = Usage{TokensIn: *response.Usage.PromptTokens, TokensOut: *response.Usage.CompletionTokens, TokensCached: response.cached}
		if usage.TokensIn < 0 || usage.TokensOut < 0 || usage.TokensCached < 0 || usage.TokensCached > usage.TokensIn {
			usage = Usage{}
			return completion{}, status, 0, failure("accounting chat response", "negative_usage", nil)
		}
		if c.phase == "critic" {
			usage.Phase = "critic"
		}
		cachedRate := c.config.PricePerMTokInUSD
		if c.config.PricePerMTokCachedInUSD != nil {
			cachedRate = *c.config.PricePerMTokCachedInUSD
		}
		usage.CostUSD = float64(usage.TokensIn-usage.TokensCached)/1e6*c.config.PricePerMTokInUSD + float64(usage.TokensCached)/1e6*cachedRate + float64(usage.TokensOut)/1e6*c.config.PricePerMTokOutUSD
		if !stdx.FiniteNonNegative(usage.CostUSD) {
			usage = Usage{}
			return completion{}, status, 0, failure("accounting chat response", "nonfinite_cost", nil)
		}
		// Completed usage survives wake cancellation; bound this final write separately.
		accountingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.timeout)
		err = c.ledger.RecordLLMUsage(accountingCtx, agentID, wakeID, day, usage)
		cancel()
		outcome, reason, level := "success", "accounted", slog.LevelInfo
		if err != nil {
			outcome, reason, level = "error", "accounting_write_failed", slog.LevelError
		}
		c.recordUsage(ctx, requestIndex, attempt, usage)
		c.event(ctx, level, logging.LLMUsageRecorded, agentID, wakeID, requestIndex, attempt, started, outcome, reason, usage, status, 0)
		if err != nil {
			return completion{}, status, 0, failure("recording LLM usage", "accounting_write_failed", err)
		}
	}
	if status == http.StatusOK && response.Usage == nil {
		return completion{}, status, 0, failure("accounting chat response", "missing_usage", nil)
	}
	response.usdLeft = max(0, c.capUSD-spent.CostUSD-usage.CostUSD)
	return response, status, retryAfter, nil
}
func (c *Client) attempt(ctx context.Context, body []byte) (completion, int, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.adapter.endpoint(c.config.BaseURL), bytes.NewReader(body))
	if err != nil {
		return completion{}, 0, 0, failure("creating chat request", "invalid_request", err)
	}
	c.adapter.authorize(req, c.config.APIKey.Expose())
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return completion{}, 0, 0, failure("sending chat request", "transport_error", err)
	}
	defer res.Body.Close()
	retryAfter := c.parseRetryAfter(res.Header.Get("Retry-After"))
	raw, err := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if err != nil {
		return completion{}, res.StatusCode, 0, failure("reading chat response", "response_read_failed", err)
	}
	if len(raw) > 8<<20 {
		return completion{}, res.StatusCode, 0, failure("reading chat response", "response_too_large", nil)
	}
	response, err := c.adapter.decode(raw)
	if err != nil {
		if res.StatusCode != http.StatusOK {
			return completion{}, res.StatusCode, retryAfter, nil
		}
		return completion{}, res.StatusCode, 0, failure("decoding chat response", "malformed_response", err)
	}
	return response, res.StatusCode, retryAfter, nil
}

// PhaseLedger is implemented by stores that split spend by wake phase. Agent
// and study spend share limits.max_usd_per_day; dream spend uses its own cap.
type PhaseLedger interface {
	LLMUsageForDayPhases(context.Context, string, string, ...string) (Usage, error)
}

func (c *Client) spentForBudget(ctx context.Context, agentID, day string) (Usage, error) {
	return spentForPhase(ctx, c.ledger, c.phase, agentID, day)
}

func spentForPhase(ctx context.Context, l Ledger, phase, agentID, day string) (Usage, error) {
	ledger, ok := l.(PhaseLedger)
	if !ok {
		if phase == "dream" {
			return Usage{}, errors.New("phase accounting unavailable for dream")
		}
		return l.LLMUsageForDay(ctx, agentID, day)
	}
	phases := []string{"agent", "study"}
	if phase == "dream" {
		phases = []string{"dream"}
	}
	return ledger.LLMUsageForDayPhases(ctx, agentID, day, phases...)
}

// resolvePhase validates the phase and its spend cap, shared by every
// component that spends against the ledger.
func resolvePhase(opts Options, limits config.LimitsConfig) (string, float64, error) {
	phase := opts.Phase
	if phase == "" {
		phase = "agent"
	}
	if phase != "agent" && phase != "study" && phase != "dream" && phase != "critic" {
		return "", 0, errors.New("configuring LLM client: unknown phase")
	}
	capUSD := opts.CapUSD
	if phase == "dream" && capUSD <= 0 {
		return "", 0, errors.New("configuring LLM client: dream phase requires a positive spend cap")
	}
	if capUSD == 0 {
		capUSD = limits.MaxUSDPerDay
	}
	if !stdx.FiniteNonNegative(capUSD) || capUSD <= 0 {
		return "", 0, errors.New("configuring LLM client: invalid phase spend cap")
	}
	return phase, capUSD, nil
}

// Errors from transports and bodies may contain credentials or reflected provider
// content. Preserve cancellation identity, but never include untrusted error text.
func safeHTTPError(operation string, err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", operation, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, context.DeadlineExceeded)
	}
	return fmt.Errorf("%s: transport or response failure", operation)
}
func (c *Client) redactMessage(m Message) Message {
	redact := c.redactor.String
	m.Content = redact(m.Content)
	m.ToolCallID = redact(m.ToolCallID)
	m.ToolCalls = append([]ToolCall(nil), m.ToolCalls...)
	for i := range m.ToolCalls {
		m.ToolCalls[i].ID = redact(m.ToolCalls[i].ID)
		m.ToolCalls[i].Function.Name = redact(m.ToolCalls[i].Function.Name)
		m.ToolCalls[i].Function.Arguments = redact(m.ToolCalls[i].Function.Arguments)
	}
	return m
}

// cacheKey is the OpenAI prompt_cache_key, "<agent>:<phase>". Only api.openai.com
// is known to accept it; other OpenAI-compatible endpoints may reject unknown fields.
func (c *Client) cacheKey(agentID string) string {
	if u, err := url.Parse(c.config.BaseURL); err != nil || u.Hostname() != "api.openai.com" {
		return ""
	}
	return agentID + ":" + c.phase
}
