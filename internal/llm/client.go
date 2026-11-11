// Package llm implements the provider-agnostic Chat Completions loop.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/canhta/threadwalker/internal/config"
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
	// Zero omits provider-specific output/reasoning options.
	MaxCompletionTokens int
	ReasoningEffort     string
	HTTPClient          *http.Client
	Now                 func() time.Time
	Sleep               func(context.Context, time.Duration) error
	RequestTimeout      time.Duration
}
type Client struct {
	config      config.LLMConfig
	limits      config.LimitsConfig
	http        *http.Client
	now         func() time.Time
	timeout     time.Duration
	ledger      Ledger
	sleep       func(context.Context, time.Duration) error
	outputLimit int
	reasoning   string
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
	return &Client{config: cfg, limits: limits, http: opts.HTTPClient, now: opts.Now, timeout: opts.RequestTimeout, ledger: ledger, sleep: opts.Sleep, outputLimit: opts.MaxCompletionTokens, reasoning: opts.ReasoningEffort}, nil
}

type completion struct {
	Usage *struct {
		PromptTokens     *int64 `json:"prompt_tokens"`
		CompletionTokens *int64 `json:"completion_tokens"`
	} `json:"usage"`
	Choices json.RawMessage `json:"choices"`
}

// Run performs one wake's bounded conversation. The caller owns wake creation,
// tool auditing and guard wrapping; an error must not trigger wake replay.
func (c *Client) Run(ctx context.Context, agentID string, wakeID int64, messages []Message, tools []Tool, dispatcher Dispatcher) (Result, error) {
	if len(tools) > 0 && dispatcher == nil {
		return Result{}, errors.New("running chat loop: tools require a guard-wrapped dispatcher")
	}
	known := make(map[string]bool, len(tools))
	for _, tool := range tools {
		known[tool.Function.Name] = true
	}
	result := Result{Messages: append([]Message(nil), messages...)}
	for result.Steps < c.limits.MaxStepsPerWake {
		body, err := json.Marshal(struct {
			Model               string    `json:"model"`
			Messages            []Message `json:"messages"`
			Tools               []Tool    `json:"tools,omitempty"`
			MaxCompletionTokens int       `json:"max_completion_tokens,omitempty"`
			ReasoningEffort     string    `json:"reasoning_effort,omitempty"`
		}{c.config.Model, result.Messages, tools, c.outputLimit, c.reasoning})
		if err != nil {
			return result, fmt.Errorf("encoding chat request: %w", err)
		}
		response, err := c.request(ctx, agentID, wakeID, body)
		if err != nil {
			return result, err
		}
		var choices []struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		}
		if err := json.Unmarshal(response.Choices, &choices); err != nil {
			return result, fmt.Errorf("decoding chat choices: %w", err)
		}
		if len(choices) != 1 || choices[0].Message.Role != "assistant" {
			return result, errors.New("reading chat response: expected one assistant choice")
		}
		message := choices[0].Message
		ids := make(map[string]bool, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			if call.ID == "" || ids[call.ID] {
				return result, errors.New("reading chat response: missing or duplicate tool call ID")
			}
			ids[call.ID] = true
		}
		result.Messages = append(result.Messages, message)
		if len(message.ToolCalls) == 0 {
			result.Steps++
			if choices[0].FinishReason != "stop" {
				return result, errors.New("reading chat response: model did not finish normally")
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
			var output string
			var args map[string]json.RawMessage
			switch {
			case call.Type != "function":
				output = `{"error":"unsupported tool call type"}`
			case !known[call.Function.Name]:
				output = `{"error":"unknown tool"}`
			case json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args == nil:
				output = `{"error":"invalid JSON object arguments"}`
			default:
				output, err = dispatcher.Dispatch(ctx, call)
				if err != nil {
					output = `{"error":"tool dispatcher rejected or failed the call"}`
				}
			}
			result.Messages = append(result.Messages, Message{Role: "tool", Content: output, ToolCallID: call.ID})
		}
	}
	return result, fmt.Errorf("running chat loop: %w", ErrStepLimit)
}

// Requests retry only explicit 429/5xx responses. Transport errors are not
// retried because it is unknown whether the provider processed the request.
func (c *Client) request(ctx context.Context, agentID string, wakeID int64, body []byte) (completion, error) {
	for attempt := 0; attempt < 3; attempt++ {
		day := c.now().UTC().Format("2006-01-02")
		spent, err := c.ledger.LLMUsageForDay(ctx, agentID, day)
		if err != nil {
			return completion{}, fmt.Errorf("checking daily LLM budget: %w", err)
		}
		if !nonnegative(spent.CostUSD) {
			return completion{}, errors.New("checking daily LLM budget: invalid ledger cost")
		}
		if spent.CostUSD >= c.limits.MaxUSDPerDay {
			return completion{}, fmt.Errorf("requesting chat completion: %w", ErrBudget)
		}
		response, status, err := c.attempt(ctx, body)
		if err != nil {
			return completion{}, err
		}
		if response.Usage != nil {
			if response.Usage.PromptTokens == nil || response.Usage.CompletionTokens == nil {
				return completion{}, errors.New("accounting chat response: incomplete usage")
			}
			u := Usage{TokensIn: *response.Usage.PromptTokens, TokensOut: *response.Usage.CompletionTokens}
			if u.TokensIn < 0 || u.TokensOut < 0 {
				return completion{}, errors.New("accounting chat response: negative token usage")
			}
			u.CostUSD = float64(u.TokensIn)/1e6*c.config.PricePerMTokInUSD + float64(u.TokensOut)/1e6*c.config.PricePerMTokOutUSD
			if !nonnegative(u.CostUSD) {
				return completion{}, errors.New("accounting chat response: non-finite cost")
			}
			// A completed response still needs accounting if the wake
			// was cancelled while reading it. Bound this final write separately.
			accountingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.timeout)
			err := c.ledger.RecordLLMUsage(accountingCtx, agentID, wakeID, day, u)
			cancel()
			if err != nil {
				return completion{}, fmt.Errorf("recording LLM usage: %w", err)
			}
		}
		if status == http.StatusOK {
			if response.Usage == nil {
				return completion{}, errors.New("accounting chat response: missing usage")
			}
			return response, nil
		}
		if (status != 429 && (status < 500 || status > 599)) || attempt == 2 {
			return completion{}, fmt.Errorf("requesting chat completion: HTTP %d after %d attempt(s)", status, attempt+1)
		}
		if err := c.sleep(ctx, time.Second<<attempt); err != nil {
			return completion{}, fmt.Errorf("waiting to retry chat completion: %w", err)
		}
	}
	return completion{}, errors.New("requesting chat completion: attempts exhausted")
}
func (c *Client) attempt(ctx context.Context, body []byte) (completion, int, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.config.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return completion{}, 0, fmt.Errorf("creating chat request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey.Expose())
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return completion{}, 0, fmt.Errorf("sending chat request: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if err != nil {
		return completion{}, 0, fmt.Errorf("reading chat response: %w", err)
	}
	if len(raw) > 8<<20 {
		return completion{}, 0, errors.New("reading chat response: exceeds 8 MiB limit")
	}
	var response completion
	if err := json.Unmarshal(raw, &response); err != nil {
		if res.StatusCode != http.StatusOK {
			return completion{}, res.StatusCode, nil
		}
		return completion{}, 0, fmt.Errorf("decoding chat response: %w", err)
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
