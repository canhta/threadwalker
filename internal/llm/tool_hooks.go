package llm

import (
	"context"
	"encoding/json"
	"errors"
)

// TerminalToolError marks failures for which another model turn is unsafe.
// Ordinary dispatcher errors still become safe model-visible tool errors.
// Wrapping preserves errors.Is/As so the runtime can record its stop reason.
type TerminalToolError interface {
	error
	TerminalToolError() bool
}

func isTerminalToolError(err error) bool {
	var terminal TerminalToolError
	return errors.As(err, &terminal) && terminal.TerminalToolError()
}
func terminalToolFailure(err error) error {
	return failure("running tool loop", "terminal_tool_error", err)
}
func (c *Client) rejectCalls(ctx context.Context, calls []ToolCall, reason string) error {
	if c.rejectedToolCall == nil {
		return nil
	}
	for _, call := range calls {
		if err := c.rejectedToolCall(ctx, call, reason); isTerminalToolError(err) {
			return err
		}
	}
	return nil
}

// Native adapters can reject a response after normalizing its tool attempts.
// Audit those attempts even though no effect may execute from that response.
func (c *Client) rejectResponse(ctx context.Context, raw json.RawMessage, reason string) error {
	if c.rejectedToolCall == nil {
		return nil
	}
	// Parse calls individually so a wrong argument/ID type cannot hide every
	// recognizable attempt in the rest of the response from the audit hook.
	var choices []struct {
		Message struct {
			ToolCalls []json.RawMessage `json:"tool_calls"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &choices) != nil {
		return nil
	}
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
			if err := c.rejectedToolCall(ctx, call, reason); isTerminalToolError(err) {
				return err
			}
		}
	}
	return nil
}
