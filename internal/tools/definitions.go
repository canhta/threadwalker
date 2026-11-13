// Package tools exposes the agent's fixed, guarded local dry-run action surface.
package tools

import (
	"context"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/llm"
)

// executor is the effect of one tool. What a tool is, which phases may use it
// and how its arguments look live in guard.Specs; an executor can only be
// attached to a tool listed there.
type executor func(context.Context, arguments) (any, error)

type arguments struct {
	guard.Args
	ReservedActionID int64
}

func definition(s guard.ToolSpec) llm.Tool {
	return llm.Tool{Type: "function", Function: llm.Function{Name: s.Name, Description: s.Description, Parameters: s.Schema()}}
}
