package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/llm"
)

func TestNativeLoggingRetryUsageAndMalformedResponse(t *testing.T) {
	logger, buf := loggingTestLogger(t, "debug")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(503)
		}
		if requests == 1 {
			loggingWriteJSON(t, w, map[string]any{"error": map[string]string{"message": loggingFakeProviderBody}, "usage": map[string]int64{"input_tokens": 10, "output_tokens": 2, "cache_read_input_tokens": 3, "cache_creation_input_tokens": 4}})
			return
		}
		// Valid usage must survive a malformed native message and remain correlated.
		loggingWriteJSON(t, w, map[string]any{"role": "assistant", "content": loggingFakeProviderBody, "usage": map[string]int64{"input_tokens": 20, "output_tokens": 4}})
	}))
	defer server.Close()
	ledger := &memoryLedger{}
	client, err := llm.New(config.LLMConfig{Protocol: "anthropic", BaseURL: server.URL, Model: "configured-native-model", APIKey: config.NewSecret(loggingFakeKey), PricePerMTokInUSD: 1, PricePerMTokOutUSD: 2}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, ledger, llm.Options{Logger: logger, ProviderID: "configured-provider", ProfileID: "configured-profile", Sleep: func(context.Context, time.Duration) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Run(context.Background(), "native-agent", 18, loggingMessages(), nil, nil)
	if err == nil || requests != 2 || ledger.usage.TokensIn != 37 || ledger.usage.TokensOut != 6 {
		t.Fatal("native failure lost accounting or retry semantics")
	}
	records := loggingRecords(t, buf)
	usages := loggingEvents(records, "llm.usage.recorded")
	if len(usages) != 2 || loggingNumberField(t, usages[0], "tokens_in") != 17 || loggingNumberField(t, usages[1], "tokens_in") != 20 {
		t.Fatal("native usage events do not match normalized input")
	}
	if got := loggingNumberField(t, usages[0], "cost_usd"); got != 0.000021 {
		t.Fatalf("native first cost=%g", got)
	}
	if loggingStringField(t, loggingOneEvent(t, records, "llm.run.stopped"), "reason") != "invalid_response" {
		t.Fatal("native malformed-message reason missing")
	}
	for _, record := range records {
		if record["agent_id"] != "native-agent" || record["wake_id"] != float64(18) || record["protocol"] != "anthropic" {
			t.Fatal("native event lost correlation")
		}
	}
	loggingAssertSafeRecords(t, buf, records, server.URL)
}

// Every terminal path carries planted private data in messages, schema metadata,
// response data and injected errors. Filtering may remove context, never expose
// payloads or change sanitization of the returned diagnostic.
func TestLoggingFailurePathRedactionAtEveryLevel(t *testing.T) {
	poison := strings.Join([]string{loggingFakeKey, loggingFakePrompt, loggingFakeNote, loggingFakeUsername, loggingFakeProviderBody}, " ")
	valid := func() string {
		body, _ := json.Marshal(map[string]any{"choices": []any{loggingAssistant(poison)}, "usage": map[string]int64{"prompt_tokens": 10, "completion_tokens": 2}, "private": poison})
		return string(body)
	}
	for _, level := range []string{"debug", "info", "warn", "error"} {
		for _, scenario := range []string{"invalid_request", "transport", "response_read", "response_too_large", "malformed_response", "malformed_choices", "incomplete_usage", "negative_usage", "missing_usage", "invalid_tool_call_id", "incomplete_response", "http_error", "retry_exhausted", "budget_limit", "step_limit", "cancelled", "accounting_read_failed", "accounting_write_failed", "dispatch_failed"} {
			t.Run(level+"/"+scenario, func(t *testing.T) {
				logger, buf := loggingTestLogger(t, level)
				ledger := &memoryLedger{}
				if scenario == "budget_limit" {
					ledger.usage.CostUSD = 1
				}
				if scenario == "accounting_read_failed" {
					ledger.readErr = errors.New(poison)
				}
				if scenario == "accounting_write_failed" {
					ledger.writeErr = errors.New(poison)
				}
				body, status := valid(), 200
				switch scenario {
				case "malformed_response":
					body = poison
				case "malformed_choices":
					body = `{"choices":"` + loggingFakeProviderBody + `","usage":{"prompt_tokens":10,"completion_tokens":2}}`
				case "incomplete_usage":
					body = `{"private":"` + loggingFakeProviderBody + `","usage":{"prompt_tokens":10}}`
				case "negative_usage":
					body = `{"private":"` + loggingFakeProviderBody + `","usage":{"prompt_tokens":-1,"completion_tokens":2}}`
				case "missing_usage":
					body = `{"private":"` + loggingFakeProviderBody + `"}`
				case "invalid_tool_call_id":
					body = `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"","type":"function","function":{"name":"note_write","arguments":"{}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`
				case "incomplete_response":
					body = strings.Replace(valid(), `"finish_reason":"stop"`, `"finish_reason":"length"`, 1)
				case "http_error":
					status = 400
				case "retry_exhausted":
					status = 503
				case "step_limit", "dispatch_failed":
					b, _ := json.Marshal(loggingToolResponse(t, llm.ToolCall{ID: poison, Type: "function", Function: llm.CallFunction{Name: "note_write", Arguments: `{"text":"` + loggingFakeNote + `"}`}}))
					body = string(b)
				}
				transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if scenario == "transport" {
						return nil, errors.New(poison)
					}
					var reader io.ReadCloser = io.NopCloser(strings.NewReader(body))
					if scenario == "response_read" {
						reader = secretErrorBody{poison}
					}
					if scenario == "response_too_large" {
						reader = io.NopCloser(strings.NewReader(strings.Repeat(loggingFakeProviderBody, 300000)))
					}
					return &http.Response{StatusCode: status, Body: reader, Header: make(http.Header)}, nil
				})
				client, err := llm.New(config.LLMConfig{BaseURL: "https://example.test", Model: "configured-model", APIKey: config.NewSecret(loggingFakeKey), PricePerMTokInUSD: 1, PricePerMTokOutUSD: 2}, config.LimitsConfig{MaxStepsPerWake: 1, MaxUSDPerDay: 1}, ledger, llm.Options{Logger: logger, HTTPClient: &http.Client{Transport: transport}, Sleep: func(context.Context, time.Duration) error { return nil }})
				if err != nil {
					t.Fatal(err)
				}
				ctx := context.Background()
				if scenario == "cancelled" {
					cancelled, cancel := context.WithCancel(ctx)
					cancel()
					ctx = cancelled
					ledger.readErr = context.Canceled
				}
				tools := []llm.Tool{loggingTool("note_write", "")}
				tools[0].Function.Description = poison
				if scenario == "invalid_request" {
					tools[0].Function.Parameters = json.RawMessage(poison)
				}
				_, err = client.Run(ctx, "agent-failure", 19, loggingMessages(), tools, llm.DispatchFunc(func(context.Context, llm.ToolCall) (string, error) { return poison, errors.New(poison) }))
				if err == nil {
					t.Fatal("failure scenario unexpectedly succeeded")
				}
				for _, value := range []string{loggingFakeKey, loggingFakePrompt, loggingFakeNote, loggingFakeUsername, loggingFakeProviderBody} {
					if strings.Contains(err.Error(), value) {
						t.Fatal("returned error exposed private data")
					}
				}
				records := loggingRecords(t, buf)
				if len(loggingEvents(records, "llm.run.stopped")) != 1 {
					t.Fatal("terminal failure event missing at selected level")
				}
				loggingAssertSafeRecords(t, buf, records)
			})
		}
	}
}

type secretErrorBody struct{ message string }

func (b secretErrorBody) Read([]byte) (int, error) { return 0, errors.New(b.message) }
func (secretErrorBody) Close() error               { return nil }
