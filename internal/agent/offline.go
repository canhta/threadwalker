package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/llm"
	"github.com/canhta/threadwalker/internal/tools"
)

// OfflineTemplate is synthetic, credential-free and independent of operator
// configuration. Its placeholder key is never resolved from the environment.
func OfflineTemplate() config.Template {
	return config.Template{ID: "synthetic-offline", Goal: "Exercise a synthetic local dry-run wake.", DryRun: true,
		Persona: config.PersonaConfig{Name: "Synthetic fixture", Language: "en", Identity: "Synthetic offline agent", Voice: "Brief", BioMustInclude: "AI experiment"},
		LLM:     config.LLMConfig{Protocol: "openai", BaseURL: "https://fixture.invalid", Model: "synthetic-script", APIKey: config.NewSecret("offline-dummy-key"), PricePerMTokInUSD: 1, PricePerMTokOutUSD: 2},
		Limits:  config.LimitsConfig{PostsPerDay: 3, RepliesPerDay: 15, RepliesPerUser7d: 2, MinSecondsBetweenActions: 1, MaxStepsPerWake: 30, MaxUSDPerDay: 1, MinMinutesBetweenWakes: 20}}
}

// OfflineFixture runs the real LLM wire adapter using an in-process transport;
// it cannot make a socket connection or fall back to an external service.
type OfflineFixture struct {
	Now     func() time.Time
	Calls   []llm.ToolCall
	Observe func([]llm.Message)
}

func NewOfflineFixture(now func() time.Time) *OfflineFixture { return &OfflineFixture{Now: now} }
func SyntheticCall(name, args string) llm.ToolCall {
	return llm.ToolCall{Type: "function", Function: llm.CallFunction{Name: name, Arguments: args}}
}
func (f *OfflineFixture) Client() *http.Client { return &http.Client{Transport: f} }
func (f *OfflineFixture) Trends(ctx context.Context) ([]tools.Trend, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []tools.Trend{{Source: "synthetic", Title: "Synthetic offline trend", URL: "https://synthetic.invalid/trend"}}, nil
}
func (f *OfflineFixture) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	if req.URL.Host != "fixture.invalid" || req.Method != http.MethodPost {
		return nil, errors.New("offline transport: unexpected request")
	}
	var body struct{ Messages []llm.Message }
	var raw struct {
		System   string `json:"system"`
		Messages []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCalls  []llm.ToolCall  `json:"tool_calls"`
			ToolCallID string          `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(io.LimitReader(req.Body, 2<<20)).Decode(&raw); err != nil {
		return nil, errors.New("offline transport: invalid request")
	}
	native := req.URL.Path == "/v1/messages"
	if raw.System != "" {
		body.Messages = append(body.Messages, llm.Message{Role: "system", Content: raw.System})
	}
	for _, m := range raw.Messages {
		if !native {
			var content string
			if json.Unmarshal(m.Content, &content) != nil {
				return nil, errors.New("offline transport: invalid content")
			}
			body.Messages = append(body.Messages, llm.Message{Role: m.Role, Content: content, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID})
			continue
		}
		var blocks []struct {
			Type, Text, Content string
			ToolUseID           string `json:"tool_use_id"`
		}
		if json.Unmarshal(m.Content, &blocks) != nil {
			return nil, errors.New("offline transport: invalid native content")
		}
		for _, b := range blocks {
			switch b.Type {
			case "text":
				body.Messages = append(body.Messages, llm.Message{Role: m.Role, Content: b.Text})
			case "tool_result":
				body.Messages = append(body.Messages, llm.Message{Role: "tool", Content: b.Content, ToolCallID: b.ToolUseID})
			}
		}
	}
	if len(body.Messages) == 0 {
		return nil, errors.New("offline transport: missing messages")
	}
	if f.Observe != nil {
		f.Observe(body.Messages)
	}
	message := llm.Message{Role: "assistant", Content: "Synthetic wake completed."}
	reason := "stop"
	if body.Messages[len(body.Messages)-1].Role != "tool" {
		calls := f.Calls
		if calls == nil {
			now := f.Now()
			args, _ := json.Marshal(map[string]string{"at": now.Add(time.Hour).UTC().Format(time.RFC3339)})
			calls = []llm.ToolCall{SyntheticCall("read_feed", `{}`), SyntheticCall("fetch_trends", `{}`), SyntheticCall("note_write", `{"text":"Synthetic note for the next wake."}`), SyntheticCall("post", `{"text":"Synthetic local dry-run action."}`), SyntheticCall("schedule_next_wake", string(args))}
		}
		if len(calls) > 0 {
			message.Content = ""
			message.ToolCalls = append([]llm.ToolCall(nil), calls...)
			for i := range message.ToolCalls {
				message.ToolCalls[i].ID = "synthetic-" + time.Unix(int64(i), 0).UTC().Format("150405")
			}
			reason = "tool_calls"
		}
	}
	data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": reason}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5}})
	if native {
		blocks := []any{map[string]any{"type": "text", "text": message.Content}}
		stop := "end_turn"
		if len(message.ToolCalls) > 0 {
			blocks = []any{}
			stop = "tool_use"
			for _, call := range message.ToolCalls {
				var input any
				json.Unmarshal([]byte(call.Function.Arguments), &input)
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": input})
			}
		}
		data, _ = json.Marshal(map[string]any{"role": "assistant", "content": blocks, "stop_reason": stop, "usage": map[string]int{"input_tokens": 10, "output_tokens": 5}})
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data))), Request: req}, nil
}
