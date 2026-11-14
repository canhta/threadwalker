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

type rejectionKey struct{}
type finalKey struct{}

// FinalRequest reports whether a call comes from the last model request the
// wake may make: the model never sees its result.
func FinalRequest(ctx context.Context) bool {
	final, _ := ctx.Value(finalKey{}).(bool)
	return final
}

// RejectedReason is the reason the client attaches to a call taken from a
// malformed response; the dispatcher must refuse and audit it, never run it.
func RejectedReason(ctx context.Context) string {
	reason, _ := ctx.Value(rejectionKey{}).(string)
	return reason
}

func (c *Client) rejectCalls(ctx context.Context, dispatcher Dispatcher, calls []ToolCall, reason string) error {
	if dispatcher == nil {
		return nil
	}
	ctx = context.WithValue(ctx, rejectionKey{}, reason)
	for _, call := range calls {
		if _, err := dispatcher.Dispatch(ctx, call); isTerminalToolError(err) {
			return err
		}
	}
	return nil
}

// Native adapters can reject a response after normalizing its tool attempts.
// Forward those attempts for audit even though no effect may execute from that response.
func (c *Client) rejectResponse(ctx context.Context, dispatcher Dispatcher, raw json.RawMessage, reason string) error {
	for _, call := range c.adapter.attempted(raw) {
		if err := c.rejectCalls(ctx, dispatcher, []ToolCall{call}, reason); err != nil {
			return err
		}
	}
	return nil
}
