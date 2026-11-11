package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/llm"
)

func protocolJSONEqual(t *testing.T, raw json.RawMessage, want string) bool {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(raw, &gotValue); err != nil {
		t.Fatalf("decoding actual JSON %s: %v", raw, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("decoding expected JSON %s: %v", want, err)
	}
	return reflect.DeepEqual(gotValue, wantValue)
}

func protocolFindToolResult(results []protocolNativeBlock, id string) (protocolNativeBlock, bool) {
	for _, result := range results {
		if result.ToolUseID == id {
			return result, true
		}
	}
	return protocolNativeBlock{}, false
}

func protocolNewAnthropicClient(t *testing.T, baseURL string, ledger llm.Ledger, opts llm.Options) *llm.Client {
	t.Helper()
	client, err := llm.New(config.LLMConfig{
		Protocol:           "anthropic",
		BaseURL:            baseURL,
		Model:              "arbitrary-provider/native-model",
		APIKey:             config.NewSecret("fixture-secret"),
		PricePerMTokInUSD:  1,
		PricePerMTokOutUSD: 2,
	}, config.LimitsConfig{MaxStepsPerWake: 6, MaxUSDPerDay: 1}, ledger, opts)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// TestAnthropicRequestSchemaSystemAndHeaders locks the native Messages wire
// contract: endpoint, credentials, system separation, max_tokens, and tool
// definitions with input_schema.
func TestAnthropicRequestSchemaSystemAndHeaders(t *testing.T) {
	recorder := &protocolRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(t, r)
		w.Write(protocolAnthropicFinal("done", 12, 3))
	}))
	defer server.Close()

	ledger := &memoryLedger{}
	client := protocolNewAnthropicClient(t, server.URL+"/v1", ledger, llm.Options{MaxCompletionTokens: 256})
	tools := []llm.Tool{{
		Type: "function",
		Function: llm.Function{
			Name:        "echo",
			Description: "Echo the supplied text.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
			Strict:      true,
		},
	}}
	result, err := client.Run(context.Background(), "agent", 1, []llm.Message{
		{Role: "system", Content: "immutable system prompt"},
		{Role: "user", Content: "hello"},
	}, tools, llm.DispatchFunc(func(context.Context, llm.ToolCall) (string, error) {
		t.Error("schema-only conversation unexpectedly called a tool")
		return "", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "done" || result.Steps != 1 {
		t.Fatalf("result=%+v", result)
	}

	requests := recorder.all()
	if len(requests) != 1 {
		t.Fatalf("requests=%d want=1", len(requests))
	}
	request := requests[0]
	if request.Path != "/v1/messages" {
		t.Errorf("path=%q want=/v1/messages", request.Path)
	}
	if got := request.Header.Get("x-api-key"); got != "fixture-secret" {
		t.Errorf("x-api-key=%q", got)
	}
	if got := request.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version=%q want=2023-06-01", got)
	}
	if got := request.Header.Get("Authorization"); got != "" {
		t.Errorf("native request must not use OpenAI Authorization: %q", got)
	}

	body, fields := protocolDecodeNativeRequest(t, request)
	if body.Model != "arbitrary-provider/native-model" {
		t.Errorf("model=%q", body.Model)
	}
	if body.MaxTokens != 256 {
		t.Errorf("max_tokens=%d want=256", body.MaxTokens)
	}
	if got := strings.TrimSpace(protocolText(t, body.System)); got != "immutable system prompt" {
		t.Errorf("system=%q want immutable prompt", got)
	}
	if protocolMessagesContainRole(body.Messages, "system") {
		t.Errorf("system turn leaked into native messages: %+v", body.Messages)
	}
	if len(body.Messages) != 1 || body.Messages[0].Role != "user" {
		t.Fatalf("messages=%+v", body.Messages)
	}
	if got := protocolText(t, body.Messages[0].Content); got != "hello" {
		t.Errorf("user content=%q want=hello", got)
	}
	if len(body.Tools) != 1 {
		t.Fatalf("tools=%d want=1", len(body.Tools))
	}
	var tool map[string]json.RawMessage
	if err := json.Unmarshal(body.Tools[0], &tool); err != nil {
		t.Fatal(err)
	}
	if _, ok := tool["function"]; ok {
		t.Error("native tool retained OpenAI function wrapper")
	}
	if _, ok := tool["parameters"]; ok {
		t.Error("native tool retained OpenAI parameters key")
	}
	var name, description string
	if err := json.Unmarshal(tool["name"], &name); err != nil || name != "echo" {
		t.Errorf("tool name=%q err=%v", name, err)
	}
	if err := json.Unmarshal(tool["description"], &description); err != nil || description == "" {
		t.Errorf("tool description=%q err=%v", description, err)
	}
	var schema struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(tool["input_schema"], &schema); err != nil {
		t.Fatalf("input_schema missing or invalid: %v", err)
	}
	if schema.Type != "object" {
		t.Errorf("input_schema.type=%q want=object", schema.Type)
	}
	for _, key := range []string{"reasoning_effort", "max_completion_tokens"} {
		if _, ok := fields[key]; ok {
			t.Errorf("OpenAI-only option %q sent to native endpoint", key)
		}
	}
	if strings.Contains(string(request.Body), "fixture-secret") {
		t.Error("API key appeared in native request body")
	}
	if ledger.usage.TokensIn != 12 || ledger.usage.TokensOut != 3 {
		t.Fatalf("usage=%+v want in=12 out=3", ledger.usage)
	}
	if want := 12.0/1e6*1 + 3.0/1e6*2; math.Abs(ledger.usage.CostUSD-want) > 1e-12 {
		t.Fatalf("cost=%g want=%g", ledger.usage.CostUSD, want)
	}
}

// TestAnthropicTwoToolConversationUsesReturnedIDs replays a synthetic
// multi-step native conversation and checks that tool_use IDs and matching
// tool_result blocks survive every round trip.
func TestAnthropicTwoToolConversationUsesReturnedIDs(t *testing.T) {
	fixtures := protocolLoadFixtures(t, "testdata/protocol/anthropic-two-tool-loop.json")
	recorder := &protocolRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(t, r)
		index := recorder.count() - 1
		if index >= len(fixtures) {
			t.Errorf("unexpected request %d", index+1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write(fixtures[index])
	}))
	defer server.Close()

	ledger := &memoryLedger{}
	client := protocolNewAnthropicClient(t, server.URL+"/v1", ledger, llm.Options{MaxCompletionTokens: 512})
	tools := []llm.Tool{
		{Type: "function", Function: llm.Function{Name: "lookup_seed", Description: "Return a seed.", Parameters: json.RawMessage(`{"type":"object","properties":{"label":{"type":"string"}},"required":["label"]}`)}},
		{Type: "function", Function: llm.Function{Name: "append_marker", Description: "Append a marker.", Parameters: json.RawMessage(`{"type":"object","properties":{"seed":{"type":"string"}},"required":["seed"]}`)}},
	}
	var calls []llm.ToolCall
	result, err := client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "look up and mark"}}, tools, llm.DispatchFunc(func(_ context.Context, call llm.ToolCall) (string, error) {
		calls = append(calls, call)
		switch call.Function.Name {
		case "lookup_seed":
			return `{"seed":"seed-demo"}`, nil
		case "append_marker":
			return `{"value":"seed-demo-ok"}`, nil
		default:
			return "", fmt.Errorf("unexpected fake tool %q", call.Function.Name)
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "DONE" || result.Steps != 3 {
		t.Fatalf("result=%+v", result)
	}
	if len(calls) != 2 || calls[0].ID != "toolu_lookup" || calls[0].Function.Name != "lookup_seed" || calls[1].ID != "toolu_marker" || calls[1].Function.Name != "append_marker" {
		t.Fatalf("dispatched calls=%+v", calls)
	}

	requests := recorder.all()
	if len(requests) != 3 {
		t.Fatalf("requests=%d want=3", len(requests))
	}
	for i, request := range requests {
		if request.Path != "/v1/messages" {
			t.Errorf("request %d path=%q", i+1, request.Path)
		}
		if got := request.Header.Get("x-api-key"); got != "fixture-secret" {
			t.Errorf("request %d x-api-key=%q", i+1, got)
		}
	}

	second, _ := protocolDecodeNativeRequest(t, requests[1])
	if protocolMessagesContainRole(second.Messages, "system") || protocolMessagesContainRole(second.Messages, "tool") {
		t.Errorf("second request used non-native roles: %+v", second.Messages)
	}
	uses := protocolToolUses(t, second.Messages)
	if len(uses) != 1 || uses[0].ID != "toolu_lookup" || uses[0].Name != "lookup_seed" {
		t.Fatalf("second request tool_use=%+v", uses)
	}
	if !protocolJSONEqual(t, uses[0].Input, `{"label":"demo"}`) {
		t.Errorf("lookup input=%s", uses[0].Input)
	}
	results := protocolToolResults(t, second.Messages)
	if len(results) != 1 || results[0].ToolUseID != "toolu_lookup" {
		t.Fatalf("second request tool_result=%+v", results)
	}
	if !strings.Contains(protocolText(t, results[0].Content), "seed-demo") {
		t.Errorf("lookup result lost content: %s", results[0].Content)
	}
	if results[0].IsError {
		t.Error("successful lookup result marked as an error")
	}

	third, _ := protocolDecodeNativeRequest(t, requests[2])
	uses = protocolToolUses(t, third.Messages)
	results = protocolToolResults(t, third.Messages)
	if len(uses) != 2 || len(results) != 2 {
		t.Fatalf("third request uses=%+v results=%+v", uses, results)
	}
	wantIDs := map[string]bool{"toolu_lookup": false, "toolu_marker": false}
	for _, use := range uses {
		if _, ok := wantIDs[use.ID]; !ok {
			t.Errorf("unexpected tool_use ID %q", use.ID)
			continue
		}
		wantIDs[use.ID] = true
	}
	for id, seen := range wantIDs {
		if !seen {
			t.Errorf("tool_use ID %q missing from third request", id)
		}
	}
	seenResults := map[string]bool{}
	for _, result := range results {
		seenResults[result.ToolUseID] = true
		if result.IsError {
			t.Errorf("successful result %q marked as error", result.ToolUseID)
		}
	}
	for id := range wantIDs {
		if !seenResults[id] {
			t.Errorf("tool_result for %q missing from third request", id)
		}
	}
	for _, request := range requests {
		if strings.Contains(string(request.Body), `"tool_calls"`) {
			t.Error("OpenAI tool_calls shape sent to native endpoint")
		}
	}

	if ledger.usage.TokensIn != 60 || ledger.usage.TokensOut != 12 {
		t.Fatalf("usage=%+v want in=60 out=12", ledger.usage)
	}
	if want := 60.0/1e6*1 + 12.0/1e6*2; math.Abs(ledger.usage.CostUSD-want) > 1e-12 {
		t.Fatalf("cost=%g want=%g", ledger.usage.CostUSD, want)
	}
}

// TestAnthropicToolErrorsBecomeNativeToolResults checks that unknown tools,
// invalid input and dispatcher failures become user-role tool_result blocks
// marked is_error, and that a failed tool side effect is never retried.
func TestAnthropicToolErrorsBecomeNativeToolResults(t *testing.T) {
	first := []byte(`{"id":"msg_tool_errors","type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_unknown","name":"missing_tool","input":{}},{"type":"tool_use","id":"toolu_bad_input","name":"echo","input":[1,2]},{"type":"tool_use","id":"toolu_failed","name":"echo","input":{"text":"x"}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`)
	second := protocolAnthropicFinal("recovered", 20, 5)
	recorder := &protocolRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(t, r)
		if recorder.count() == 1 {
			w.Write(first)
			return
		}
		w.Write(second)
	}))
	defer server.Close()

	ledger := &memoryLedger{}
	client := protocolNewAnthropicClient(t, server.URL+"/v1", ledger, llm.Options{})
	tools := []llm.Tool{{Type: "function", Function: llm.Function{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}}}
	var calls []llm.ToolCall
	result, err := client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, tools, llm.DispatchFunc(func(_ context.Context, call llm.ToolCall) (string, error) {
		calls = append(calls, call)
		return "", errors.New("blocked by fake guard: fixture-secret private detail")
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "recovered" || result.Steps != 4 {
		t.Fatalf("result=%+v", result)
	}
	if len(calls) != 1 || calls[0].ID != "toolu_failed" {
		t.Fatalf("dispatched calls=%+v; failed side effect must not be retried", calls)
	}

	requests := recorder.all()
	if len(requests) != 2 {
		t.Fatalf("requests=%d want=2", len(requests))
	}
	body, _ := protocolDecodeNativeRequest(t, requests[1])
	results := protocolToolResults(t, body.Messages)
	if len(results) != 3 {
		t.Fatalf("tool results=%+v want 3", results)
	}
	for _, id := range []string{"toolu_unknown", "toolu_bad_input", "toolu_failed"} {
		result, ok := protocolFindToolResult(results, id)
		if !ok {
			t.Errorf("missing tool_result for %q", id)
			continue
		}
		if !result.IsError {
			t.Errorf("tool_result for %q not marked is_error", id)
		}
		if text := protocolText(t, result.Content); !strings.Contains(text, "error") {
			t.Errorf("tool_result for %q lacks safe error text: %q", id, text)
		}
	}
	for _, message := range body.Messages {
		for _, block := range protocolContentBlocks(t, message.Content) {
			if block.Type == "tool_result" && message.Role != "user" {
				t.Errorf("tool_result in %q role, want user", message.Role)
			}
		}
	}
	if strings.Contains(string(requests[1].Body), "fixture-secret") || strings.Contains(string(requests[1].Body), "private detail") {
		t.Error("tool failure detail leaked into the model conversation")
	}
}

// TestAnthropicUsageSumsIntoExistingLedger verifies native input_tokens and
// output_tokens are normalized into the existing Usage/ledger contract.
func TestAnthropicUsageSumsIntoExistingLedger(t *testing.T) {
	first := []byte(`{"id":"m1","type":"message","role":"assistant","content":[{"type":"tool_use","id":"u1","name":"echo","input":{"text":"x"}}],"stop_reason":"tool_use","usage":{"input_tokens":7,"output_tokens":2}}`)
	second := protocolAnthropicFinal("done", 11, 5)
	recorder := &protocolRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(t, r)
		if recorder.count() == 1 {
			w.Write(first)
			return
		}
		w.Write(second)
	}))
	defer server.Close()

	ledger := &memoryLedger{}
	client := protocolNewAnthropicClient(t, server.URL+"/v1", ledger, llm.Options{Now: func() time.Time {
		return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	}})
	tools := []llm.Tool{{Type: "function", Function: llm.Function{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}}}
	result, err := client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, tools, llm.DispatchFunc(func(context.Context, llm.ToolCall) (string, error) {
		return "ok", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "done" || result.Steps != 2 {
		t.Fatalf("result=%+v", result)
	}
	if len(ledger.records) != 2 {
		t.Fatalf("records=%+v want 2", ledger.records)
	}
	if ledger.records[0].TokensIn != 7 || ledger.records[0].TokensOut != 2 || ledger.records[1].TokensIn != 11 || ledger.records[1].TokensOut != 5 {
		t.Fatalf("records=%+v", ledger.records)
	}
	if ledger.usage.TokensIn != 18 || ledger.usage.TokensOut != 7 {
		t.Fatalf("summed usage=%+v want in=18 out=7", ledger.usage)
	}
	if want := 18.0/1e6*1 + 7.0/1e6*2; math.Abs(ledger.usage.CostUSD-want) > 1e-12 {
		t.Fatalf("cost=%g want=%g", ledger.usage.CostUSD, want)
	}
	for _, day := range ledger.days {
		if day != "2026-11-11" {
			t.Errorf("ledger day=%q", day)
		}
	}
}

func TestAnthropicRetryThenSuccessAndExhaustion(t *testing.T) {
	for _, tc := range []struct {
		name        string
		statuses    []int
		wantDelays  []time.Duration
		wantSuccess bool
	}{
		{name: "retry then success", statuses: []int{429, 500, 200}, wantDelays: []time.Duration{time.Second, 2 * time.Second}, wantSuccess: true},
		{name: "retry exhausted", statuses: []int{503, 503, 503}, wantDelays: []time.Duration{time.Second, 2 * time.Second}},
		{name: "client error no retry", statuses: []int{400}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &protocolRecorder{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				recorder.record(t, r)
				index := recorder.count() - 1
				if index >= len(tc.statuses) {
					t.Errorf("unexpected retry %d", index+1)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				status := tc.statuses[index]
				w.WriteHeader(status)
				if status == http.StatusOK {
					w.Write(protocolAnthropicFinal("ok", 9, 4))
					return
				}
				w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"private provider detail fixture-secret"}}`))
			}))
			defer server.Close()

			ledger := &memoryLedger{}
			var delays []time.Duration
			client := protocolNewAnthropicClient(t, server.URL+"/v1", ledger, llm.Options{Sleep: func(_ context.Context, delay time.Duration) error {
				delays = append(delays, delay)
				return nil
			}})
			result, err := client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, nil, nil)
			requests := recorder.all()
			if len(requests) != len(tc.statuses) {
				t.Fatalf("requests=%d want=%d", len(requests), len(tc.statuses))
			}
			for i, request := range requests {
				if request.Path != "/v1/messages" {
					t.Errorf("request %d path=%q", i+1, request.Path)
				}
			}
			if ledger.reads != len(tc.statuses) {
				t.Errorf("budget checks=%d want=%d", ledger.reads, len(tc.statuses))
			}
			if len(delays) != len(tc.wantDelays) {
				t.Fatalf("delays=%v want=%v", delays, tc.wantDelays)
			}
			for i := range delays {
				if delays[i] != tc.wantDelays[i] {
					t.Errorf("delay %d=%v want=%v", i, delays[i], tc.wantDelays[i])
				}
			}
			if tc.wantSuccess {
				if err != nil || result.Content != "ok" || len(ledger.records) != 1 {
					t.Fatalf("result=%+v records=%+v err=%v", result, ledger.records, err)
				}
				if ledger.usage.TokensIn != 9 || ledger.usage.TokensOut != 4 {
					t.Errorf("usage=%+v", ledger.usage)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), "private provider detail") || strings.Contains(err.Error(), "fixture-secret") {
				t.Fatalf("provider error body leaked into returned error: %v", err)
			}
			if len(ledger.records) != 0 {
				t.Errorf("failed requests recorded usage: %+v", ledger.records)
			}
		})
	}
}

func TestAnthropicTimeoutAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
		w.Write(protocolAnthropicFinal("late", 1, 1))
	}))
	defer server.Close()

	calls := 0
	dispatcher := llm.DispatchFunc(func(context.Context, llm.ToolCall) (string, error) {
		calls++
		return "ok", nil
	})
	client := protocolNewAnthropicClient(t, server.URL+"/v1", &memoryLedger{}, llm.Options{RequestTimeout: time.Millisecond})
	_, err := client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, nil, dispatcher)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Run(ctx, "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, nil, dispatcher)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("dispatches=%d want=0", calls)
	}
}

func TestAnthropicMalformedResponses(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "not json", body: `not json`},
		{name: "missing usage", body: `{"role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`},
		{name: "incomplete usage", body: `{"role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10}}`},
		{name: "negative usage", body: `{"role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":-1,"output_tokens":2}}`},
		{name: "missing content", body: `{"role":"assistant","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`},
		{name: "wrong role", body: `{"role":"user","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`},
		{name: "duplicate tool IDs", body: `{"role":"assistant","content":[{"type":"tool_use","id":"dup","name":"echo","input":{}},{"type":"tool_use","id":"dup","name":"echo","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":2}}`},
		{name: "empty tool ID", body: `{"role":"assistant","content":[{"type":"tool_use","id":"","name":"echo","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":2}}`},
		{name: "truncated final stop reason", body: `{"role":"assistant","content":[{"type":"text","text":"partial"}],"stop_reason":"max_tokens","usage":{"input_tokens":10,"output_tokens":2}}`},
		{name: "tool use without tool_use stop reason", body: `{"role":"assistant","content":[{"type":"tool_use","id":"one","name":"echo","input":{}}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &protocolRecorder{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				recorder.record(t, r)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()

			calls := 0
			client := protocolNewAnthropicClient(t, server.URL+"/v1", &memoryLedger{}, llm.Options{})
			_, err := client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, []llm.Tool{{Type: "function", Function: llm.Function{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}}}, llm.DispatchFunc(func(context.Context, llm.ToolCall) (string, error) {
				calls++
				return "ok", nil
			}))
			if err == nil {
				t.Fatal("malformed native response accepted")
			}
			if calls != 0 {
				t.Fatalf("dispatches=%d want=0", calls)
			}
			if recorder.count() != 1 {
				t.Fatalf("requests=%d want=1", recorder.count())
			}
		})
	}
}

func TestAnthropicOutputStepAndBudgetLimits(t *testing.T) {
	t.Run("configured max_tokens", func(t *testing.T) {
		recorder := &protocolRecorder{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recorder.record(t, r)
			w.Write(protocolAnthropicFinal("ok", 1, 1))
		}))
		defer server.Close()
		client := protocolNewAnthropicClient(t, server.URL+"/v1", &memoryLedger{}, llm.Options{MaxCompletionTokens: 321})
		if _, err := client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, nil, nil); err != nil {
			t.Fatal(err)
		}
		requests := recorder.all()
		if len(requests) != 1 {
			t.Fatalf("requests=%d", len(requests))
		}
		body, fields := protocolDecodeNativeRequest(t, requests[0])
		if body.MaxTokens != 321 {
			t.Errorf("max_tokens=%d want=321", body.MaxTokens)
		}
		for _, key := range []string{"reasoning_effort", "max_completion_tokens"} {
			if _, ok := fields[key]; ok {
				t.Errorf("OpenAI-only option %q sent to native endpoint", key)
			}
		}
	})

	t.Run("bounded default max_tokens", func(t *testing.T) {
		recorder := &protocolRecorder{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recorder.record(t, r)
			w.Write(protocolAnthropicFinal("ok", 1, 1))
		}))
		defer server.Close()
		client := protocolNewAnthropicClient(t, server.URL+"/v1", &memoryLedger{}, llm.Options{})
		if _, err := client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, nil, nil); err != nil {
			t.Fatal(err)
		}
		body, _ := protocolDecodeNativeRequest(t, recorder.all()[0])
		if body.MaxTokens != 1024 {
			t.Errorf("default max_tokens=%d want=1024", body.MaxTokens)
		}
	})

	t.Run("step limit", func(t *testing.T) {
		recorder := &protocolRecorder{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recorder.record(t, r)
			w.Write([]byte(`{"role":"assistant","content":[{"type":"tool_use","id":"one","name":"echo","input":{}},{"type":"tool_use","id":"two","name":"echo","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":5,"output_tokens":5}}`))
		}))
		defer server.Close()
		calls := 0
		client, err := llm.New(config.LLMConfig{
			Protocol: "anthropic", BaseURL: server.URL + "/v1", Model: "native-model",
			APIKey: config.NewSecret("fixture-secret"), PricePerMTokInUSD: 1, PricePerMTokOutUSD: 1,
		}, config.LimitsConfig{MaxStepsPerWake: 1, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		result, err := client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, []llm.Tool{{Type: "function", Function: llm.Function{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}}}, llm.DispatchFunc(func(context.Context, llm.ToolCall) (string, error) {
			calls++
			return "ok", nil
		}))
		if !errors.Is(err, llm.ErrStepLimit) {
			t.Fatalf("step limit error: %v", err)
		}
		if result.Steps != 1 || calls != 1 || recorder.count() != 1 {
			t.Fatalf("steps=%d calls=%d requests=%d", result.Steps, calls, recorder.count())
		}
	})

	t.Run("budget limit", func(t *testing.T) {
		recorder := &protocolRecorder{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recorder.record(t, r)
			w.Write(protocolAnthropicFinal("unexpected", 1, 1))
		}))
		defer server.Close()
		ledger := &memoryLedger{usage: llm.Usage{CostUSD: 1}}
		client, err := llm.New(config.LLMConfig{
			Protocol: "anthropic", BaseURL: server.URL + "/v1", Model: "native-model",
			APIKey: config.NewSecret("fixture-secret"), PricePerMTokInUSD: 1, PricePerMTokOutUSD: 1,
		}, config.LimitsConfig{MaxStepsPerWake: 2, MaxUSDPerDay: 1}, ledger, llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, nil, nil)
		if !errors.Is(err, llm.ErrBudget) {
			t.Fatalf("budget error: %v", err)
		}
		if recorder.count() != 0 {
			t.Fatalf("requests=%d want=0", recorder.count())
		}
	})
}

func TestAnthropicErrorsDoNotLeakSecrets(t *testing.T) {
	t.Run("provider error body", func(t *testing.T) {
		recorder := &protocolRecorder{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recorder.record(t, r)
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid key fixture-secret private detail"}}`))
		}))
		defer server.Close()
		client := protocolNewAnthropicClient(t, server.URL+"/v1", &memoryLedger{}, llm.Options{})
		_, err := client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, nil, nil)
		if err == nil {
			t.Fatal("expected provider error")
		}
		if strings.Contains(err.Error(), "fixture-secret") || strings.Contains(err.Error(), "private detail") {
			t.Fatalf("provider error body leaked: %v", err)
		}
		requests := recorder.all()
		if len(requests) != 1 {
			t.Fatalf("requests=%d want=1", len(requests))
		}
		if strings.Contains(string(requests[0].Body), "fixture-secret") {
			t.Error("API key appeared in request body")
		}
	})

	t.Run("dispatcher error", func(t *testing.T) {
		first := []byte(`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_secret","name":"echo","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":2}}`)
		recorder := &protocolRecorder{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recorder.record(t, r)
			if recorder.count() == 1 {
				w.Write(first)
				return
			}
			w.Write(protocolAnthropicFinal("recovered", 5, 2))
		}))
		defer server.Close()
		client := protocolNewAnthropicClient(t, server.URL+"/v1", &memoryLedger{}, llm.Options{})
		result, err := client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "go"}}, []llm.Tool{{Type: "function", Function: llm.Function{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}}}, llm.DispatchFunc(func(context.Context, llm.ToolCall) (string, error) {
			return "", errors.New("blocked by fake guard: fixture-secret private detail")
		}))
		if err != nil || result.Content != "recovered" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		requests := recorder.all()
		if len(requests) != 2 {
			t.Fatalf("requests=%d want=2", len(requests))
		}
		for _, request := range requests {
			if strings.Contains(string(request.Body), "fixture-secret") || strings.Contains(string(request.Body), "private detail") {
				t.Error("tool failure detail leaked into a model request")
			}
		}
		for _, message := range result.Messages {
			if strings.Contains(message.Content, "fixture-secret") || strings.Contains(message.Content, "private detail") {
				t.Errorf("tool failure detail leaked into returned messages: %+v", message)
			}
		}
		body, _ := protocolDecodeNativeRequest(t, requests[1])
		results := protocolToolResults(t, body.Messages)
		if len(results) != 1 || !results[0].IsError {
			t.Fatalf("failed tool result=%+v want one is_error block", results)
		}
	})
}
