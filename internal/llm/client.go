// Package llm implements a bounded tool loop over configured LLM protocols.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/store"
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
}

// Dispatcher must be wrapped by internal/guard before production use. This
// package provides no tool implementations or registry; tests use fake dispatchers.
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
	// RejectedToolCall observes attempts rejected by local protocol validation.
	// Production callers install their guard/audit boundary here. Legacy fake
	// dispatchers may leave it nil. A marked terminal error stops the wake.
	RejectedToolCall func(context.Context, ToolCall, string) error
	Logger           *slog.Logger
	ProviderID       string
	ProfileID        string
	// Zero omits provider-specific output/reasoning options.
	MaxCompletionTokens int
	ReasoningEffort     string
	HTTPClient          *http.Client
	Now                 func() time.Time
	Sleep               func(context.Context, time.Duration) error
	RequestTimeout      time.Duration
}
type Client struct {
	rejectedToolCall      func(context.Context, ToolCall, string) error
	logger                *slog.Logger
	providerID, profileID string
	config                config.LLMConfig
	limits                config.LimitsConfig
	http                  *http.Client
	now                   func() time.Time
	timeout               time.Duration
	ledger                Ledger
	sleep                 func(context.Context, time.Duration) error
	outputLimit           int
	reasoning             string
	adapter               protocolAdapter
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
	if cfg.Protocol == "anthropic" && opts.ReasoningEffort != "" {
		return nil, errors.New("configuring LLM client: reasoning effort is only supported by openai protocol")
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("configuring LLM client: invalid base URL")
	}
	if strings.TrimSpace(cfg.Model) == "" || cfg.APIKey.Expose() == "" {
		return nil, errors.New("configuring LLM client: model and API key are required")
	}
	if !nonnegative(cfg.PricePerMTokInUSD) || !nonnegative(cfg.PricePerMTokOutUSD) || !nonnegative(limits.MaxUSDPerDay) || limits.MaxUSDPerDay == 0 || limits.MaxStepsPerWake <= 0 {
		return nil, errors.New("configuring LLM client: invalid prices or limits")
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
		opts.Sleep = sleep
	}
	if opts.RequestTimeout == 0 {
		opts.RequestTimeout = 30 * time.Second
	}
	if cfg.Protocol == "" {
		cfg.Protocol = "openai"
	}
	return &Client{rejectedToolCall: opts.RejectedToolCall, logger: opts.Logger, providerID: opts.ProviderID, profileID: opts.ProfileID, config: cfg, limits: limits, http: opts.HTTPClient, now: opts.Now, timeout: opts.RequestTimeout, ledger: ledger, sleep: opts.Sleep, outputLimit: opts.MaxCompletionTokens, reasoning: opts.ReasoningEffort, adapter: adapter}, nil
}

type completion struct {
	Usage *struct {
		PromptTokens     *int64 `json:"prompt_tokens"`
		CompletionTokens *int64 `json:"completion_tokens"`
	} `json:"usage"`
	Choices      json.RawMessage `json:"choices"`
	MessageError error           `json:"-"`
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
	for result.Steps < c.limits.MaxStepsPerWake {
		requestIndex++
		body, err := c.adapter.encode(c.config.Model, result.Messages, tools, c.outputLimit, c.reasoning)
		if err != nil {
			return result, failure("encoding chat request", "invalid_request", err)
		}
		response, err := c.request(ctx, agentID, wakeID, requestIndex, body)
		if err != nil {
			return result, err
		}
		if response.MessageError != nil {
			if err := c.rejectResponse(ctx, response.Choices, "invalid_response"); isTerminalToolError(err) {
				return result, terminalToolFailure(err)
			}
			return result, failure("reading chat response", "invalid_response", response.MessageError)
		}
		var choices []struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		}
		if err := json.Unmarshal(response.Choices, &choices); err != nil {
			if hookErr := c.rejectResponse(ctx, response.Choices, "invalid_response"); isTerminalToolError(hookErr) {
				return result, terminalToolFailure(hookErr)
			}
			return result, failure("decoding chat choices", "malformed_choices", err)
		}
		if len(choices) != 1 || choices[0].Message.Role != "assistant" {
			if err := c.rejectResponse(ctx, response.Choices, "invalid_response"); isTerminalToolError(err) {
				return result, terminalToolFailure(err)
			}
			return result, failure("reading chat response", "invalid_choice", nil)
		}
		message := c.redactMessage(choices[0].Message)
		if len(message.ToolCalls) > 0 && choices[0].FinishReason != "" && choices[0].FinishReason != "tool_calls" {
			if err := c.rejectCalls(ctx, message.ToolCalls, "incomplete_tool_calls"); isTerminalToolError(err) {
				return result, terminalToolFailure(err)
			}
			return result, failure("reading chat response", "incomplete_tool_calls", nil)
		}
		ids := make(map[string]bool, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			if call.ID == "" || ids[call.ID] {
				if err := c.rejectCalls(ctx, message.ToolCalls, "invalid_tool_call_id"); isTerminalToolError(err) {
					return result, terminalToolFailure(err)
				}
				return result, failure("reading chat response", "invalid_tool_call_id", nil)
			}
			ids[call.ID] = true
		}
		result.Messages = append(result.Messages, message)
		if len(message.ToolCalls) == 0 {
			result.Steps++
			if choices[0].FinishReason != "stop" {
				return result, failure("reading chat response", "incomplete_response", nil)
			}
			result.Content = message.Content
			return result, nil
		}
		for _, call := range message.ToolCalls {
			if err := ctx.Err(); err != nil {
				return result, fmt.Errorf("dispatching tool call: %w", err)
			}
			if result.Steps >= c.limits.MaxStepsPerWake {
				return result, fmt.Errorf("running chat loop: %w", ErrStepLimit)
			}
			result.Steps++
			toolStarted := c.now()
			outcome, reason := "success", "completed"
			safeTool := ""
			var output string
			var toolErr error
			toolError := true
			var args map[string]json.RawMessage
			switch {
			case call.Type != "function":
				outcome, reason = "error", "unsupported_tool_type"
				output = `{"error":"unsupported tool call type"}`
			case !known[call.Function.Name]:
				outcome, reason = "error", "unknown_tool"
				output = `{"error":"unknown tool"}`
			case json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args == nil:
				outcome, reason = "error", "invalid_tool_arguments"
				output = `{"error":"invalid JSON object arguments"}`
			default:
				safeTool = call.Function.Name
				output, toolErr = dispatcher.Dispatch(ctx, call)
				toolError = toolErr != nil
				if toolErr != nil {
					outcome, reason = "error", "dispatch_failed"
					output = `{"error":"tool dispatcher rejected or failed the call"}`
				}
			}
			if safeTool == "" && c.rejectedToolCall != nil {
				toolErr = c.rejectedToolCall(ctx, call, reason)
			}
			level := slog.LevelInfo
			if outcome == "error" {
				level = slog.LevelError
			}
			logging.Emit(ctx, c.logger, level, logging.Record{Event: logging.LLMToolFinished, AgentID: agentID, WakeID: wakeID, Request: requestIndex, ToolCallID: fmt.Sprintf("%d/%d", wakeID, result.Steps), Tool: safeTool, ProviderID: c.providerID, ProfileID: c.profileID, Model: c.config.Model, Protocol: c.config.Protocol, Operation: "dispatch", Outcome: outcome, Reason: reason, DurationMS: c.now().Sub(toolStarted).Milliseconds(), Steps: result.Steps})
			result.Messages = append(result.Messages, c.redactMessage(Message{Role: "tool", Content: output, ToolCallID: call.ID, ToolError: toolError}))
			if isTerminalToolError(toolErr) {
				return result, terminalToolFailure(toolErr)
			}
		}
	}
	return result, fmt.Errorf("running chat loop: %w", ErrStepLimit)
}

// Requests retry only explicit 429/5xx responses. Transport errors are not
// retried because it is unknown whether the provider processed the request.
func (c *Client) request(ctx context.Context, agentID string, wakeID int64, requestIndex int, body []byte) (completion, error) {
	for attempt := 1; attempt <= 3; attempt++ {
		response, status, err := c.requestAttempt(ctx, agentID, wakeID, requestIndex, attempt, body)
		if err != nil {
			return completion{}, err
		}
		if status == http.StatusOK {
			return response, nil
		}
		retryable := status == 429 || status >= 500 && status <= 599
		if !retryable || attempt == 3 {
			reason := "http_error"
			if retryable {
				reason = "retry_exhausted"
			}
			return completion{}, failure("requesting chat completion", reason, nil)
		}
		c.event(ctx, slog.LevelWarn, logging.LLMRequestRetry, agentID, wakeID, requestIndex, attempt, time.Time{}, "retry", "retryable_http_status", Usage{}, status, 0)
		if err := c.sleep(ctx, time.Second<<uint(attempt-1)); err != nil {
			return completion{}, failure("waiting to retry chat completion", "retry_wait_failed", err)
		}
	}
	return completion{}, failure("requesting chat completion", "retry_exhausted", nil)
}

func (c *Client) requestAttempt(ctx context.Context, agentID string, wakeID int64, requestIndex, attempt int, body []byte) (response completion, status int, requestErr error) {
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
	day := started.UTC().Format("2006-01-02")
	spent, err := c.ledger.LLMUsageForDay(ctx, agentID, day)
	if err != nil {
		return completion{}, 0, failure("checking daily LLM budget", "accounting_read_failed", err)
	}
	if !nonnegative(spent.CostUSD) {
		return completion{}, 0, failure("checking daily LLM budget", "invalid_ledger_cost", nil)
	}
	if spent.CostUSD >= c.limits.MaxUSDPerDay {
		return completion{}, 0, fmt.Errorf("requesting chat completion: %w", ErrBudget)
	}
	response, status, err = c.attempt(ctx, body)
	if err != nil {
		return completion{}, status, err
	}
	if response.Usage != nil {
		if response.Usage.PromptTokens == nil || response.Usage.CompletionTokens == nil {
			return completion{}, status, failure("accounting chat response", "incomplete_usage", nil)
		}
		usage = Usage{TokensIn: *response.Usage.PromptTokens, TokensOut: *response.Usage.CompletionTokens}
		if usage.TokensIn < 0 || usage.TokensOut < 0 {
			usage = Usage{}
			return completion{}, status, failure("accounting chat response", "negative_usage", nil)
		}
		usage.CostUSD = float64(usage.TokensIn)/1e6*c.config.PricePerMTokInUSD + float64(usage.TokensOut)/1e6*c.config.PricePerMTokOutUSD
		if !nonnegative(usage.CostUSD) {
			usage = Usage{}
			return completion{}, status, failure("accounting chat response", "nonfinite_cost", nil)
		}
		// Completed usage survives wake cancellation; bound this final write separately.
		accountingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.timeout)
		err = c.ledger.RecordLLMUsage(accountingCtx, agentID, wakeID, day, usage)
		cancel()
		outcome, reason, level := "success", "accounted", slog.LevelInfo
		if err != nil {
			outcome, reason, level = "error", "accounting_write_failed", slog.LevelError
		}
		c.event(ctx, level, logging.LLMUsageRecorded, agentID, wakeID, requestIndex, attempt, started, outcome, reason, usage, status, 0)
		if err != nil {
			return completion{}, status, failure("recording LLM usage", "accounting_write_failed", err)
		}
	}
	if status == http.StatusOK && response.Usage == nil {
		return completion{}, status, failure("accounting chat response", "missing_usage", nil)
	}
	return response, status, nil
}
func (c *Client) attempt(ctx context.Context, body []byte) (completion, int, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.adapter.endpoint(c.config.BaseURL), bytes.NewReader(body))
	if err != nil {
		return completion{}, 0, failure("creating chat request", "invalid_request", err)
	}
	c.adapter.authorize(req, c.config.APIKey.Expose())
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return completion{}, 0, failure("sending chat request", "transport_error", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if err != nil {
		return completion{}, res.StatusCode, failure("reading chat response", "response_read_failed", err)
	}
	if len(raw) > 8<<20 {
		return completion{}, res.StatusCode, failure("reading chat response", "response_too_large", nil)
	}
	response, err := c.adapter.decode(raw)
	if err != nil {
		if res.StatusCode != http.StatusOK {
			return completion{}, res.StatusCode, nil
		}
		return completion{}, res.StatusCode, failure("decoding chat response", "malformed_response", err)
	}
	return response, res.StatusCode, nil
}
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func nonnegative(v float64) bool { return v >= 0 && !math.IsInf(v, 0) && !math.IsNaN(v) }

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
	redact := func(value string) string { return strings.ReplaceAll(value, c.config.APIKey.Expose(), "[REDACTED]") }
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
