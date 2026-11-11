package llm

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
)

type anthropicAdapter struct{}

func (anthropicAdapter) endpoint(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + "/messages"
	}
	return base + "/v1/messages"
}
func (anthropicAdapter) authorize(req *http.Request, key string) {
	req.Header.Set("x-api-key", key)
	// Pin the stable Messages contract; no model or vendor ID is selected here.
	req.Header.Set("anthropic-version", "2023-06-01")
}

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}
type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}
type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func (anthropicAdapter) encode(model string, messages []Message, tools []Tool, limit int, _ string) ([]byte, error) {
	if limit == 0 {
		limit = 1024
	} // Messages requires a bounded max_tokens.
	request := struct {
		Model     string             `json:"model"`
		MaxTokens int                `json:"max_tokens"`
		System    string             `json:"system,omitempty"`
		Messages  []anthropicMessage `json:"messages"`
		Tools     []anthropicTool    `json:"tools,omitempty"`
	}{Model: model, MaxTokens: limit}
	for _, m := range messages {
		var blocks []anthropicBlock
		role := m.Role
		switch role {
		case "system", "developer":
			if request.System != "" {
				request.System += "\n\n"
			}
			request.System += m.Content
			continue
		case "user", "assistant":
			if m.Content != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: m.Content})
			}
			for _, call := range m.ToolCalls {
				// Non-object input is valid JSON on the wire and is returned as a
				// tool error by the shared loop; preserve it for conversation repair.
				if !json.Valid([]byte(call.Function.Arguments)) {
					return nil, errors.New("encoding native message: invalid tool arguments")
				}
				blocks = append(blocks, anthropicBlock{Type: "tool_use", ID: call.ID, Name: call.Function.Name, Input: json.RawMessage(call.Function.Arguments)})
			}
		case "tool":
			role = "user"
			blocks = append(blocks, anthropicBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Content, IsError: m.ToolError})
		default:
			return nil, errors.New("encoding native message: unsupported role")
		}
		if len(request.Messages) > 0 && request.Messages[len(request.Messages)-1].Role == role {
			last := &request.Messages[len(request.Messages)-1]
			last.Content = append(last.Content, blocks...)
		} else {
			request.Messages = append(request.Messages, anthropicMessage{Role: role, Content: blocks})
		}
	}
	for _, tool := range tools {
		if tool.Type != "function" {
			return nil, errors.New("encoding native tools: unsupported type")
		}
		request.Tools = append(request.Tools, anthropicTool{Name: tool.Function.Name, Description: tool.Function.Description, InputSchema: tool.Function.Parameters})
	}
	return json.Marshal(request)
}

func (anthropicAdapter) decode(raw []byte) (completion, error) {
	var wire struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		StopReason string          `json:"stop_reason"`
		Usage      *struct {
			Input         *int64 `json:"input_tokens"`
			Output        *int64 `json:"output_tokens"`
			CacheRead     int64  `json:"cache_read_input_tokens"`
			CacheCreation int64  `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return completion{}, err
	}
	response := completion{}
	if wire.Usage != nil {
		// The ledger's input count includes all native input categories. Its
		// configured flat input price does not model cache discounts/surcharges.
		input := wire.Usage.Input
		if input != nil {
			n := *input
			for _, extra := range []int64{wire.Usage.CacheRead, wire.Usage.CacheCreation} {
				if n < 0 || extra < 0 || n > math.MaxInt64-extra {
					n = -1
					break
				}
				n += extra
			}
			input = &n
		}
		response.Usage = &struct {
			PromptTokens     *int64 `json:"prompt_tokens"`
			CompletionTokens *int64 `json:"completion_tokens"`
		}{input, wire.Usage.Output}
	}
	message := Message{Role: wire.Role}
	var blocks []anthropicBlock
	if err := json.Unmarshal(wire.Content, &blocks); err != nil || blocks == nil {
		response.MessageError = errors.New("reading native response: invalid content blocks")
		return response, nil
	}
	for _, block := range blocks {
		switch block.Type {
		case "text":
			message.Content += block.Text
		case "tool_use":
			input := string(block.Input)
			if input == "" {
				input = "null"
			}
			message.ToolCalls = append(message.ToolCalls, ToolCall{ID: block.ID, Type: "function", Function: CallFunction{Name: block.Name, Arguments: input}})
		default:
			response.MessageError = errors.New("reading native response: unsupported content block")
		}
	}
	finish := wire.StopReason
	if finish == "tool_use" {
		finish = "tool_calls"
	}
	if finish == "end_turn" || finish == "stop_sequence" {
		finish = "stop"
	}
	if len(message.ToolCalls) > 0 && wire.StopReason != "tool_use" {
		response.MessageError = errors.New("reading native response: incomplete tool use")
	}
	response.Choices, _ = json.Marshal([]struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	}{{message, finish}})
	return response, nil
}
