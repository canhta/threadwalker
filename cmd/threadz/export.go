package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"

	"github.com/canhta/threadz/internal/eval"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/store"
)

// replayTools are the tools a replay case keeps: the reads that issue the refs
// a write names, and the writes themselves.
var (
	replayReads  = []string{"fetch_trends", "read_feed", "read_topic", "search", "read_post", "read_replies", "profile_posts"}
	replayWrites = []string{"post", "reply"}
)

func ctlWakeExport(ctx context.Context, getenv func(string) string, args []string, stdout, stderr io.Writer) int {
	var keepText *bool
	id, f, ok := wakeCommand("wake-export", getenv, args, stderr, func(fs *flag.FlagSet) {
		keepText = fs.Bool("keep-text", false, "keep the text of posts other people wrote")
	})
	if !ok {
		return exitUsage
	}
	st, ok := openReadOnly(ctx, *f.db, stderr)
	if !ok {
		return exitFailure
	}
	defer st.Close()
	suite, err := exportWake(ctx, st, *f.agent, id, *keepText)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(suite)
	return exitOK
}

// exportWake turns a wake's stored transcript into a replay suite of one
// case. The guard verdict of each write is what the wake saw, so the case
// passes against the build that produced it; edit expected_blocked to the
// outcome that should hold, then run it.
func exportWake(ctx context.Context, st *store.Store, agentID string, id int64, keepText bool) (*eval.Suite, error) {
	if _, err := findWake(ctx, st, agentID, id); err != nil {
		return nil, err
	}
	entries, err := st.Transcript(ctx, agentID, id)
	if err != nil {
		return nil, err
	}
	calls, err := st.WakeCalls(ctx, agentID, id)
	if err != nil {
		return nil, err
	}
	type result struct {
		Tool      string `json:"tool"`
		CallID    string `json:"tool_call_id"`
		Output    string `json:"output"`
		Truncated bool   `json:"truncated"`
		Error     bool   `json:"error"`
	}
	results := map[string]result{}
	var issued []llm.ToolCall
	for _, e := range entries {
		switch e.Kind {
		case store.TranscriptAssistant:
			var a struct {
				ToolCalls []llm.ToolCall `json:"tool_calls"`
			}
			if json.Unmarshal([]byte(e.Body), &a) == nil {
				issued = append(issued, a.ToolCalls...)
			}
		case store.TranscriptToolResult:
			var r result
			if json.Unmarshal([]byte(e.Body), &r) == nil {
				results[r.CallID] = r
			}
		}
	}
	if len(issued) != len(calls) {
		return nil, errors.New("exporting wake: the transcript and the audit disagree on the calls (a transcript cut by its bounds, or pruned)")
	}
	rc := &eval.ReplayCase{}
	var tools []string
	for i, call := range issued {
		name := call.Function.Name
		isRead, isWrite := slices.Contains(replayReads, name), slices.Contains(replayWrites, name)
		if !isRead && !isWrite {
			continue
		}
		step := eval.ReplayStep{Call: llm.ToolCall{Type: "function", Function: call.Function}}
		if isWrite {
			step.ExpectedBlocked = calls[i].BlockedBy
			if step.ExpectedBlocked == "" {
				rc.ExpectedActions++
			}
		} else if r := results[call.ID]; !r.Error && calls[i].BlockedBy == "" {
			if r.Truncated {
				return nil, fmt.Errorf("exporting wake: the %s result was cut when it was stored", name)
			}
			output := json.RawMessage(r.Output)
			if !keepText {
				output = withoutPostText(output)
			}
			step.Output = output
		}
		rc.Steps = append(rc.Steps, step)
		tools = append(tools, name)
	}
	if len(rc.Steps) == 0 {
		return nil, errors.New("exporting wake: it made no read or write call")
	}
	c := eval.Case{ID: fmt.Sprintf("replay_wake_%d", id), Kind: "replay", Description: fmt.Sprintf("Replay of wake %d: recorded reads, the agent's calls, the guard's verdicts.", id), Replay: rc, Expect: eval.Expectation{EndReason: "completed", Tools: tools}}
	return &eval.Suite{Description: "Replay case exported from a stored wake; review and sanitize before committing.", Cases: []eval.Case{c}}, nil
}

// withoutPostText blanks the text of every post in a recorded read, keeping
// its URL, id, username and counters, which refs and reply checks need.
func withoutPostText(output json.RawMessage) json.RawMessage {
	var v any
	if json.Unmarshal(output, &v) != nil {
		return output
	}
	var walk func(any)
	walk = func(n any) {
		switch n := n.(type) {
		case map[string]any:
			if posts, ok := n["posts"].([]any); ok {
				for _, p := range posts {
					if m, ok := p.(map[string]any); ok {
						if _, has := m["text"]; has {
							m["text"] = ""
						}
					}
				}
			}
			for _, e := range n {
				walk(e)
			}
		case []any:
			for _, e := range n {
				walk(e)
			}
		}
	}
	walk(v)
	out, err := json.Marshal(v)
	if err != nil {
		return output
	}
	return out
}
