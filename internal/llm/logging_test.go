package llm_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/logging"
)

// Fake values planted in prompts, tool payloads, provider bodies and ledger
// errors. None may reach operational logs at any level.
const (
	loggingFakeKey          = "sk-logging-fake-secret-key-123"
	loggingFakeHeader       = "Bearer logging-header-secret-456"
	loggingFakePrompt       = "private-prompt-do-not-log-111"
	loggingFakeNote         = "private-note-do-not-log-222"
	loggingFakeUsername     = "stranger-username-do-not-log-333"
	loggingFakeProviderBody = "raw-provider-body-do-not-log-444"
	loggingFakeProviderID   = "arbitrary-provider-request-id-do-not-log-555"
	loggingFakeUnknownTool  = "unknown-tool-name-do-not-log-666"
	loggingFakeToolArgs     = "private-tool-arguments-do-not-log-777"
	loggingFakeToolResult   = "private-tool-result-do-not-log-888"
	loggingFakeLedgerError  = "ledger-error-do-not-log-999"
)

type loggingRecord map[string]any

var loggingSafeCode = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)

// loggingAllowedFields is the metadata allowlist in the issue's event contract.
// Standard slog keys plus the documented correlation fields are allowed; any
// other key is a redaction bug by construction.
var loggingAllowedFields = map[string]bool{
	"time": true, "level": true, "msg": true, "event": true,
	"agent_id": true, "phase": true, "wake_id": true, "provider_id": true, "profile_id": true,
	"model": true, "protocol": true, "operation": true, "request": true, "attempt": true,
	"http_status": true, "duration_ms": true, "tokens_in": true, "tokens_out": true,
	"cost_usd": true, "outcome": true, "reason": true, "steps": true,
	"tool": true, "tool_call_id": true,
}

func loggingTestLogger(t *testing.T, level string) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger, err := logging.New(&buf, level, "json")
	if err != nil {
		t.Fatalf("logging.New(%q): %v", level, err)
	}
	return logger, &buf
}

func loggingRecords(t *testing.T, buf *bytes.Buffer) []loggingRecord {
	t.Helper()
	var records []loggingRecord
	dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	for {
		var record loggingRecord
		err := dec.Decode(&record)
		if errors.Is(err, io.EOF) {
			return records
		}
		if err != nil {
			t.Fatalf("decoding captured JSON log: %v; output %q", err, buf.String())
		}
		records = append(records, record)
	}
}

func loggingEvents(records []loggingRecord, event string) []loggingRecord {
	var found []loggingRecord
	for _, record := range records {
		if record["event"] == event {
			found = append(found, record)
		}
	}
	return found
}

func loggingOneEvent(t *testing.T, records []loggingRecord, event string) loggingRecord {
	t.Helper()
	found := loggingEvents(records, event)
	if len(found) != 1 {
		t.Fatalf("event %q appeared %d times, want once; records: %v", event, len(found), records)
	}
	return found[0]
}

func loggingStringField(t *testing.T, record loggingRecord, field string) string {
	t.Helper()
	value, ok := record[field]
	if !ok {
		t.Fatalf("event %v has no %q field", record["event"], field)
	}
	text, ok := value.(string)
	if !ok {
		t.Fatalf("event %v field %q = %v is not a string", record["event"], field, value)
	}
	return text
}

func loggingNumberField(t *testing.T, record loggingRecord, field string) float64 {
	t.Helper()
	value, ok := record[field]
	if !ok {
		t.Fatalf("event %v has no %q field", record["event"], field)
	}
	number, ok := value.(float64)
	if !ok {
		t.Fatalf("event %v field %q = %v is not a number", record["event"], field, value)
	}
	return number
}

func loggingSemantic(record loggingRecord) string {
	return strings.Join([]string{
		loggingMaybeString(record["outcome"]),
		loggingMaybeString(record["reason"]),
	}, " ")
}

func loggingMaybeString(value any) string {
	text, _ := value.(string)
	return text
}

// loggingAssertSafeRecords enforces the redaction contract: allowlisted fields,
// stable code-shaped reasons/outcomes and none of the planted fake values.
func loggingAssertSafeRecords(t *testing.T, buf *bytes.Buffer, records []loggingRecord, forbidden ...string) {
	t.Helper()
	for _, record := range records {
		for field := range record {
			if !loggingAllowedFields[field] {
				t.Errorf("event %v logged unexpected field %q", record["event"], field)
			}
		}
		for _, field := range []string{"reason", "outcome"} {
			value, ok := record[field]
			if !ok {
				continue
			}
			text, ok := value.(string)
			if !ok {
				t.Errorf("event %v field %q = %v is not a string", record["event"], field, value)
				continue
			}
			if text != "" && !loggingSafeCode.MatchString(text) {
				t.Errorf("event %v field %q = %v is not a safe stable code", record["event"], field, value)
			}
		}
	}

	allForbidden := append([]string{
		loggingFakeKey,
		loggingFakeHeader,
		loggingFakePrompt,
		loggingFakeNote,
		loggingFakeUsername,
		loggingFakeProviderBody,
		loggingFakeProviderID,
		loggingFakeUnknownTool,
		loggingFakeToolArgs,
		loggingFakeToolResult,
		loggingFakeLedgerError,
		"Authorization",
	}, forbidden...)
	output := buf.String()
	for _, value := range allForbidden {
		if value == "" {
			continue
		}
		if strings.Contains(output, value) {
			t.Errorf("captured log leaked forbidden value %q: %s", value, output)
		}
	}
}

func loggingWriteJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encoding fake provider response: %v", err)
	}
}

func loggingAssistant(content string) map[string]any {
	return map[string]any{"message": llm.Message{Role: "assistant", Content: content}, "finish_reason": "stop"}
}

func loggingToolResponse(t *testing.T, calls ...llm.ToolCall) map[string]any {
	t.Helper()
	return map[string]any{"choices": []any{map[string]any{"message": llm.Message{Role: "assistant", ToolCalls: calls}}}, "usage": map[string]int64{"prompt_tokens": 10, "completion_tokens": 2}}
}

func loggingTool(name, arguments string) llm.Tool {
	return llm.Tool{Type: "function", Function: llm.Function{Name: name, Parameters: json.RawMessage(`{"type":"object"}`)}}
}

func loggingClient(t *testing.T, server *httptest.Server, ledger llm.Ledger, opts llm.Options) *llm.Client {
	t.Helper()
	if opts.ProviderID == "" {
		opts.ProviderID = "configured-provider"
	}
	if opts.ProfileID == "" {
		opts.ProfileID = "configured-profile"
	}
	client, err := llm.New(
		config.LLMConfig{
			Protocol: "openai", BaseURL: server.URL, Model: "fake-metadata-model",
			APIKey: config.NewSecret(loggingFakeKey), PricePerMTokInUSD: 1, PricePerMTokOutUSD: 2,
		},
		config.LimitsConfig{MaxStepsPerWake: 5, MaxUSDPerDay: 1},
		ledger,
		opts,
	)
	if err != nil {
		t.Fatalf("llm.New: %v", err)
	}
	return client
}

func loggingMessages() []llm.Message {
	return []llm.Message{{
		Role:    "user",
		Content: loggingFakePrompt + " note=" + loggingFakeNote + " username=" + loggingFakeUsername,
	}}
}

func TestLoggingSuccessfulToolLoopCorrelatesEvents(t *testing.T) {
	logger, buf := loggingTestLogger(t, "debug")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.Header.Get("Authorization"); got != "Bearer "+loggingFakeKey {
			t.Errorf("request authorization = %q", got)
		}
		if requests == 1 {
			args, err := json.Marshal(map[string]string{"text": loggingFakeToolArgs})
			if err != nil {
				t.Fatal(err)
			}
			loggingWriteJSON(t, w, map[string]any{
				"id": loggingFakeProviderID,
				"choices": []any{map[string]any{"message": llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{{
						ID: "call-correlated-1", Type: "function",
						Function: llm.CallFunction{Name: "web_search", Arguments: string(args)},
					}},
				}}},
				"usage": map[string]int64{"prompt_tokens": 120, "completion_tokens": 30},
			})
			return
		}
		loggingWriteJSON(t, w, map[string]any{
			"choices": []any{loggingAssistant("done")},
			"usage":   map[string]int64{"prompt_tokens": 80, "completion_tokens": 10},
		})
	}))
	defer server.Close()

	ledger := &memoryLedger{}
	client := loggingClient(t, server, ledger, llm.Options{Logger: logger})
	result, err := client.Run(context.Background(), "agent-logging", 42, loggingMessages(),
		[]llm.Tool{loggingTool("web_search", `{"type":"object"}`)},
		llm.DispatchFunc(func(_ context.Context, call llm.ToolCall) (string, error) {
			if call.ID != "call-correlated-1" {
				t.Errorf("dispatched tool call id = %q", call.ID)
			}
			return loggingFakeToolResult, nil
		}))
	if err != nil || result.Content != "done" || result.Steps != 2 {
		t.Fatalf("result = %+v, err = %v", result, err)
	}

	records := loggingRecords(t, buf)
	started := loggingOneEvent(t, records, "llm.run.started")
	if got := loggingStringField(t, started, "agent_id"); got != "agent-logging" {
		t.Errorf("run started agent_id = %q", got)
	}
	if got := loggingNumberField(t, started, "wake_id"); got != 42 {
		t.Errorf("run started wake_id = %v, want 42", got)
	}
	if got := loggingStringField(t, started, "provider_id"); got != "configured-provider" {
		t.Errorf("run started provider_id = %q", got)
	}
	if got := loggingStringField(t, started, "profile_id"); got != "configured-profile" {
		t.Errorf("run started profile_id = %q", got)
	}
	if got := loggingStringField(t, started, "model"); got != "fake-metadata-model" {
		t.Errorf("run started model = %q", got)
	}

	if got := len(loggingEvents(records, "llm.request.started")); got != 2 {
		t.Errorf("llm.request.started count = %d, want 2", got)
	}
	if got := len(loggingEvents(records, "llm.request.finished")); got < 2 {
		t.Errorf("llm.request.finished count = %d, want at least 2", got)
	}
	for _, record := range loggingEvents(records, "llm.request.finished") {
		if got := loggingNumberField(t, record, "http_status"); got != http.StatusOK {
			t.Errorf("request finished http_status = %v, want 200", got)
		}
	}

	usage := loggingEvents(records, "llm.usage.recorded")
	if len(usage) != 2 {
		t.Fatalf("llm.usage.recorded count = %d, want 2", len(usage))
	}
	var tokensIn, tokensOut, cost float64
	for _, record := range usage {
		tokensIn += loggingNumberField(t, record, "tokens_in")
		tokensOut += loggingNumberField(t, record, "tokens_out")
		cost += loggingNumberField(t, record, "cost_usd")
	}
	if tokensIn != 200 || tokensOut != 40 {
		t.Errorf("logged usage tokens = %v/%v, want 200/40", tokensIn, tokensOut)
	}
	// Synthetic prices from the fake config: $1/Mtok in and $2/Mtok out.
	const wantCost = 200.0/1e6*1 + 40.0/1e6*2
	if math.Abs(cost-wantCost) > 1e-12 {
		t.Errorf("logged usage cost = %v, want %v", cost, wantCost)
	}

	tool := loggingOneEvent(t, records, "llm.tool.finished")
	if got := loggingStringField(t, tool, "tool"); got != "web_search" {
		t.Errorf("tool.finished tool = %q", got)
	}
	// Tool calls are correlated by the local wake/step identifier, never by an
	// identifier supplied in the provider response.
	if got := loggingStringField(t, tool, "tool_call_id"); got != "42/1" {
		t.Errorf("tool.finished tool_call_id = %q, want the local wake/step id 42/1", got)
	}

	finished := loggingOneEvent(t, records, "llm.run.finished")
	if got := loggingStringField(t, finished, "outcome"); got != "success" {
		t.Errorf("run.finished outcome = %q", got)
	}
	if got := loggingNumberField(t, finished, "steps"); got != 2 {
		t.Errorf("run.finished steps = %v, want 2", got)
	}
	if math.Abs(ledger.usage.CostUSD-wantCost) > 1e-12 {
		t.Errorf("ledger cost = %v, want %v", ledger.usage.CostUSD, wantCost)
	}

	loggingAssertSafeRecords(t, buf, records, server.URL)
}

func TestLoggingRetryThenSuccessAndExhaustion(t *testing.T) {
	t.Run("retry then success", func(t *testing.T) {
		logger, buf := loggingTestLogger(t, "debug")
		requests := 0
		var delays []time.Duration
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			if requests == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				loggingWriteJSON(t, w, map[string]any{"error": map[string]string{"message": loggingFakeProviderBody}})
				return
			}
			loggingWriteJSON(t, w, map[string]any{
				"choices": []any{loggingAssistant("ok")},
				"usage":   map[string]int64{"prompt_tokens": 7, "completion_tokens": 2},
			})
		}))
		defer server.Close()

		client := loggingClient(t, server, &memoryLedger{}, llm.Options{
			Logger: logger,
			Sleep: func(context.Context, time.Duration) error {
				delays = append(delays, time.Second)
				return nil
			},
		})
		result, err := client.Run(context.Background(), "agent-logging", 7, loggingMessages(), nil, nil)
		if err != nil || result.Content != "ok" {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
		if requests != 2 || len(delays) != 1 {
			t.Fatalf("requests = %d, delays = %v", requests, delays)
		}

		records := loggingRecords(t, buf)
		retries := loggingEvents(records, "llm.request.retry")
		if len(retries) != 1 {
			t.Fatalf("llm.request.retry count = %d, want 1", len(retries))
		}
		if got := loggingNumberField(t, retries[0], "attempt"); got != 1 {
			t.Errorf("retry attempt = %v, want 1", got)
		}
		if got := loggingNumberField(t, retries[0], "http_status"); got != http.StatusTooManyRequests {
			t.Errorf("retry http_status = %v, want 429", got)
		}
		if got := loggingOneEvent(t, records, "llm.run.finished"); loggingStringField(t, got, "outcome") != "success" {
			t.Errorf("run.finished outcome = %v", got["outcome"])
		}
		if got := len(loggingEvents(records, "llm.usage.recorded")); got != 1 {
			t.Errorf("llm.usage.recorded count = %d, want 1", got)
		}
		loggingAssertSafeRecords(t, buf, records, server.URL)
	})

	t.Run("retry exhaustion", func(t *testing.T) {
		logger, buf := loggingTestLogger(t, "debug")
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			w.WriteHeader(http.StatusServiceUnavailable)
			loggingWriteJSON(t, w, map[string]any{"error": map[string]string{"message": loggingFakeProviderBody}})
		}))
		defer server.Close()

		client := loggingClient(t, server, &memoryLedger{}, llm.Options{
			Logger: logger,
			Sleep:  func(context.Context, time.Duration) error { return nil },
		})
		if _, err := client.Run(context.Background(), "agent-logging", 8, loggingMessages(), nil, nil); err == nil {
			t.Fatal("retry exhaustion returned no error")
		}
		if requests != 3 {
			t.Fatalf("requests = %d, want 3", requests)
		}

		records := loggingRecords(t, buf)
		if got := len(loggingEvents(records, "llm.request.retry")); got != 2 {
			t.Errorf("llm.request.retry count = %d, want 2", got)
		}
		if got := len(loggingEvents(records, "llm.run.finished")); got != 0 {
			t.Errorf("run finished after exhaustion %d times, want 0", got)
		}
		stopped := loggingOneEvent(t, records, "llm.run.stopped")
		if loggingSemantic(stopped) == "" {
			t.Errorf("run.stopped has no outcome or reason: %v", stopped)
		}
		var lastAttempt bool
		for _, record := range records {
			if record["event"] == "llm.request.finished" && loggingNumberField(t, record, "attempt") == 3 && loggingNumberField(t, record, "http_status") == http.StatusServiceUnavailable {
				lastAttempt = true
			}
		}
		if !lastAttempt {
			t.Errorf("no final attempt record with http_status 503: %v", records)
		}
		loggingAssertSafeRecords(t, buf, records, server.URL)
	})
}

func TestLoggingMalformedResponsesStopSafely(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "invalid JSON", body: `not json`},
		{name: "incomplete usage", body: `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10}}`},
		{name: "no choices", body: `{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2}}`},
		{name: "missing usage", body: `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`},
		{name: "negative usage", body: `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":-1,"completion_tokens":2}}`},
		{name: "duplicate tool call id", body: `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"same","type":"function","function":{"name":"web_search","arguments":"{}"}},{"id":"same","type":"function","function":{"name":"web_search","arguments":"{}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`},
		{name: "unfinished response", body: `{"choices":[{"message":{"role":"assistant","content":"partial"},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger, buf := loggingTestLogger(t, "debug")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(tc.body))
			}))
			defer server.Close()

			client := loggingClient(t, server, &memoryLedger{}, llm.Options{Logger: logger})
			if _, err := client.Run(context.Background(), "agent-logging", 9, loggingMessages(), nil, nil); err == nil {
				t.Fatal("malformed response returned no error")
			}

			records := loggingRecords(t, buf)
			stopped := loggingOneEvent(t, records, "llm.run.stopped")
			if loggingSemantic(stopped) == "" {
				t.Errorf("run.stopped has no outcome or reason: %v", stopped)
			}
			if got := len(loggingEvents(records, "llm.run.finished")); got != 0 {
				t.Errorf("run.finished after malformed response = %d, want 0", got)
			}
			loggingAssertSafeRecords(t, buf, records, server.URL)
		})
	}
}

func TestLoggingMalformedToolCallsDoNotEchoPayloads(t *testing.T) {
	logger, buf := loggingTestLogger(t, "debug")
	requests := 0
	badArguments := `{"text":"` + loggingFakeToolArgs
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			loggingWriteJSON(t, w, loggingToolResponse(t,
				llm.ToolCall{ID: "call-unknown", Type: "function", Function: llm.CallFunction{Name: loggingFakeUnknownTool, Arguments: `{}`}},
				llm.ToolCall{ID: "call-bad-args", Type: "function", Function: llm.CallFunction{Name: "web_search", Arguments: badArguments}},
				llm.ToolCall{ID: "call-bad-type", Type: "not_function", Function: llm.CallFunction{Name: "web_search", Arguments: `{}`}},
				llm.ToolCall{ID: "call-dispatch", Type: "function", Function: llm.CallFunction{Name: "web_search", Arguments: `{}`}},
			))
			return
		}
		loggingWriteJSON(t, w, map[string]any{
			"choices": []any{loggingAssistant("recovered")},
			"usage":   map[string]int64{"prompt_tokens": 20, "completion_tokens": 5},
		})
	}))
	defer server.Close()

	client := loggingClient(t, server, &memoryLedger{}, llm.Options{Logger: logger, MaxSteps: 10})
	dispatches := 0
	result, err := client.Run(context.Background(), "agent-logging", 10, loggingMessages(),
		[]llm.Tool{loggingTool("web_search", `{"type":"object"}`)},
		wellFormed(llm.DispatchFunc(func(context.Context, llm.ToolCall) (string, error) {
			dispatches++
			return "", errors.New("fake guard rejected: " + loggingFakeToolResult)
		}), "web_search"))
	if err != nil || result.Content != "recovered" || result.Steps != 2 || dispatches != 1 {
		t.Fatalf("result = %+v dispatches = %d err = %v", result, dispatches, err)
	}

	records := loggingRecords(t, buf)
	tools := loggingEvents(records, "llm.tool.finished")
	if len(tools) != 4 {
		t.Fatalf("llm.tool.finished count = %d, want 4: %v", len(tools), records)
	}
	// The provider's call IDs are not logged; the client correlates the four
	// calls to wake 10, steps 1 through 4.
	wantIDs := map[string]bool{"10/1": true, "10/2": true, "10/3": true, "10/4": true}
	for _, record := range tools {
		id := loggingStringField(t, record, "tool_call_id")
		if !wantIDs[id] {
			t.Errorf("tool.finished unexpected tool_call_id %q", id)
		}
		delete(wantIDs, id)
		if name, ok := record["tool"]; ok && name == loggingFakeUnknownTool {
			t.Errorf("unknown tool name echoed into logs: %v", name)
		}
	}
	if len(wantIDs) != 0 {
		t.Errorf("tool.finished missing tool call IDs %v", wantIDs)
	}
	loggingAssertSafeRecords(t, buf, records, server.URL)
}

func TestLoggingBudgetAndStepStops(t *testing.T) {
	t.Run("budget", func(t *testing.T) {
		logger, buf := loggingTestLogger(t, "debug")
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			loggingWriteJSON(t, w, map[string]any{"choices": []any{loggingAssistant("unexpected")}})
		}))
		defer server.Close()

		ledger := &memoryLedger{usage: llm.Usage{CostUSD: 1}}
		client := loggingClient(t, server, ledger, llm.Options{Logger: logger})
		if _, err := client.Run(context.Background(), "agent-logging", 11, loggingMessages(), nil, nil); !errors.Is(err, llm.ErrBudget) {
			t.Fatalf("budget error = %v", err)
		}
		if requests != 0 {
			t.Fatalf("budget stop sent %d requests, want 0", requests)
		}

		records := loggingRecords(t, buf)
		stopped := loggingOneEvent(t, records, "llm.run.stopped")
		if semantic := loggingSemantic(stopped); !strings.Contains(semantic, "budget") {
			t.Errorf("budget stop semantic = %q, want a budget code", semantic)
		}
		loggingAssertSafeRecords(t, buf, records, server.URL)
	})

	t.Run("step limit", func(t *testing.T) {
		logger, buf := loggingTestLogger(t, "debug")
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			loggingWriteJSON(t, w, loggingToolResponse(t,
				llm.ToolCall{ID: "call-step", Type: "function", Function: llm.CallFunction{Name: "web_search", Arguments: `{}`}},
			))
		}))
		defer server.Close()

		client, err := llm.New(
			config.LLMConfig{BaseURL: server.URL, Model: "fake-metadata-model", APIKey: config.NewSecret(loggingFakeKey)},
			config.LimitsConfig{MaxStepsPerWake: 2, MaxUSDPerDay: 1},
			&memoryLedger{},
			llm.Options{Logger: logger, ProviderID: "configured-provider", ProfileID: "configured-profile"},
		)
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Run(context.Background(), "agent-logging", 12, loggingMessages(),
			[]llm.Tool{loggingTool("web_search", `{"type":"object"}`)},
			llm.DispatchFunc(func(context.Context, llm.ToolCall) (string, error) { return "ok", nil }))
		if !errors.Is(err, llm.ErrStepLimit) {
			t.Fatalf("step error = %v", err)
		}
		if requests != 2 {
			t.Fatalf("step limit requests = %d, want 2", requests)
		}

		records := loggingRecords(t, buf)
		stopped := loggingOneEvent(t, records, "llm.run.stopped")
		if semantic := loggingSemantic(stopped); !strings.Contains(semantic, "step") {
			t.Errorf("step stop semantic = %q, want a step code", semantic)
		}
		if got := len(loggingEvents(records, "llm.tool.finished")); got != 2 {
			t.Errorf("tool.finished count at step limit = %d, want 2", got)
		}
		loggingAssertSafeRecords(t, buf, records, server.URL)
	})
}

func TestLoggingCancellationStopsSafely(t *testing.T) {
	logger, buf := loggingTestLogger(t, "debug")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loggingWriteJSON(t, w, map[string]any{"choices": []any{loggingAssistant("unexpected")}})
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := loggingClient(t, server, &memoryLedger{}, llm.Options{Logger: logger})
	if _, err := client.Run(ctx, "agent-logging", 13, loggingMessages(), nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}

	records := loggingRecords(t, buf)
	stopped := loggingOneEvent(t, records, "llm.run.stopped")
	if semantic := loggingSemantic(stopped); !strings.Contains(semantic, "cancel") {
		t.Errorf("cancel stop semantic = %q, want a cancellation code", semantic)
	}
	loggingAssertSafeRecords(t, buf, records, server.URL)
}

func TestLoggingAccountingFailuresAreSanitized(t *testing.T) {
	for _, writeFailure := range []bool{false, true} {
		name := "ledger read failure"
		if writeFailure {
			name = "ledger write failure"
		}
		t.Run(name, func(t *testing.T) {
			logger, buf := loggingTestLogger(t, "debug")
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				loggingWriteJSON(t, w, map[string]any{
					"choices": []any{loggingAssistant("ok")},
					"usage":   map[string]int64{"prompt_tokens": 10, "completion_tokens": 2},
				})
			}))
			defer server.Close()

			ledger := &memoryLedger{readErr: errors.New("read: " + loggingFakeLedgerError)}
			if writeFailure {
				ledger.readErr = nil
				ledger.writeErr = errors.New("write: " + loggingFakeLedgerError)
			}
			client := loggingClient(t, server, ledger, llm.Options{Logger: logger})
			if _, err := client.Run(context.Background(), "agent-logging", 14, loggingMessages(), nil, nil); err == nil {
				t.Fatal("accounting failure returned no error")
			}
			wantRequests := 1
			if !writeFailure {
				wantRequests = 0
			}
			if requests != wantRequests {
				t.Errorf("requests = %d, want %d", requests, wantRequests)
			}

			records := loggingRecords(t, buf)
			stopped := loggingOneEvent(t, records, "llm.run.stopped")
			if loggingSemantic(stopped) == "" {
				t.Errorf("run.stopped has no outcome or reason: %v", stopped)
			}
			loggingAssertSafeRecords(t, buf, records, server.URL)
		})
	}
}

func TestLoggingRedactionHoldsAtDebugAndInfo(t *testing.T) {
	for _, level := range []string{"debug", "info"} {
		t.Run(level, func(t *testing.T) {
			logger, buf := loggingTestLogger(t, level)
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("X-Provider-Secret", loggingFakeHeader)
				switch requests {
				case 1:
					w.WriteHeader(http.StatusTooManyRequests)
					loggingWriteJSON(t, w, map[string]any{
						"error":             map[string]string{"message": loggingFakeProviderBody},
						"provider_id":       loggingFakeProviderID,
						"authorization":     loggingFakeHeader,
						"private_prompt":    loggingFakePrompt,
						"private_note":      loggingFakeNote,
						"stranger_username": loggingFakeUsername,
					})
				case 2:
					loggingWriteJSON(t, w, map[string]any{
						"id": loggingFakeProviderID,
						"choices": []any{map[string]any{"message": llm.Message{
							Role: "assistant",
							ToolCalls: []llm.ToolCall{
								{ID: "call-unknown", Type: "function", Function: llm.CallFunction{Name: loggingFakeUnknownTool, Arguments: `{"` + loggingFakeToolArgs + `":true}`}},
								{ID: "call-known", Type: "function", Function: llm.CallFunction{Name: "web_search", Arguments: `{"text":"` + loggingFakeToolArgs + `"}`}},
							},
						}}},
						"usage": map[string]int64{"prompt_tokens": 11, "completion_tokens": 3},
					})
				default:
					loggingWriteJSON(t, w, map[string]any{
						"choices": []any{loggingAssistant("done")},
						"usage":   map[string]int64{"prompt_tokens": 5, "completion_tokens": 1},
					})
				}
			}))
			defer server.Close()

			client := loggingClient(t, server, &memoryLedger{}, llm.Options{
				Logger: logger,
				Sleep:  func(context.Context, time.Duration) error { return nil },
			})
			result, err := client.Run(context.Background(), "agent-logging", 15, loggingMessages(),
				[]llm.Tool{loggingTool("web_search", `{"type":"object"}`)},
				llm.DispatchFunc(func(context.Context, llm.ToolCall) (string, error) {
					return "", errors.New("fake guard rejected: " + loggingFakeToolResult)
				}))
			if err != nil || result.Content != "done" {
				t.Fatalf("result = %+v err = %v", result, err)
			}

			records := loggingRecords(t, buf)
			if len(records) == 0 {
				t.Fatal("no log records captured")
			}
			for _, record := range records {
				if provider, ok := record["provider_id"]; ok && provider != "configured-provider" {
					t.Errorf("provider_id = %v, want the configured name", provider)
				}
				if profile, ok := record["profile_id"]; ok && profile != "configured-profile" {
					t.Errorf("profile_id = %v, want the configured name", profile)
				}
				if model, ok := record["model"]; ok && model != "fake-metadata-model" {
					t.Errorf("model = %v, want the configured name", model)
				}
			}
			loggingAssertSafeRecords(t, buf, records, server.URL)
		})
	}
}

func TestLoggingWithoutInjectedLoggerWritesNothingToProcessOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loggingWriteJSON(t, w, map[string]any{
			"choices": []any{loggingAssistant("ok")},
			"usage":   map[string]int64{"prompt_tokens": 3, "completion_tokens": 1},
		})
	}))
	defer server.Close()

	client := loggingClient(t, server, &memoryLedger{}, llm.Options{})
	stdout, stderr := loggingCaptureProcessOutput(t, func() {
		if _, err := client.Run(context.Background(), "agent-logging", 16, loggingMessages(), nil, nil); err != nil {
			t.Errorf("run without logger: %v", err)
		}
	})
	if stdout != "" || stderr != "" {
		t.Fatalf("client without injected logger wrote to process output: stdout=%q stderr=%q", stdout, stderr)
	}
}

func loggingCaptureProcessOutput(t *testing.T, fn func()) (string, string) {
	t.Helper()
	oldStdout, oldStderr := os.Stdout, os.Stderr
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating stdout pipe: %v", err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating stderr pipe: %v", err)
	}
	os.Stdout, os.Stderr = stdoutWriter, stderrWriter

	fn()

	if err := stdoutWriter.Close(); err != nil {
		t.Errorf("closing stdout writer: %v", err)
	}
	if err := stderrWriter.Close(); err != nil {
		t.Errorf("closing stderr writer: %v", err)
	}
	os.Stdout, os.Stderr = oldStdout, oldStderr
	stdout, err := io.ReadAll(stdoutReader)
	if err != nil {
		t.Errorf("reading captured stdout: %v", err)
	}
	stderr, err := io.ReadAll(stderrReader)
	if err != nil {
		t.Errorf("reading captured stderr: %v", err)
	}
	_ = stdoutReader.Close()
	_ = stderrReader.Close()
	return string(stdout), string(stderr)
}
