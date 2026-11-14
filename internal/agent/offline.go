package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/quality"
	"github.com/canhta/threadz/internal/trends"
)

// OfflineTemplate is synthetic, credential-free and independent of operator
// configuration. Its placeholder key is never resolved from the environment.
func OfflineTemplate() config.Template {
	return config.Template{ID: "synthetic-offline", Goal: "Exercise a synthetic local dry-run wake.", DryRun: true,
		Persona: config.PersonaConfig{Name: "Synthetic fixture", Language: "en", Identity: "Synthetic offline agent", Voice: "Brief", BioMustInclude: "AI experiment"},
		LLM:     config.LLMConfig{Protocol: "openai", BaseURL: "https://fixture.invalid", Model: "synthetic-script", APIKey: config.NewSecret("offline-dummy-key"), PricePerMTokInUSD: 1, PricePerMTokOutUSD: 2}, EmbeddingAPIKey: config.NewSecret("offline-dummy-key"),
		Threads: config.ThreadsConfig{Username: "synthetic_offline"},
		Limits:  config.LimitsConfig{PostsPerDay: 3, RepliesPerDay: 15, RepliesPerUser7d: 2, MinSecondsBetweenActions: 1, MaxStepsPerWake: 30, MaxUSDPerDay: 1, MinMinutesBetweenWakes: 20}, Targets: config.DefaultTargets, MinEffect: 0.2}
}

// OfflineFixture runs the real LLM wire adapter using an in-process transport;
// it cannot make a socket connection or fall back to an external service.
type OfflineFixture struct {
	Now     func() time.Time
	Calls   []llm.ToolCall
	Observe func([]llm.Message)
	// Reply is the answer to a request that offers no tools, such as the post-mortem judge.
	Reply string
}

func NewOfflineFixture(now func() time.Time) *OfflineFixture { return &OfflineFixture{Now: now} }
func SyntheticCall(name, args string) llm.ToolCall {
	return llm.ToolCall{Type: "function", Function: llm.CallFunction{Name: name, Arguments: args}}
}
func (f *OfflineFixture) Client() *http.Client { return &http.Client{Transport: f} }
func (f *OfflineFixture) Trends(ctx context.Context) ([]trends.Trend, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []trends.Trend{{Source: "synthetic", Title: "Synthetic offline trend", URL: "https://synthetic.invalid/trend"}}, nil
}

// Study returns synthetic study items for an offline study wake.
func (f *OfflineFixture) Study(ctx context.Context) (trends.StudyFeed, error) {
	if err := ctx.Err(); err != nil {
		return trends.StudyFeed{}, err
	}
	return trends.StudyFeed{Items: []trends.Trend{
		{Source: "synthetic", Title: "Synthetic offline study story", URL: "https://synthetic.invalid/study"},
		{Source: "synthetic", Title: "Synthetic offline follow-up story", URL: "https://synthetic.invalid/study-follow-up"},
	}}, nil
}
func (f *OfflineFixture) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	if req.URL.Host != "fixture.invalid" || req.Method != http.MethodPost {
		return nil, errors.New("offline transport: unexpected request")
	}
	if req.URL.Path == "/embeddings" {
		return llm.OfflineEmbeddingsResponse(req)
	}
	if req.URL.Path == "/responses" {
		data, _ := json.Marshal(map[string]any{
			"output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "Synthetic offline web answer.", "annotations": []any{map[string]any{"type": "url_citation", "url": "https://synthetic.invalid/web-source", "title": "Synthetic source"}}}}}},
			"usage":  map[string]int{"input_tokens": 25, "output_tokens": 10},
		})
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data))), Request: req}, nil
	}
	var body struct{ Messages []llm.Message }
	var raw struct {
		System string `json:"system"`
		Tools  []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
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
	answered := len(raw.Tools) == 0
	if answered && f.Reply != "" {
		message.Content = f.Reply
	}
	for _, m := range body.Messages {
		answered = answered || m.Role == "tool"
	}
	if strings.HasPrefix(body.Messages[0].Content, quality.CriticPrefix) {
		message.Content = `{"pass":true,"problems":[],"scores":{"hook":4,"natural_vietnamese":4,"specificity":4,"forced_self_reference":5,"target_fit":4,"novelty":4,"clarity":4}}`
	}
	if !answered {
		calls := f.Calls
		if calls == nil {
			now := f.Now()
			calls = []llm.ToolCall{SyntheticCall("read_feed", `{}`), SyntheticCall("fetch_trends", `{}`), SyntheticCall("note_write", `{"topic":"synthetic","description":"Synthetic note for the next wake.","text":"Synthetic note body."}`), SyntheticCall("post", fmt.Sprintf(`{"text":"local%d"}`, now.UnixNano()))}
			for _, tool := range raw.Tools {
				if tool.Function.Name == "web_search" {
					calls = append([]llm.ToolCall{SyntheticCall("web_search", `{"query":"Synthetic offline study story."}`)}, calls...)
					break
				}
			}
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
