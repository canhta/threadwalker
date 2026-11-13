package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
)

type terminalFailure struct{}

func (terminalFailure) Error() string           { return "private error details" }
func (terminalFailure) TerminalToolError() bool { return true }

func TestClientForwardsEveryCallWithoutValidating(t *testing.T) {
	for _, tc := range []struct{ name, kind, args string }{
		{"echo", "invalid", "{}"}, {"unknown", "function", "{}"}, {"echo", "function", "{"}, {"echo", "function", "null"},
	} {
		t.Run(tc.kind+tc.name+tc.args, func(t *testing.T) {
			var forwarded []llm.ToolCall
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if requests == 1 {
					writeHookResponse(w, map[string]any{"choices": []any{map[string]any{"message": llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "id", Type: tc.kind, Function: llm.CallFunction{Name: tc.name, Arguments: tc.args}}}}, "finish_reason": "tool_calls"}}})
					return
				}
				w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0}}`))
			}))
			defer server.Close()
			client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "test", APIKey: config.NewSecret("key")}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Run(context.Background(), "agent", 1, nil, []llm.Tool{{Type: "function", Function: llm.Function{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}}}, llm.DispatchFunc(func(ctx context.Context, call llm.ToolCall) (string, error) {
				if llm.RejectedReason(ctx) != "" {
					t.Errorf("valid response marked rejected: %q", llm.RejectedReason(ctx))
				}
				forwarded = append(forwarded, call)
				return "", errors.New("private dispatcher refusal")
			}))
			if err != nil || len(forwarded) != 1 || forwarded[0].Function.Name != tc.name || result.Content != "done" {
				t.Fatalf("forwarded %+v result %+v err %v", forwarded, result, err)
			}
		})
	}
}
func TestTerminalToolErrorsEndRunBeforeNextCall(t *testing.T) {
	requests, calls := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeHookResponse(w, map[string]any{"choices": []any{map[string]any{"message": llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "one", Type: "function", Function: llm.CallFunction{Name: "echo", Arguments: "{}"}}, {ID: "two", Type: "function", Function: llm.CallFunction{Name: "echo", Arguments: "{}"}}}}, "finish_reason": "tool_calls"}}})
	}))
	defer server.Close()
	client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "test", APIKey: config.NewSecret("key")}, config.LimitsConfig{MaxStepsPerWake: 5, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Run(context.Background(), "agent", 1, nil, []llm.Tool{{Type: "function", Function: llm.Function{Name: "echo", Parameters: json.RawMessage(`{}`)}}}, llm.DispatchFunc(func(context.Context, llm.ToolCall) (string, error) { calls++; return "", terminalFailure{} }))
	var terminal terminalFailure
	if !errors.As(err, &terminal) || strings.Contains(err.Error(), "private") || requests != 1 || result.Steps != 1 || calls != 1 {
		t.Fatalf("requests %d calls %d result %+v err %v", requests, calls, result, err)
	}
}

type rejection struct {
	name, reason string
}

// recorder is a dispatcher that records what the client forwards from a
// malformed response.
func recorder(rejected *[]rejection, result error) llm.Dispatcher {
	return llm.DispatchFunc(func(ctx context.Context, call llm.ToolCall) (string, error) {
		*rejected = append(*rejected, rejection{call.Function.Name, llm.RejectedReason(ctx)})
		return "", result
	})
}

func TestInvalidBatchAuditsEveryRejectedCall(t *testing.T) {
	for _, tc := range []struct{ finish, reason string }{{"tool_calls", "invalid_tool_call_id"}, {"length", "incomplete_tool_calls"}} {
		t.Run(tc.finish, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeHookResponse(w, map[string]any{"choices": []any{map[string]any{"message": llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "same", Type: "function", Function: llm.CallFunction{Name: "echo", Arguments: "{}"}}, {ID: "same", Type: "function", Function: llm.CallFunction{Name: "echo", Arguments: "{}"}}}}, "finish_reason": tc.finish}}})
			}))
			defer server.Close()
			client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "test", APIKey: config.NewSecret("key")}, config.LimitsConfig{MaxStepsPerWake: 5, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{})
			if err != nil {
				t.Fatal(err)
			}
			var got []rejection
			_, err = client.Run(context.Background(), "agent", 1, nil, nil, recorder(&got, nil))
			if err == nil || len(got) != 2 || got[0] != (rejection{"echo", tc.reason}) || got[1] != got[0] {
				t.Fatalf("rejected %+v err %v", got, err)
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
			var got []rejection
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "one", "name": "echo", "input": map[string]string{}}, map[string]any{"type": "image", "text": "unsupported"}}, "stop_reason": stop, "usage": map[string]int{"input_tokens": 0, "output_tokens": 0}})
			}))
			defer server.Close()
			client, err := llm.New(config.LLMConfig{Protocol: "anthropic", BaseURL: server.URL, Model: "synthetic", APIKey: config.NewSecret("dummy")}, config.LimitsConfig{MaxStepsPerWake: 5, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Run(context.Background(), "agent", 1, nil, nil, recorder(&got, terminalFailure{}))
			var terminal terminalFailure
			if len(got) != 1 || got[0] != (rejection{"echo", "invalid_response"}) || !errors.As(err, &terminal) {
				t.Fatalf("rejected %+v err %v", got, err)
			}
		})
	}
}

type nonterminalFailure struct{}

func (nonterminalFailure) Error() string           { return "private nonterminal details" }
func (nonterminalFailure) TerminalToolError() bool { return false }
func runWithToolError(t *testing.T, toolErr error) (llm.Result, int, error) {
	t.Helper()
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
	result, err := client.Run(context.Background(), "agent", 1, nil, []llm.Tool{{Type: "function", Function: llm.Function{Name: "echo", Parameters: json.RawMessage(`{}`)}}}, llm.DispatchFunc(func(context.Context, llm.ToolCall) (string, error) { return "", toolErr }))
	return result, requests, err
}

func TestNonterminalMarkerContinuesWithSafeToolError(t *testing.T) {
	result, requests, err := runWithToolError(t, nonterminalFailure{})
	if err != nil || requests != 2 || result.Content != "done" || !result.Messages[1].ToolError || strings.Contains(result.Messages[1].Content, "private") {
		t.Fatalf("requests %d result %+v err %v", requests, result, err)
	}
}

type blockedFailure struct{}

func (blockedFailure) Error() string       { return "private blocked details" }
func (blockedFailure) ModelResult() string { return `{"blocked":"x","hint":"fix it"}` }
func TestBlockedErrorReachesModelAsItsTypedResult(t *testing.T) {
	result, requests, err := runWithToolError(t, fmt.Errorf("wrapped: %w", blockedFailure{}))
	if err != nil || requests != 2 || !result.Messages[1].ToolError || !strings.HasPrefix(result.Messages[1].Content, `{"blocked":"x","hint":"fix it"}`+"\n") {
		t.Fatalf("requests %d result %+v err %v", requests, result, err)
	}
}

func TestMalformedChoiceShapeStillAuditsRecognizableCalls(t *testing.T) {
	for _, raw := range []string{
		`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"one","type":"function","function":{"name":"echo","arguments":{}}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":0,"completion_tokens":0}}`,
		`{"choices":[{"message":{"role":"user","tool_calls":[{"id":"one","type":"function","function":{"name":"echo","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":0,"completion_tokens":0}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var got []rejection
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(raw)) }))
			defer server.Close()
			client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "synthetic", APIKey: config.NewSecret("dummy")}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Run(context.Background(), "agent", 1, nil, nil, recorder(&got, nil))
			if err == nil || len(got) != 1 || got[0] != (rejection{"echo", "invalid_response"}) {
				t.Fatalf("rejected %+v err %v", got, err)
			}
		})
	}
}

// wellFormed stands in for the real dispatcher's validation: it refuses
// rejected responses, other call types, unknown tools and non-object
// arguments, and passes the rest to inner.
func wellFormed(inner llm.Dispatcher, known ...string) llm.Dispatcher {
	return llm.DispatchFunc(func(ctx context.Context, call llm.ToolCall) (string, error) {
		var args map[string]json.RawMessage
		if llm.RejectedReason(ctx) != "" || call.Type != "function" || !slices.Contains(known, call.Function.Name) || json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args == nil {
			return "", errors.New("refused")
		}
		return inner.Dispatch(ctx, call)
	})
}

func refusing(calls *int, result string, known ...string) llm.Dispatcher {
	return wellFormed(llm.DispatchFunc(func(context.Context, llm.ToolCall) (string, error) { *calls++; return result, nil }), known...)
}
