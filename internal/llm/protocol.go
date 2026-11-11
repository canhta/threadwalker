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
	var response completion
	err := json.Unmarshal(raw, &response)
	return response, err
}
