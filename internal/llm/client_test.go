package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
)

func TestMultiStepToolLoop(t *testing.T) {
	requests, calls := 0, 0
	raw, err := os.ReadFile("testdata/tool-loop.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []json.RawMessage
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	ledger := &memoryLedger{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected request path or authorization")
		}
		var request struct {
			Model    string        `json:"model"`
			Messages []llm.Message `json:"messages"`
			Tools    []llm.Tool    `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "test-model" || len(request.Tools) != 1 {
			t.Errorf("request model/tools: %s/%d", request.Model, len(request.Tools))
		}
		if requests > 1 {
			last := request.Messages[len(request.Messages)-1]
			if last.Role != "tool" || last.ToolCallID != fmt.Sprint("call_", requests-1) || last.Content != fmt.Sprintf("hello\n{\"steps_left\":%d,\"usd_left\":%s}", 5-(requests-1), []string{"0.99986", "0.99969"}[requests-2]) {
				t.Errorf("tool result: %+v", last)
			}
		}
		w.Write(fixtures[requests-1])
	}))
	defer server.Close()
	client, err := llm.New(config.LLMConfig{BaseURL: server.URL + "/v1", Model: "test-model", APIKey: config.NewSecret("test-key"), PricePerMTokInUSD: 1, PricePerMTokOutUSD: 2}, config.LimitsConfig{MaxStepsPerWake: 5, MaxUSDPerDay: 1}, ledger, llm.Options{Now: func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "echo hello"}}, []llm.Tool{{Type: "function", Function: llm.Function{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}}}, llm.DispatchFunc(func(ctx context.Context, call llm.ToolCall) (string, error) { calls++; return "hello", nil }))
	if err != nil {
		t.Fatal(err)
	}
	if requests != 3 || calls != 2 || result.Steps != 3 || result.Content != "done" {
		t.Fatalf("requests=%d calls=%d result=%+v", requests, calls, result)
	}
	if ledger.usage.TokensIn != 450 || ledger.usage.TokensOut != 35 || math.Abs(ledger.usage.CostUSD-0.00052) > 1e-12 {
		t.Fatalf("ledger total: %+v", ledger.usage)
	}
}

type memoryLedger struct {
	usage             llm.Usage
	reads             int
	records           []llm.Usage
	days              []string
	readErr, writeErr error
}

func (l *memoryLedger) LLMUsageForDay(ctx context.Context, agent, day string) (llm.Usage, error) {
	l.reads++
	return l.usage, l.readErr
}
func (l *memoryLedger) RecordLLMUsage(ctx context.Context, agent string, wake int64, day string, u llm.Usage) error {
	if l.writeErr != nil {
		return l.writeErr
	}
	l.records = append(l.records, u)
	l.days = append(l.days, day)
	l.usage.TokensIn += u.TokensIn
	l.usage.TokensOut += u.TokensOut
	l.usage.CostUSD += u.CostUSD
	return nil
}
func TestBudgetAndAccounting(t *testing.T) {
	for _, initial := range []float64{0, 1, 2} {
		t.Run(fmt.Sprint(initial), func(t *testing.T) {
			ledger := &memoryLedger{usage: llm.Usage{CostUSD: initial}}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100000,"completion_tokens":200000}}`))
			}))
			defer server.Close()
			client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "test-model", APIKey: config.NewSecret("test-key"), PricePerMTokInUSD: 2, PricePerMTokOutUSD: 4}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, ledger, llm.Options{Now: func() time.Time { return time.Date(2026, 11, 12, 0, 30, 0, 0, time.FixedZone("east", 3600)) }})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "hello"}}, nil, nil)
			if initial >= 1 {
				if !errors.Is(err, llm.ErrBudget) || requests != 0 {
					t.Fatalf("spent budget: requests=%d err=%v", requests, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(ledger.records) != 1 || ledger.usage.TokensIn != 100000 || ledger.usage.TokensOut != 200000 || math.Abs(ledger.usage.CostUSD-1) > 1e-12 || ledger.days[0] != "2026-11-11" {
				t.Fatalf("accounting: %+v days=%v", ledger, ledger.days)
			}
		})
	}
}

func TestStepLimitAndMalformedTools(t *testing.T) {
	for _, tc := range []struct {
		name, args, kind string
		limit            int
	}{
		{"echo", `{`, "function", 2}, {"unknown", `{}`, "function", 2}, {"echo", `[]`, "function", 2}, {"echo", `null`, "function", 2}, {"echo", `{}`, "invalid", 2}, {"echo", `{}`, "function", 1},
	} {
		t.Run(tc.name+tc.args+tc.kind+fmt.Sprint(tc.limit), func(t *testing.T) {
			requests, calls := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if requests == 1 {
					json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "one", Type: tc.kind, Function: llm.CallFunction{Name: tc.name, Arguments: tc.args}}, {ID: "two", Type: "function", Function: llm.CallFunction{Name: "echo", Arguments: `{}`}}}}}}, "usage": map[string]int{"prompt_tokens": 3, "completion_tokens": 4}})
					return
				}
				var request struct {
					Messages []llm.Message `json:"messages"`
				}
				json.NewDecoder(r.Body).Decode(&request)
				if !strings.Contains(request.Messages[2].Content, "error") {
					t.Errorf("missing tool error: %+v", request.Messages)
				}
				w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4}}`))
			}))
			defer server.Close()
			client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "test", APIKey: config.NewSecret("key")}, config.LimitsConfig{MaxStepsPerWake: tc.limit, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, []llm.Tool{{Type: "function", Function: llm.Function{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}}}, refusing(&calls, "ok", "echo"))
			if !errors.Is(err, llm.ErrStepLimit) || result.Steps != tc.limit || requests != 1 {
				t.Fatalf("result=%+v requests=%d err=%v", result, requests, err)
			}
			if calls != 1 {
				t.Fatalf("dispatches=%d want=1", calls)
			}
		})
	}
}

func TestMalformedToolErrorsReachModel(t *testing.T) {
	requests, calls := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"bad","type":"function","function":{"name":"missing","arguments":"{}"}},{"id":"json","type":"function","function":{"name":"echo","arguments":"{"}},{"id":"dispatch","type":"function","function":{"name":"echo","arguments":"{}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":20}}`))
			return
		}
		var request struct {
			Messages []llm.Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&request)
		for _, m := range request.Messages[2:] {
			if m.Role != "tool" || !strings.Contains(m.Content, "error") {
				t.Errorf("tool error: %+v", m)
			}
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"recovered"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":5}}`))
	}))
	defer server.Close()
	client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "test", APIKey: config.NewSecret("key")}, config.LimitsConfig{MaxStepsPerWake: 4, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, []llm.Tool{{Type: "function", Function: llm.Function{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}}}, wellFormed(llm.DispatchFunc(func(context.Context, llm.ToolCall) (string, error) {
		calls++
		return "", errors.New("blocked by fake guard")
	}), "echo"))
	if err != nil || result.Content != "recovered" || result.Steps != 4 || calls != 1 {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
	}
}

func TestRetryBackoff(t *testing.T) {
	for _, statuses := range [][]int{{429, 500, 200}, {503, 503, 503}, {400}} {
		t.Run(fmt.Sprint(statuses), func(t *testing.T) {
			requests := 0
			var delays []time.Duration
			ledger := &memoryLedger{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				status := statuses[requests]
				requests++
				w.WriteHeader(status)
				if status == 200 {
					w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":2}}`))
				} else {
					w.Write([]byte(`{"error":{"message":"private provider error"}}`))
				}
			}))
			defer server.Close()
			client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "test", APIKey: config.NewSecret("key")}, config.LimitsConfig{MaxStepsPerWake: 2, MaxUSDPerDay: 1}, ledger, llm.Options{Jitter: func() float64 { return 0 }, Sleep: func(ctx context.Context, d time.Duration) error { delays = append(delays, d); return nil }})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, nil, nil)
			if requests != len(statuses) || ledger.reads != requests {
				t.Fatalf("requests=%d budget checks=%d", requests, ledger.reads)
			}
			if statuses[len(statuses)-1] == 200 {
				if err != nil || result.Content != "ok" || len(ledger.records) != 1 {
					t.Fatalf("result=%+v err=%v ledger=%+v", result, err, ledger)
				}
			} else {
				if err == nil || strings.Contains(err.Error(), "private provider error") {
					t.Fatalf("error=%v", err)
				}
			}
			if len(statuses) == 3 {
				if len(delays) != 2 || delays[0] != time.Second || delays[1] != 2*time.Second {
					t.Fatalf("backoff=%v", delays)
				}
			} else if len(delays) != 0 {
				t.Fatalf("unexpected retries: %v", delays)
			}
		})
	}
}

func TestBudgetRecheckedAfterResponseAndBeforeRetry(t *testing.T) {
	for _, status := range []int{200, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			ledger := &memoryLedger{}
			requests, calls := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.WriteHeader(status)
				w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call","type":"function","function":{"name":"echo","arguments":"{}"}}]}}],"usage":{"prompt_tokens":1000000,"completion_tokens":0}}`))
			}))
			defer server.Close()
			client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "test", APIKey: config.NewSecret("key"), PricePerMTokInUSD: 1}, config.LimitsConfig{MaxStepsPerWake: 5, MaxUSDPerDay: 1}, ledger, llm.Options{Sleep: func(context.Context, time.Duration) error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, []llm.Tool{{Type: "function", Function: llm.Function{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}}}, refusing(&calls, "ok", "echo"))
			if !errors.Is(err, llm.ErrBudget) || requests != 1 || ledger.usage.CostUSD != 1 {
				t.Fatalf("requests=%d cost=%g err=%v", requests, ledger.usage.CostUSD, err)
			}
		})
	}
}

func TestInvalidConfiguration(t *testing.T) {
	for _, change := range []func(*config.LLMConfig, *config.LimitsConfig, *llm.Options){
		func(c *config.LLMConfig, l *config.LimitsConfig, o *llm.Options) { c.BaseURL = ":invalid" },
		func(c *config.LLMConfig, l *config.LimitsConfig, o *llm.Options) { c.BaseURL = "ftp://localhost" },
		func(c *config.LLMConfig, l *config.LimitsConfig, o *llm.Options) {
			c.BaseURL = "https://user:password@localhost"
		},
		func(c *config.LLMConfig, l *config.LimitsConfig, o *llm.Options) {
			c.BaseURL = "https://localhost?key=secret"
		},
		func(c *config.LLMConfig, l *config.LimitsConfig, o *llm.Options) { c.Model = "" },
		func(c *config.LLMConfig, l *config.LimitsConfig, o *llm.Options) { c.APIKey = config.NewSecret("") },
		func(c *config.LLMConfig, l *config.LimitsConfig, o *llm.Options) { c.PricePerMTokInUSD = -1 },
		func(c *config.LLMConfig, l *config.LimitsConfig, o *llm.Options) { c.PricePerMTokOutUSD = math.NaN() },
		func(c *config.LLMConfig, l *config.LimitsConfig, o *llm.Options) { l.MaxStepsPerWake = 0 },
		func(c *config.LLMConfig, l *config.LimitsConfig, o *llm.Options) { l.MaxUSDPerDay = math.Inf(1) },
		func(c *config.LLMConfig, l *config.LimitsConfig, o *llm.Options) { o.RequestTimeout = -1 },
	} {
		cfg := config.LLMConfig{BaseURL: "https://example.test/v1", Model: "test", APIKey: config.NewSecret("secret")}
		limits := config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}
		opts := llm.Options{}
		change(&cfg, &limits, &opts)
		if _, err := llm.New(cfg, limits, &memoryLedger{}, opts); err == nil {
			t.Error("invalid config accepted")
		}
	}
}

func TestResponseFailuresStopBeforeDispatch(t *testing.T) {
	for _, body := range []string{
		`not json`,
		`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10}}`,
		`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2}}`,
		`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
		`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":-1,"completion_tokens":2}}`,
		`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"bad","type":"function","function":{"name":"echo","arguments":{}}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`,
		`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"","type":"function","function":{"name":"echo","arguments":"{}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`,
		`{"choices":[{"message":{"role":"assistant","content":"partial"},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`,
	} {
		t.Run(body, func(t *testing.T) {
			ledger := &memoryLedger{}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer server.Close()
			client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "test", APIKey: config.NewSecret("key")}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, ledger, llm.Options{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, []llm.Tool{{Type: "function", Function: llm.Function{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}}}, refusing(&calls, "ok", "echo"))
			if err == nil || calls != 0 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if strings.Contains(body, `"prompt_tokens":10`) && strings.Contains(body, `"completion_tokens":2`) && len(ledger.records) != 1 {
				t.Fatalf("valid usage lost before response rejection: %+v", ledger)
			}
		})
	}
}

func TestLedgerErrorsFailClosed(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(fmt.Sprint(write), func(t *testing.T) {
			sentinel := errors.New("ledger unavailable")
			ledger := &memoryLedger{}
			if write {
				ledger.writeErr = sentinel
			} else {
				ledger.readErr = sentinel
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`))
			}))
			defer server.Close()
			client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "test", APIKey: config.NewSecret("key")}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, ledger, llm.Options{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, nil, nil)
			want := 0
			if write {
				want = 1
			}
			if !errors.Is(err, sentinel) || requests != want {
				t.Fatalf("requests=%d err=%v", requests, err)
			}
		})
	}
}

func TestRequestTimeoutAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(30 * time.Millisecond); w.Write([]byte(`{}`)) }))
	defer server.Close()
	client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "test", APIKey: config.NewSecret("key")}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{RequestTimeout: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Run(ctx, "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error: %v", err)
	}
}

func TestRetrySleepCancellation(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(429) }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "test", APIKey: config.NewSecret("key")}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{Sleep: func(ctx context.Context, d time.Duration) error { cancel(); return ctx.Err() }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Run(ctx, "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, nil, nil)
	if !errors.Is(err, context.Canceled) || requests != 1 {
		t.Fatalf("requests=%d err=%v", requests, err)
	}
}

func TestBoundedOutputOptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			MaxCompletionTokens int    `json:"max_completion_tokens"`
			ReasoningEffort     string `json:"reasoning_effort"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.MaxCompletionTokens != 256 || request.ReasoningEffort != "none" {
			t.Errorf("options: %+v", request)
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`))
	}))
	defer server.Close()
	client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "test", APIKey: config.NewSecret("key")}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{MaxCompletionTokens: 256, ReasoningEffort: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestReasoningEffortOmittedWhenUnset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if _, present := request["reasoning_effort"]; present {
			t.Error("reasoning_effort present without configuration")
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`))
	}))
	defer server.Close()
	client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "test", APIKey: config.NewSecret("key")}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestMissingDispatcherFailsBeforeRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.Write([]byte(`{}`)) }))
	defer server.Close()
	client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "test", APIKey: config.NewSecret("key")}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, []llm.Tool{{Type: "function", Function: llm.Function{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}}}, nil)
	if err == nil || requests != 0 {
		t.Fatalf("requests=%d err=%v", requests, err)
	}
}

func TestRedirectDoesNotResendCredentials(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == "/chat/completions" {
			http.Redirect(w, r, "/redirected", 307)
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "test", APIKey: config.NewSecret("key")}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, nil, nil)
	if err == nil || requests != 1 {
		t.Fatalf("requests=%d err=%v", requests, err)
	}
}

func TestDefaultBackoffTimingAndCancellation(t *testing.T) {
	for _, cancelRetry := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelRetry), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if requests == 1 {
					if cancelRetry {
						cancel()
					}
					w.WriteHeader(429)
					return
				}
				w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
			}))
			defer server.Close()
			client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "test", APIKey: config.NewSecret("key")}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Run(ctx, "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, nil, nil)
			if cancelRetry {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else if err != nil || requests != 2 {
				t.Fatalf("requests=%d err=%v", requests, err)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type failedBody struct{}

func (failedBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (failedBody) Close() error             { return nil }
func TestResponseReadFailuresAndSizeLimit(t *testing.T) {
	for _, body := range []io.ReadCloser{failedBody{}, io.NopCloser(strings.NewReader(strings.Repeat("x", (8<<20)+1)))} {
		requests := 0
		client, err := llm.New(config.LLMConfig{BaseURL: "https://example.test/v1", Model: "test", APIKey: config.NewSecret("key")}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			requests++
			if _, ok := r.Context().Deadline(); !ok {
				t.Error("request has no timeout")
			}
			return &http.Response{StatusCode: 200, Body: body, Header: make(http.Header)}, nil
		})}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, nil, nil)
		if err == nil || requests != 1 {
			t.Fatalf("requests=%d err=%v", requests, err)
		}
	}
}
func TestInvalidLedgerCostFailsClosed(t *testing.T) {
	for _, cost := range []float64{-1, math.NaN(), math.Inf(1)} {
		ledger := &memoryLedger{usage: llm.Usage{CostUSD: cost}}
		client, err := llm.New(config.LLMConfig{BaseURL: "https://example.test/v1", Model: "test", APIKey: config.NewSecret("key")}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, ledger, llm.Options{HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			t.Error("invalid ledger allowed a request")
			return nil, errors.New("unexpected request")
		})}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, nil, nil); err == nil {
			t.Fatal("accepted invalid ledger cost")
		}
	}
}

func TestClientRedactsRedditCredentialsFromTranscript(t *testing.T) {
	const id, secret = "reddit-id-canary", "reddit-secret-canary"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"echo ` + id + ` ` + secret + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer server.Close()
	var study config.StudyConfig
	study.RedditClientID, study.RedditClientSecret = config.NewSecret(id), config.NewSecret(secret)
	client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "test", APIKey: config.NewSecret("key")}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{Redactor: config.NewRedactor(study)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "hi " + id}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range result.Messages {
		if strings.Contains(m.Content, id) || strings.Contains(m.Content, secret) {
			t.Fatalf("transcript leaked: %+v", m)
		}
	}
	if len(result.Messages) < 2 || !strings.Contains(result.Messages[len(result.Messages)-1].Content, "[REDACTED]") {
		t.Fatalf("messages: %+v", result.Messages)
	}
}

func nudgeRun(t *testing.T, maxSteps int, replies ...string) (llm.Result, []llm.Message, error) {
	t.Helper()
	var last []llm.Message
	n := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []llm.Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&request)
		last = request.Messages
		w.Write([]byte(replies[min(n, len(replies)-1)]))
		n++
	}))
	defer server.Close()
	client, err := llm.New(config.LLMConfig{BaseURL: server.URL + "/v1", Model: "m", APIKey: config.NewSecret("k")}, config.LimitsConfig{MaxStepsPerWake: maxSteps, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	tools := []llm.Tool{{Type: "function", Function: llm.Function{Name: "note_write", Parameters: json.RawMessage(`{"type":"object"}`)}}}
	result, err := client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, tools, llm.DispatchFunc(func(context.Context, llm.ToolCall) (string, error) { return "ok", nil }))
	return result, last, err
}

const (
	textReply = `{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
)

func TestWakeEndingInTextWithoutANoteIsNeverNudged(t *testing.T) {
	result, last, err := nudgeRun(t, 5, textReply, textReply)
	for _, m := range last {
		if m.Role == "user" && m.Content != "go" {
			t.Fatalf("unexpected user message %q", m.Content)
		}
	}
	if err != nil || result.Steps != 1 || result.Content != "done" {
		t.Fatalf("result %+v err %v", result, err)
	}
}

func TestStopCheckGivesBoundedExtraTurnsAndSeesTheCalls(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Write([]byte(textReply))
	}))
	defer server.Close()
	var seen []bool
	client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "test", APIKey: config.NewSecret("key")}, config.LimitsConfig{MaxStepsPerWake: 30, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{StopCheck: func(called map[string]bool, ok map[string]int) string {
		seen = append(seen, called["post"])
		return "you may not stop idle"
	}})
	if err != nil {
		t.Fatal(err)
	}
	tool := llm.Tool{Type: "function", Function: llm.Function{Name: "post", Parameters: json.RawMessage(`{"type":"object"}`)}}
	result, err := client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, []llm.Tool{tool}, refusing(new(int), "ok", "post"))
	if err != nil || requests != 1+llm.MaxStopNudges || len(seen) != llm.MaxStopNudges || result.Steps != 1+llm.MaxStopNudges {
		t.Fatalf("requests=%d checks=%v steps=%d err=%v", requests, seen, result.Steps, err)
	}
	if last := result.Messages[len(result.Messages)-2]; last.Role != "user" || last.Content != "you may not stop idle" {
		t.Fatalf("nudge message: %+v", last)
	}
}

func TestRetryAfterJitterAndTransportRetry(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	ok := `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	cases := []struct {
		name   string
		steps  []string // "<status>:<Retry-After>" or "transport"
		delays []time.Duration
		fail   bool
	}{
		{"seconds", []string{"429:7", "200:"}, []time.Duration{7 * time.Second}, false},
		{"http date", []string{"503:" + now.Add(9*time.Second).Format(http.TimeFormat), "200:"}, []time.Duration{9 * time.Second}, false},
		{"capped", []string{"429:3600", "200:"}, []time.Duration{60 * time.Second}, false},
		{"jitter", []string{"500:", "200:"}, []time.Duration{1250 * time.Millisecond}, false},
		{"transport once", []string{"transport", "200:"}, []time.Duration{1250 * time.Millisecond}, false},
		{"transport twice fails", []string{"transport", "transport", "200:"}, []time.Duration{1250 * time.Millisecond}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var delays []time.Duration
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				step := tc.steps[calls]
				calls++
				if step == "transport" {
					return nil, errors.New("connection reset")
				}
				status, after, _ := strings.Cut(step, ":")
				code, _ := strconv.Atoi(status)
				h := make(http.Header)
				if after != "" {
					h.Set("Retry-After", after)
				}
				return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader(ok))}, nil
			})
			client, err := llm.New(config.LLMConfig{BaseURL: "https://example.test/v1", Model: "test", APIKey: config.NewSecret("key")}, config.LimitsConfig{MaxStepsPerWake: 2, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{
				HTTPClient: &http.Client{Transport: transport}, Now: func() time.Time { return now }, Jitter: func() float64 { return 1 },
				Sleep: func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, nil, nil)
			if (err != nil) != tc.fail || !slices.Equal(delays, tc.delays) {
				t.Fatalf("err=%v delays=%v want=%v", err, delays, tc.delays)
			}
		})
	}
}
