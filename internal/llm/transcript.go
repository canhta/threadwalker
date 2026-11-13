package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/canhta/threadz/internal/stdx"
	"github.com/canhta/threadz/internal/store"
)

// MaxTranscriptToolBytes caps one tool result in a transcript. Tool results
// can carry other users' text, so the cap also bounds how much is kept.
const MaxTranscriptToolBytes = 64 << 10

func (c *Client) record(ctx context.Context, kind string, body any) {
	if c.transcript == nil {
		return
	}
	data, err := json.Marshal(body)
	if err != nil {
		return
	}
	c.transcript(ctx, kind, string(data))
}

// recordContext stores the hash of every rendered message and the data message
// itself; the system prompt is derived from the template, so only its hash is kept.
func (c *Client) recordContext(ctx context.Context, messages []Message) {
	h := sha256.New()
	var data []string
	for _, m := range messages {
		h.Write([]byte(m.Role + "\x00" + m.Content + "\x00"))
		if m.Role != "system" {
			data = append(data, m.Content)
		}
	}
	c.record(ctx, store.TranscriptContext, map[string]any{"hash": hex.EncodeToString(h.Sum(nil)), "data": data})
}

func (c *Client) recordAssistant(ctx context.Context, request int, m Message) {
	calls := append([]ToolCall(nil), m.ToolCalls...)
	for i := range calls {
		calls[i].Function.Arguments, _ = stdx.TruncateUTF8(calls[i].Function.Arguments, MaxTranscriptToolBytes)
	}
	text, _ := stdx.TruncateUTF8(m.Content, MaxTranscriptToolBytes)
	c.record(ctx, store.TranscriptAssistant, map[string]any{"request": request, "text": text, "tool_calls": calls})
}

func (c *Client) recordToolResult(ctx context.Context, request int, tool string, m Message, reason string) {
	output, truncated := stdx.TruncateUTF8(m.Content, MaxTranscriptToolBytes)
	// The dispatcher wraps outside data as {"untrusted":true,...}; read the mark
	// from the whole result so a cut cannot hide it.
	var wrapper struct {
		Untrusted bool `json:"untrusted"`
	}
	_ = json.Unmarshal([]byte(m.Content), &wrapper)
	c.record(ctx, store.TranscriptToolResult, map[string]any{"untrusted": wrapper.Untrusted, "request": request, "tool": tool, "tool_call_id": m.ToolCallID, "reason": reason, "error": m.ToolError, "truncated": truncated, "output": output})
}

func (c *Client) recordUsage(ctx context.Context, request, attempt int, u Usage) {
	c.record(ctx, store.TranscriptUsage, map[string]any{"request": request, "attempt": attempt, "tokens_in": u.TokensIn, "tokens_out": u.TokensOut, "cost_usd": u.CostUSD})
}
