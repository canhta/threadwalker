package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
)

func TestNativeProtocolSelectionAndAccounting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "fixture-secret" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("wrong native endpoint or headers")
		}
		w.Write([]byte(`{"role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":3}}`))
	}))
	defer server.Close()
	ledger := &memoryLedger{}
	client, err := llm.New(config.LLMConfig{Protocol: "anthropic", BaseURL: server.URL + "/v1", Model: "arbitrary/native-model", APIKey: config.NewSecret("fixture-secret"), PricePerMTokInUSD: 1, PricePerMTokOutUSD: 2}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, ledger, llm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "hello"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "done" || ledger.usage.TokensIn != 12 || ledger.usage.TokensOut != 3 {
		t.Fatalf("result=%+v usage=%+v", result, ledger.usage)
	}
}

func TestTransportAndToolResultsRedactCredentials(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				raw, _ := io.ReadAll(r.Body)
				if strings.Contains(string(raw), "fixture-secret") {
					t.Error("credential entered model request")
				}
				if requests == 1 {
					if protocol == "anthropic" {
						w.Write([]byte(`{"role":"assistant","content":[{"type":"tool_use","id":"one","name":"echo","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":1}}`))
					} else {
						w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"one","type":"function","function":{"name":"echo","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))
					}
				} else if protocol == "anthropic" {
					w.Write([]byte(`{"role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`))
				} else {
					w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))
				}
			}))
			defer server.Close()
			client, err := llm.New(config.LLMConfig{Protocol: protocol, BaseURL: server.URL, Model: "custom", APIKey: config.NewSecret("fixture-secret")}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "fixture-secret"}}, []llm.Tool{{Type: "function", Function: llm.Function{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}}}, llm.DispatchFunc(func(context.Context, llm.ToolCall) (string, error) { return "fixture-secret", nil }))
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	client, err := llm.New(config.LLMConfig{BaseURL: "http://unused.invalid", Model: "custom", APIKey: config.NewSecret("fixture-secret")}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{HTTPClient: &http.Client{Transport: secretTransport{}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Run(context.Background(), "agent", 1, nil, nil, nil)
	if err == nil || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("unsafe transport error: %v", err)
	}
}

type secretTransport struct{}

func (secretTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("fixture-secret")
}

func TestNativeCacheUsageAndMalformedContentAccounting(t *testing.T) {
	for _, tc := range []struct {
		name, content, usage string
		wantIn               int64
		wantErr              bool
	}{
		{"cache tokens", `[{"type":"text","text":"done"}]`, `{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}`, 17, false},
		{"unknown block", `[{"type":"server_tool_use"}]`, `{"input_tokens":10,"output_tokens":2}`, 10, true},
		{"malformed content", `{}`, `{"input_tokens":10,"output_tokens":2}`, 10, true},
		{"missing content", `null`, `{"input_tokens":10,"output_tokens":2}`, 10, true},
		{"negative cache", `[{"type":"text","text":"done"}]`, `{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":-1}`, 0, true},
		{"cache overflow", `[{"type":"text","text":"done"}]`, `{"input_tokens":9223372036854775807,"output_tokens":2,"cache_read_input_tokens":1}`, 0, true},
		{"incomplete usage", `[{"type":"text","text":"done"}]`, `{"input_tokens":10}`, 0, true},
		{"missing usage", `[{"type":"text","text":"done"}]`, `null`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"role":"assistant","content":%s,"stop_reason":"end_turn","usage":%s}`, tc.content, tc.usage)
			}))
			defer server.Close()
			ledger := &memoryLedger{}
			client, err := llm.New(config.LLMConfig{Protocol: "anthropic", BaseURL: server.URL, Model: "arbitrary", APIKey: config.NewSecret("fixture-secret")}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, ledger, llm.Options{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "hello"}}, nil, nil)
			if (err != nil) != tc.wantErr || ledger.usage.TokensIn != tc.wantIn {
				t.Fatalf("err=%v usage=%+v", err, ledger.usage)
			}
		})
	}
}

func TestTruncatedToolCallsNeverDispatch(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if protocol == "openai" {
					w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"one","type":"function","function":{"name":"echo","arguments":"{}"}}]},"finish_reason":"length"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))
				} else {
					w.Write([]byte(`{"role":"assistant","content":[{"type":"tool_use","id":"one","name":"echo","input":{}}],"stop_reason":"max_tokens","usage":{"input_tokens":2,"output_tokens":1}}`))
				}
			}))
			defer server.Close()
			ledger := &memoryLedger{}
			client, err := llm.New(config.LLMConfig{Protocol: protocol, BaseURL: server.URL, Model: "custom", APIKey: config.NewSecret("fixture-secret")}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, ledger, llm.Options{})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			_, err = client.Run(context.Background(), "agent", 1, nil, []llm.Tool{{Type: "function", Function: llm.Function{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}}}, refusing(&calls, "ok", "echo"))
			if err == nil || calls != 0 || ledger.usage.TokensIn != 2 {
				t.Fatalf("err=%v calls=%d usage=%+v", err, calls, ledger.usage)
			}
		})
	}
}
