package llm

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// Protocols only translate the wire format. The shared client owns every HTTP
// attempt, usage write, limit check and call to the caller's guarded dispatcher.
type protocolAdapter interface {
	endpoint(string) string
	authorize(*http.Request, string)
	encode(string, []Message, []Tool, int, string) ([]byte, error)
	decode([]byte) (completion, error)
	// attempted lists the tool calls a provider response tried to make, for the audit of a refused response.
	attempted([]byte) []ToolCall
}

func adapterFor(protocol string) (protocolAdapter, error) {
	switch protocol {
	case "", "openai":
		return openAIAdapter{}, nil
	case "anthropic":
		return anthropicAdapter{}, nil
	default:
		return nil, errors.New("configuring LLM client: unsupported protocol")
	}
}

type openAIAdapter struct{}

func (openAIAdapter) endpoint(base string) string {
	return strings.TrimRight(base, "/") + "/chat/completions"
}
func (openAIAdapter) authorize(req *http.Request, key string) {
	req.Header.Set("Authorization", "Bearer "+key)
}
func (openAIAdapter) encode(model string, messages []Message, tools []Tool, limit int, reasoning string) ([]byte, error) {
	return json.Marshal(struct {
		Model               string    `json:"model"`
		Messages            []Message `json:"messages"`
		Tools               []Tool    `json:"tools,omitempty"`
		MaxCompletionTokens int       `json:"max_completion_tokens,omitempty"`
		ReasoningEffort     string    `json:"reasoning_effort,omitempty"`
	}{model, messages, tools, limit, reasoning})
}
func (openAIAdapter) decode(raw []byte) (completion, error) {
	var wire struct {
		Usage *struct {
			PromptTokens     *int64 `json:"prompt_tokens"`
			CompletionTokens *int64 `json:"completion_tokens"`
		} `json:"usage"`
		Choices json.RawMessage `json:"choices"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return completion{}, err
	}
	response := completion{Usage: wire.Usage, raw: raw}
	var choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	}
	if response.choicesError = json.Unmarshal(wire.Choices, &choices); response.choicesError == nil && len(choices) == 1 {
		response.Message, response.FinishReason = choices[0].Message, choices[0].FinishReason
		response.assistant = response.Message.Role == "assistant"
	}
	return response, nil
}

func (openAIAdapter) attempted(raw []byte) []ToolCall {
	// Parse calls individually so a wrong argument/ID type cannot hide every
	// recognizable attempt in the rest of the response from the audit.
	var choices []struct {
		Message struct {
			ToolCalls []json.RawMessage `json:"tool_calls"`
		} `json:"message"`
	}
	var wire struct {
		Choices json.RawMessage `json:"choices"`
	}
	if json.Unmarshal(raw, &wire) != nil || json.Unmarshal(wire.Choices, &choices) != nil {
		return nil
	}
	var calls []ToolCall
	for _, choice := range choices {
		for _, rawCall := range choice.Message.ToolCalls {
			var call ToolCall
			if json.Unmarshal(rawCall, &call) != nil {
				var loose struct {
					ID       json.RawMessage `json:"id"`
					Type     json.RawMessage `json:"type"`
					Function struct {
						Name      json.RawMessage `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					} `json:"function"`
				}
				// Partially decoded strings still identify an attempted tool. Neither
				// opaque provider IDs nor the raw rejected payload enter operational logs.
				_ = json.Unmarshal(rawCall, &loose)
				call = ToolCall{}
				_ = json.Unmarshal(loose.ID, &call.ID)
				_ = json.Unmarshal(loose.Type, &call.Type)
				_ = json.Unmarshal(loose.Function.Name, &call.Function.Name)
				if json.Unmarshal(loose.Function.Arguments, &call.Function.Arguments) != nil {
					call.Function.Arguments = string(loose.Function.Arguments)
				}
			}
			calls = append(calls, call)
		}
	}
	return calls
}
