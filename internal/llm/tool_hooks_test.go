package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/llm"
)

type terminalFailure struct{}

func (terminalFailure) Error() string           { return "private error details" }
func (terminalFailure) TerminalToolError() bool { return true }

func TestRejectedToolHookPreservesLegacyValidation(t *testing.T) {
	for _, tc := range []struct{ name, kind, args, reason string }{
		{"echo", "invalid", "{}", "unsupported_tool_type"}, {"unknown", "function", "{}", "unknown_tool"}, {"echo", "function", "{", "invalid_tool_arguments"}, {"echo", "function", "null", "invalid_tool_arguments"},
	} {
		t.Run(tc.reason+tc.args, func(t *testing.T) {
			calls, rejected, requests := 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if requests == 1 {
					writeHookResponse(w, map[string]any{"choices": []any{map[string]any{"message": llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "id", Type: tc.kind, Function: llm.CallFunction{Name: tc.name, Arguments: tc.args}}}}, "finish_reason": "tool_calls"}}})
					return
				}
				w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0}}`))
			}))
			defer server.Close()
			opts := llm.Options{RejectedToolCall: func(ctx context.Context, call llm.ToolCall, reason string) error {
				rejected++
				if call.Function.Name != tc.name || reason != tc.reason {
					t.Errorf("hook: %+v %s", call, reason)
				}
				return errors.New("private hook failure")
			}}
			client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "test", APIKey: config.NewSecret("key")}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, &memoryLedger{}, opts)
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Run(context.Background(), "agent", 1, nil, []llm.Tool{{Type: "function", Function: llm.Function{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}}}, llm.DispatchFunc(func(context.Context, llm.ToolCall) (string, error) { calls++; return "", nil }))
			if err != nil || calls != 0 || rejected != 1 || result.Content != "done" {
				t.Fatalf("calls %d rejected %d result %+v err %v", calls, rejected, result, err)
			}
		})
	}
}
func TestTerminalToolErrorsEndRunBeforeNextCall(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(map[bool]string{false: "dispatch", true: "rejected"}[rejected], func(t *testing.T) {
			requests, calls, hooks := 0, 0, 0
			name := "echo"
			if rejected {
				name = "unknown"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				writeHookResponse(w, map[string]any{"choices": []any{map[string]any{"message": llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "one", Type: "function", Function: llm.CallFunction{Name: name, Arguments: "{}"}}, {ID: "two", Type: "function", Function: llm.CallFunction{Name: "echo", Arguments: "{}"}}}}, "finish_reason": "tool_calls"}}})
			}))
			defer server.Close()
			opts := llm.Options{RejectedToolCall: func(context.Context, llm.ToolCall, string) error { hooks++; return terminalFailure{} }}
			client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "test", APIKey: config.NewSecret("key")}, config.LimitsConfig{MaxStepsPerWake: 5, MaxUSDPerDay: 1}, &memoryLedger{}, opts)
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Run(context.Background(), "agent", 1, nil, []llm.Tool{{Type: "function", Function: llm.Function{Name: "echo", Parameters: json.RawMessage(`{}`)}}}, llm.DispatchFunc(func(context.Context, llm.ToolCall) (string, error) { calls++; return "", terminalFailure{} }))
			var terminal terminalFailure
			if !errors.As(err, &terminal) || strings.Contains(err.Error(), "private") || requests != 1 || result.Steps != 1 || calls+hooks != 1 {
				t.Fatalf("requests %d calls %d hooks %d result %+v err %v", requests, calls, hooks, result, err)
			}
		})
	}
}
func TestInvalidBatchAuditsEveryRejectedCall(t *testing.T) {
	for _, finish := range []string{"tool_calls", "length"} {
		t.Run(finish, func(t *testing.T) {
			hooks := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeHookResponse(w, map[string]any{"choices": []any{map[string]any{"message": llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "same", Type: "function", Function: llm.CallFunction{Name: "echo", Arguments: "{}"}}, {ID: "same", Type: "function", Function: llm.CallFunction{Name: "echo", Arguments: "{}"}}}}, "finish_reason": finish}}})
			}))
			defer server.Close()
			client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "test", APIKey: config.NewSecret("key")}, config.LimitsConfig{MaxStepsPerWake: 5, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{RejectedToolCall: func(context.Context, llm.ToolCall, string) error { hooks++; return nil }})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Run(context.Background(), "agent", 1, nil, nil, nil)
			if err == nil || hooks != 2 {
				t.Fatalf("hooks %d err %v", hooks, err)
			}
		})
	}
}

func writeHookResponse(w http.ResponseWriter, body map[string]any) {
	body["usage"] = map[string]int{"prompt_tokens": 0, "completion_tokens": 0}
	json.NewEncoder(w).Encode(body)
}

func TestRejectedNativeResponseAuditsNormalizedAttempts(t *testing.T) {
	for _, stop := range []string{"tool_use", "max_tokens"} {
		t.Run(stop, func(t *testing.T) {
			hooks := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "one", "name": "echo", "input": map[string]string{}}, map[string]any{"type": "thinking", "text": "unsupported"}}, "stop_reason": stop, "usage": map[string]int{"input_tokens": 0, "output_tokens": 0}})
			}))
			defer server.Close()
			client, err := llm.New(config.LLMConfig{Protocol: "anthropic", BaseURL: server.URL, Model: "synthetic", APIKey: config.NewSecret("dummy")}, config.LimitsConfig{MaxStepsPerWake: 5, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{RejectedToolCall: func(ctx context.Context, call llm.ToolCall, reason string) error {
				hooks++
				if call.Function.Name != "echo" || reason != "invalid_response" {
					t.Errorf("unexpected rejected attempt %+v %q", call, reason)
				}
				return terminalFailure{}
			}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Run(context.Background(), "agent", 1, nil, nil, nil)
			var terminal terminalFailure
			if hooks != 1 || !errors.As(err, &terminal) {
				t.Fatalf("hooks %d err %v", hooks, err)
			}
		})
	}
}

type nonterminalFailure struct{}

func (nonterminalFailure) Error() string           { return "private nonterminal details" }
func (nonterminalFailure) TerminalToolError() bool { return false }
func TestNonterminalMarkerContinuesWithSafeToolError(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			writeHookResponse(w, map[string]any{"choices": []any{map[string]any{"message": llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "one", Type: "function", Function: llm.CallFunction{Name: "echo", Arguments: "{}"}}}}, "finish_reason": "tool_calls"}}})
			return
		}
		writeHookResponse(w, map[string]any{"choices": []any{map[string]any{"message": llm.Message{Role: "assistant", Content: "done"}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "synthetic", APIKey: config.NewSecret("dummy")}, config.LimitsConfig{MaxStepsPerWake: 5, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Run(context.Background(), "agent", 1, nil, []llm.Tool{{Type: "function", Function: llm.Function{Name: "echo", Parameters: json.RawMessage(`{}`)}}}, llm.DispatchFunc(func(context.Context, llm.ToolCall) (string, error) { return "", nonterminalFailure{} }))
	if err != nil || requests != 2 || result.Content != "done" || !result.Messages[1].ToolError || strings.Contains(result.Messages[1].Content, "private") {
		t.Fatalf("requests %d result %+v err %v", requests, result, err)
	}
}

func TestMalformedChoiceShapeStillAuditsRecognizableCalls(t *testing.T) {
	for _, raw := range []string{
		`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"one","type":"function","function":{"name":"echo","arguments":{}}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":0,"completion_tokens":0}}`,
		`{"choices":[{"message":{"role":"user","tool_calls":[{"id":"one","type":"function","function":{"name":"echo","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":0,"completion_tokens":0}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			hooks := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(raw)) }))
			defer server.Close()
			client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "synthetic", APIKey: config.NewSecret("dummy")}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{RejectedToolCall: func(ctx context.Context, call llm.ToolCall, reason string) error {
				hooks++
				if call.Function.Name != "echo" {
					t.Errorf("unrecognized attempt: %+v", call)
				}
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Run(context.Background(), "agent", 1, nil, nil, nil)
			if err == nil || hooks != 1 {
				t.Fatalf("hooks %d err %v", hooks, err)
			}
		})
	}
}
