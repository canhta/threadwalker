// Package tools exposes the agent's fixed, guarded local dry-run action surface.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/canhta/threadwalker/internal/llm"
)

// Tool is implemented only by the fixed builtins. Execution stays private so
// callers cannot retrieve a handler and bypass the dispatcher/guard boundary.
type Tool interface {
	Definition() llm.Tool
	execute(context.Context, arguments) (any, error)
	external() bool
}
type field struct {
	kind     string
	required bool
}
type builtin struct {
	name, description string
	fields            map[string]field
	outside           bool
	run               func(context.Context, arguments) (any, error)
}

func (b builtin) external() bool                                           { return b.outside }
func (b builtin) execute(ctx context.Context, args arguments) (any, error) { return b.run(ctx, args) }
func (b builtin) Definition() llm.Tool {
	properties := map[string]any{}
	required := []string{}
	// Stable ordering makes request bodies and configuration fingerprints stable.
	for _, key := range []string{"text", "query", "limit", "at", "url", "target_url", "target_username", "username", "hypothesis_ids"} {
		f, ok := b.fields[key]
		if !ok {
			continue
		}
		property := map[string]any{"type": f.kind}
		if f.kind == "array" {
			property["items"] = map[string]string{"type": "integer"}
		}
		if key == "limit" {
			property["minimum"] = 1
			property["maximum"] = 100
		}
		if key == "at" {
			property["format"] = "date-time"
		}
		if f.kind == "string" {
			property["minLength"] = 1
		}
		properties[key] = property
		if f.required {
			required = append(required, key)
		}
	}
	schema, _ := json.Marshal(map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false})
	return llm.Tool{Type: "function", Function: llm.Function{Name: b.name, Description: b.description, Parameters: schema}}
}

type arguments struct {
	Text, Query, At, URL, TargetURL, TargetUsername, Username string
	ReservedActionID                                          int64
	Limit                                                     int
	HypothesisIDs                                             []int64
}

var errArguments = errors.New("invalid tool arguments")

func (b builtin) parse(raw string) (arguments, error) {
	var values map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &values) != nil || values == nil {
		return arguments{}, errArguments
	}
	a := arguments{Limit: 10}
	for key, f := range b.fields {
		if _, ok := values[key]; !ok && f.required {
			return arguments{}, errArguments
		}
	}
	for key, value := range values {
		f, ok := b.fields[key]
		if !ok || string(value) == "null" {
			return arguments{}, errArguments
		}
		switch f.kind {
		case "string":
			var s string
			if json.Unmarshal(value, &s) != nil || strings.TrimSpace(s) == "" {
				return arguments{}, errArguments
			}
			switch key {
			case "text":
				a.Text = s
			case "query":
				a.Query = s
			case "at":
				a.At = s
			case "url":
				a.URL = s
			case "target_url":
				a.TargetURL = s
			case "target_username":
				a.TargetUsername = s
			case "username":
				a.Username = s
			}
		case "integer":
			if json.Unmarshal(value, &a.Limit) != nil || a.Limit < 1 || a.Limit > 100 {
				return arguments{}, errArguments
			}
		case "array":
			if json.Unmarshal(value, &a.HypothesisIDs) != nil || a.HypothesisIDs == nil {
				return arguments{}, errArguments
			}
			for _, id := range a.HypothesisIDs {
				if id <= 0 {
					return arguments{}, errArguments
				}
			}
		}
	}
	return a, nil
}
