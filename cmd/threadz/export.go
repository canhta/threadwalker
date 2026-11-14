package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"slices"

	"github.com/canhta/threadz/internal/eval"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/store"
)

// replayTools are the tools a replay case keeps: the reads that issue the refs
// a write names, and the writes themselves.
var (
	replayReads  = []string{"fetch_trends", "find_posts", "read_post", "read_replies"}
	replayWrites = []string{"post", "reply"}
)

func ctlWakeExport(ctx context.Context, getenv func(string) string, args []string, stdout, stderr io.Writer) int {
	var keepText, briefs *bool
	id, f, ok := wakeCommand("wake-export", getenv, args, stderr, func(fs *flag.FlagSet) {
		keepText = fs.Bool("keep-text", false, "keep the text of posts other people wrote")
		briefs = fs.Bool("briefs", false, "export the wake's writes as a briefs case for a live replay of the writer and critic, not as a replay case")
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
	if *briefs {
		abs, _ := filepath.Abs(*f.agents)
		if suite, err = briefsSuite(suite, id, *f.agent, abs); err != nil {
			fmt.Fprintln(stderr, err)
			return exitFailure
		}
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
	var topLiked *eval.TopPost // the most-liked post read so far
	// A replay starts on an empty database, so its posts are numbered p1, p2, ...
	// in the order the wake's reads showed them; a write names them by that number.
	numbering := map[string]string{}
	for i, call := range issued {
		name := call.Function.Name
		isRead, isWrite := slices.Contains(replayReads, name), slices.Contains(replayWrites, name)
		if !isRead && !isWrite {
			continue
		}
		step := eval.ReplayStep{Call: llm.ToolCall{Type: "function", Function: call.Function}}
		if isWrite {
			step.Call.Function.Arguments = renumberRefs(step.Call.Function.Arguments, numbering)
			step.Brief = briefOf(id, name, calls[i].Detail, topLiked, keepText)
			step.ExpectedBlocked = calls[i].BlockedBy
			if step.ExpectedBlocked == "" {
				rc.ExpectedActions++
			}
		} else if r := results[call.ID]; !r.Error && calls[i].BlockedBy == "" {
			if r.Truncated {
				return nil, fmt.Errorf("exporting wake: the %s result was cut when it was stored", name)
			}
			output := json.RawMessage(r.Output)
			if p := mostLiked(output); p != nil && (topLiked == nil || p.Likes > topLiked.Likes) {
				topLiked = p
			}
			if !keepText {
				output = withoutPostText(output)
			}
			for _, ref := range postRefs(output) {
				if _, ok := numbering[ref]; !ok {
					numbering[ref] = store.PostRef(int64(len(numbering) + 1))
				}
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

// postRefs lists the refs of the posts of a recorded read, in order.
func postRefs(output json.RawMessage) []string {
	var wrapped struct {
		Data struct {
			Posts []struct {
				Ref string `json:"ref"`
			} `json:"posts"`
		} `json:"data"`
		Posts []struct {
			Ref string `json:"ref"`
		} `json:"posts"`
	}
	var refs []string
	if json.Unmarshal(output, &wrapped) != nil {
		return nil
	}
	for _, p := range append(wrapped.Data.Posts, wrapped.Posts...) {
		if p.Ref != "" {
			refs = append(refs, p.Ref)
		}
	}
	return refs
}

// renumberRefs rewrites the post refs a write's target and trend name to their
// replay numbers; a ref the wake's reads did not show (a post stored by an
// earlier wake) stays, and the replay refuses it as unknown.
func renumberRefs(arguments string, numbering map[string]string) string {
	var args map[string]json.RawMessage
	if json.Unmarshal([]byte(arguments), &args) != nil {
		return arguments
	}
	for _, key := range []string{"target", "trend"} {
		var ref string
		if json.Unmarshal(args[key], &ref) == nil {
			if n, ok := numbering[ref]; ok {
				args[key], _ = json.Marshal(n)
			}
		}
	}
	out, err := json.Marshal(args)
	if err != nil {
		return arguments
	}
	return string(out)
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

// mostLiked is the post with the most likes among the posts of a recorded read, or nil.
func mostLiked(output json.RawMessage) *eval.TopPost {
	var best *eval.TopPost
	var walk func(any)
	walk = func(n any) {
		switch n := n.(type) {
		case map[string]any:
			if posts, ok := n["posts"].([]any); ok {
				for _, p := range posts {
					m, _ := p.(map[string]any)
					if likes, ok := m["likes"].(float64); ok && (best == nil || int64(likes) > best.Likes) {
						text, _ := m["text"].(string)
						user, _ := m["username"].(string)
						best = &eval.TopPost{Username: user, Likes: int64(likes), Text: text}
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
	var v any
	if json.Unmarshal(output, &v) == nil {
		walk(v)
	}
	return best
}

// briefOf is the writer and critic exchange a write's detail recorded, or nil
// when it recorded none. The text answered and the top-liked post are other
// people's, so they are dropped unless keepText.
func briefOf(wake int64, tool string, d store.CallDetail, top *eval.TopPost, keepText bool) *eval.Brief {
	if d.Brief == nil {
		return nil
	}
	b := &eval.Brief{Wake: wake, Kind: tool, Angle: d.Brief.Angle, Fact: d.Brief.Fact, Hook: d.Brief.Hook, Rough: d.Brief.Rough, Recent: d.Brief.Recent}
	if top != nil {
		t := *top
		b.TopLiked = &t
	}
	if keepText {
		b.Parent = d.Brief.Parent
	} else if b.TopLiked != nil {
		b.TopLiked.Text = ""
	}
	for _, t := range slices.Backward(d.Tries) {
		if len(t.Texts) > 0 {
			b.Recorded = t.Texts
			break
		}
	}
	return b
}

// briefsSuite turns an exported replay suite into a live-only briefs suite of
// one case, from the briefs its write steps recorded.
func briefsSuite(replay *eval.Suite, wake int64, agentID, templatesDir string) (*eval.Suite, error) {
	if agentID == "" {
		return nil, errors.New("exporting briefs: --agent is required")
	}
	bc := &eval.BriefsCase{TemplateDir: templatesDir, TemplateID: agentID}
	for _, step := range replay.Cases[0].Replay.Steps {
		if step.Brief != nil {
			bc.Briefs = append(bc.Briefs, *step.Brief)
		}
	}
	if len(bc.Briefs) == 0 {
		return nil, errors.New("exporting briefs: the wake recorded no writer exchange (it wrote nothing, or ran before briefs were traced)")
	}
	c := eval.Case{ID: fmt.Sprintf("briefs_wake_%d", wake), Kind: "briefs", Description: fmt.Sprintf("Live replay of the writes of wake %d through the current writer and critic.", wake), Briefs: bc, Expect: eval.Expectation{EndReason: "completed"}}
	return &eval.Suite{Description: "Briefs exported from a stored wake; live only.", Cases: []eval.Case{c}}, nil
}
