package llm_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/llm"
)

// protocolRequestRecord is one request observed by a fake provider. Tests keep
// the raw body so they can assert native protocol shapes without depending on
// production-only types.
type protocolRequestRecord struct {
	Path   string
	Header http.Header
	Body   []byte
}

type protocolRecorder struct {
	mu       sync.Mutex
	requests []protocolRequestRecord
}

func (r *protocolRecorder) record(t *testing.T, req *http.Request) {
	t.Helper()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Errorf("reading recorded request body: %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, protocolRequestRecord{
		Path:   req.URL.Path,
		Header: req.Header.Clone(),
		Body:   body,
	})
}

func (r *protocolRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

func (r *protocolRecorder) all() []protocolRequestRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]protocolRequestRecord(nil), r.requests...)
}

func protocolLoadFixtures(t *testing.T, path string) []json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []json.RawMessage
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	return fixtures
}

func protocolOpenAIFinal(text string, in, out int64) []byte {
	return []byte(fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":%d,"completion_tokens":%d}}`, text, in, out))
}

func protocolAnthropicFinal(text string, in, out int64) []byte {
	return []byte(fmt.Sprintf(`{"id":"msg_final","type":"message","role":"assistant","content":[{"type":"text","text":%q}],"stop_reason":"end_turn","usage":{"input_tokens":%d,"output_tokens":%d}}`, text, in, out))
}

type protocolNativeMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type protocolNativeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type protocolNativeRequest struct {
	Model     string                  `json:"model"`
	MaxTokens int                     `json:"max_tokens"`
	System    json.RawMessage         `json:"system"`
	Messages  []protocolNativeMessage `json:"messages"`
	Tools     []json.RawMessage       `json:"tools"`
}

func protocolDecodeNativeRequest(t *testing.T, record protocolRequestRecord) (protocolNativeRequest, map[string]json.RawMessage) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(record.Body, &fields); err != nil {
		t.Fatalf("decoding native request body: %v", err)
	}
	var request protocolNativeRequest
	if err := json.Unmarshal(record.Body, &request); err != nil {
		t.Fatalf("decoding native request schema: %v", err)
	}
	return request, fields
}

// protocolContentBlocks accepts either a bare string, as the Messages API
// permits for simple text, or an array of content blocks. It returns blocks in
// order so tests can inspect tool_use and tool_result entries.
func protocolContentBlocks(t *testing.T, raw json.RawMessage) []protocolNativeBlock {
	t.Helper()
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var blocks []protocolNativeBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		return blocks
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []protocolNativeBlock{{Type: "text", Text: text}}
	}
	t.Fatalf("content is neither text nor content blocks: %s", raw)
	return nil
}

func protocolText(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	blocks := protocolContentBlocks(t, raw)
	var text strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	return text.String()
}

func protocolMessagesContainRole(messages []protocolNativeMessage, role string) bool {
	for _, message := range messages {
		if message.Role == role {
			return true
		}
	}
	return false
}

func protocolToolUses(t *testing.T, messages []protocolNativeMessage) []protocolNativeBlock {
	t.Helper()
	var uses []protocolNativeBlock
	for _, message := range messages {
		for _, block := range protocolContentBlocks(t, message.Content) {
			if block.Type == "tool_use" {
				uses = append(uses, block)
			}
		}
	}
	return uses
}

func protocolToolResults(t *testing.T, messages []protocolNativeMessage) []protocolNativeBlock {
	t.Helper()
	var results []protocolNativeBlock
	for _, message := range messages {
		for _, block := range protocolContentBlocks(t, message.Content) {
			if block.Type == "tool_result" {
				results = append(results, block)
			}
		}
	}
	return results
}

// TestProtocolSelectionAcrossEndpoints verifies that protocol selection is
// explicit configuration, that an empty protocol keeps the OpenAI default, and
// that arbitrary provider/model identifiers and base URLs route to the right
// endpoint and headers.
func TestProtocolSelectionAcrossEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name          string
		protocol      string
		baseSuffix    string
		model         string
		wantPath      string
		wantBearer    bool
		wantNativeKey bool
		response      []byte
		wantContent   string
		wantIn        int64
		wantOut       int64
	}{
		{
			name:        "empty protocol defaults to OpenAI-compatible",
			protocol:    "",
			baseSuffix:  "/v1",
			model:       "vendor/arbitrary-openai-model",
			wantPath:    "/v1/chat/completions",
			wantBearer:  true,
			response:    protocolOpenAIFinal("openai-done", 10, 2),
			wantContent: "openai-done",
			wantIn:      10,
			wantOut:     2,
		},
		{
			name:        "explicit OpenAI-compatible base without v1",
			protocol:    "openai",
			baseSuffix:  "",
			model:       "another/arbitrary-model",
			wantPath:    "/chat/completions",
			wantBearer:  true,
			response:    protocolOpenAIFinal("custom-done", 7, 3),
			wantContent: "custom-done",
			wantIn:      7,
			wantOut:     3,
		},
		{
			name:          "native Anthropic base already ending in v1",
			protocol:      "anthropic",
			baseSuffix:    "/v1",
			model:         "any-provider/any-native-model",
			wantPath:      "/v1/messages",
			wantNativeKey: true,
			response:      protocolAnthropicFinal("native-done", 12, 3),
			wantContent:   "native-done",
			wantIn:        12,
			wantOut:       3,
		},
		{
			name:          "native Anthropic base without v1",
			protocol:      "anthropic",
			baseSuffix:    "",
			model:         "second/arbitrary-native-model",
			wantPath:      "/v1/messages",
			wantNativeKey: true,
			response:      protocolAnthropicFinal("native-root-done", 5, 4),
			wantContent:   "native-root-done",
			wantIn:        5,
			wantOut:       4,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &protocolRecorder{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				recorder.record(t, r)
				w.Write(tc.response)
			}))
			defer server.Close()

			ledger := &memoryLedger{}
			client, err := llm.New(config.LLMConfig{
				Protocol:           tc.protocol,
				BaseURL:            server.URL + tc.baseSuffix,
				Model:              tc.model,
				APIKey:             config.NewSecret("fixture-secret"),
				PricePerMTokInUSD:  1,
				PricePerMTokOutUSD: 2,
			}, config.LimitsConfig{MaxStepsPerWake: 2, MaxUSDPerDay: 1}, ledger, llm.Options{})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: "hello"}}, nil, nil)
			if err != nil {
				t.Fatalf("protocol %q base %q: %v", tc.protocol, tc.baseSuffix, err)
			}
			if result.Content != tc.wantContent {
				t.Fatalf("content=%q want=%q", result.Content, tc.wantContent)
			}

			requests := recorder.all()
			if len(requests) != 1 {
				t.Fatalf("requests=%d want=1", len(requests))
			}
			request := requests[0]
			if request.Path != tc.wantPath {
				t.Errorf("path=%q want=%q", request.Path, tc.wantPath)
			}
			if got := request.Header.Get("Authorization"); tc.wantBearer && got != "Bearer fixture-secret" {
				t.Errorf("Authorization=%q", got)
			} else if !tc.wantBearer && got != "" {
				t.Errorf("OpenAI Authorization leaked into native request: %q", got)
			}
			if got := request.Header.Get("x-api-key"); tc.wantNativeKey && got != "fixture-secret" {
				t.Errorf("x-api-key=%q", got)
			} else if !tc.wantNativeKey && got != "" {
				t.Errorf("native API key header sent to OpenAI-compatible endpoint: %q", got)
			}
			if got := request.Header.Get("anthropic-version"); tc.wantNativeKey && strings.TrimSpace(got) == "" {
				t.Error("missing anthropic-version header")
			} else if !tc.wantNativeKey && got != "" {
				t.Errorf("anthropic-version sent to OpenAI-compatible endpoint: %q", got)
			}

			var body struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal(request.Body, &body); err != nil {
				t.Fatalf("decoding request: %v", err)
			}
			if body.Model != tc.model {
				t.Errorf("model=%q want=%q", body.Model, tc.model)
			}
			if ledger.usage.TokensIn != tc.wantIn || ledger.usage.TokensOut != tc.wantOut {
				t.Fatalf("ledger usage=%+v want in=%d out=%d", ledger.usage, tc.wantIn, tc.wantOut)
			}
		})
	}
}

// TestProtocolOpenAISystemMessageStaysInMessages guards the protocol boundary:
// OpenAI-compatible requests keep the system turn in the message list, while
// Anthropic extracts it (covered in anthropic_test.go).
func TestProtocolOpenAISystemMessageStaysInMessages(t *testing.T) {
	recorder := &protocolRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(t, r)
		w.Write(protocolOpenAIFinal("ok", 1, 1))
	}))
	defer server.Close()

	client, err := llm.New(config.LLMConfig{
		Protocol: "openai", BaseURL: server.URL + "/v1", Model: "arbitrary-openai-model",
		APIKey: config.NewSecret("fixture-secret"), PricePerMTokInUSD: 1, PricePerMTokOutUSD: 1,
	}, config.LimitsConfig{MaxStepsPerWake: 2, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Run(context.Background(), "agent", 1, []llm.Message{
		{Role: "system", Content: "immutable system prompt"},
		{Role: "user", Content: "hello"},
	}, nil, nil); err != nil {
		t.Fatalf("openai protocol: %v", err)
	}

	requests := recorder.all()
	if len(requests) != 1 {
		t.Fatalf("requests=%d want=1", len(requests))
	}
	var body struct {
		Messages []llm.Message   `json:"messages"`
		System   json.RawMessage `json:"system"`
	}
	if err := json.Unmarshal(requests[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.System) != 0 {
		t.Errorf("OpenAI request unexpectedly has native system field: %s", body.System)
	}
	if len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[0].Content != "immutable system prompt" {
		t.Errorf("OpenAI system message changed: %+v", body.Messages)
	}
}
