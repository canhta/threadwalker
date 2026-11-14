package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/quality"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/tools"
	"github.com/canhta/threadz/internal/trends"
)

// ReplayCase replays the tool calls of one wake through the production
// dispatcher and guard on temporary SQLite. Reads are not executed against
// anything: a step's Output is what the read returned in the wake, served by
// the fake backend when the call is dispatched, so refs are issued and noted
// as they were. Writers and critics are scripted; with neither, the agent's
// own text is published. ctl wake export writes this shape from a stored wake.
type ReplayCase struct {
	// Live makes the template publish: a write reaches the fake backend, so a
	// step can fail it.
	Live bool `json:"live,omitempty"`
	// Writer lists the parts each writer call returns; Critic the verdicts of
	// each critic call, in order. Both are set together or not at all.
	Writer          [][]string              `json:"writer,omitempty"`
	Critic          []quality.CriticVerdict `json:"critic,omitempty"`
	Steps           []ReplayStep            `json:"steps"`
	ExpectedActions int                     `json:"expected_actions"`
}

// ReplayStep is one tool call. ExpectedBlocked is the guard reason the call
// must stop with, empty when it must pass. ExpectedDetail is a substring the
// call's stored detail must contain.
type ReplayStep struct {
	Call            llm.ToolCall    `json:"call"`
	Output          json.RawMessage `json:"output,omitempty"`
	FailWrite       *ReplayFailure  `json:"fail_write,omitempty"`
	ExpectedBlocked string          `json:"expected_blocked,omitempty"`
	ExpectedDetail  string          `json:"expected_detail,omitempty"`
	ExpectedStage   string          `json:"expected_stage,omitempty"`
	// Brief is the writer and critic exchange the wake recorded for this write; replay ignores it.
	Brief *Brief `json:"brief,omitempty"`
}

// ReplayFailure makes the fake backend fail the write with a browser reason
// at a stage; Snapshot is the page text saved as evidence.
type ReplayFailure struct {
	Reason   string `json:"reason"`
	Stage    string `json:"stage"`
	Snapshot string `json:"snapshot,omitempty"`
}

var replayReasons = map[string]error{"write_refused": threads.ErrWriteRefused, "malformed_page": threads.ErrMalformedPage, "unknown_page": threads.ErrUnknownPage}

func runReplayCase(ctx context.Context, c Case, p Profile, repeat int, opts Options) (r CaseResult) {
	run, err := beginCase(&r, c, p, repeat, "replay", "completed", "replay_setup_failed", nil)
	if err != nil {
		return r
	}
	defer run.end()
	if err = run.Open(ctx, "replay.db"); err != nil {
		return r
	}
	st := run.Store
	rc := c.Replay
	now := opts.Now().UTC()
	base := now
	tmpl := agent.OfflineTemplate()
	tmpl.ID = "synthetic-replay-eval"
	tmpl.DryRun = !rc.Live
	tmpl.Limits.PostsPerDay, tmpl.Limits.RepliesPerDay, tmpl.Limits.RepliesPerUser7d, tmpl.Limits.BrowserRepliesPerDay = 100, 100, 100, 100
	tmpl.Guard.TrendAnchor = true
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		return r
	}
	wakeID, err := st.BeginWake(ctx, tmpl.ID, now, now.Add(time.Hour))
	if err != nil {
		return r
	}
	mem := threads.NewMemory()
	var fetched []trends.Trend
	scope := guard.Scope{Store: st, Template: tmpl, WakeID: wakeID, Now: func() time.Time { return now }, Logger: run.Logger, AllowLive: rc.Live}
	var writes, judged int
	if len(rc.Writer) > 0 || len(rc.Critic) > 0 {
		scope.Writer = func(context.Context, quality.WriterInput) ([]string, error) {
			if writes >= len(rc.Writer) {
				return nil, errors.New("replay: no scripted writer output")
			}
			writes++
			return rc.Writer[writes-1], nil
		}
		scope.Critic = func(context.Context, quality.CriticInput) (quality.CriticVerdict, error) {
			if judged >= len(rc.Critic) {
				return quality.CriticVerdict{}, errors.New("replay: no scripted critic verdict")
			}
			judged++
			return rc.Critic[judged-1], nil
		}
	}
	d, err := tools.NewDispatcher(tools.Options{Scope: scope, Threads: mem, EvidenceDir: run.Path("evidence"), FetchTrends: func(context.Context) ([]trends.Trend, error) { return fetched, nil }})
	if err != nil {
		return r
	}
	for i, step := range rc.Steps {
		now = base.Add(time.Duration(i) * time.Minute)
		r.Steps++
		call := step.Call
		call.ID = fmt.Sprintf("synthetic-replay-%d", i)
		if len(step.Output) > 0 {
			op := call.Function.Name
			if op == "find_posts" {
				var a struct {
					Source string `json:"source"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &a)
				src, _ := guard.FindSourceOf(a.Source)
				op = src.Read
			}
			if err = serveRecorded(mem, &fetched, op, step.Output); err != nil {
				return run.fail("replay_output_invalid")
			}
		}
		if step.FailWrite != nil {
			mem.Errors[call.Function.Name] = replayFailure(*step.FailWrite)
		}
		_, callErr := d.Dispatch(ctx, call)
		calls, err := st.WakeCalls(ctx, tmpl.ID, wakeID)
		if err != nil || len(calls) != i+1 {
			return run.fail("guard_audit_missing")
		}
		got := calls[i]
		if got.BlockedBy != step.ExpectedBlocked || (callErr != nil && got.BlockedBy == "" && step.FailWrite == nil) {
			return run.fail("replay_outcome_mismatch")
		}
		encoded, _ := json.Marshal(got.Detail)
		if !strings.Contains(string(encoded), step.ExpectedDetail) {
			return run.fail("replay_detail_mismatch")
		}
		if step.ExpectedStage != "" {
			if got.Detail.Stage != step.ExpectedStage {
				return run.fail("replay_stage_mismatch")
			}
			if _, err := os.Stat(got.Detail.Evidence); step.FailWrite != nil && step.FailWrite.Snapshot != "" && err != nil {
				return run.fail("replay_evidence_missing")
			}
			outcomes, err := st.ActionOutcomes(ctx, tmpl.ID, []int64{got.Detail.ActionID})
			if err != nil || !outcomes[got.Detail.ActionID].Failed {
				return run.fail("replay_intent_not_failed")
			}
		}
		r.ToolCalls = append(r.ToolCalls, call.Function.Name)
	}
	if tally, err := st.ActionTally(ctx, tmpl.ID); err != nil || tally.Total != rc.ExpectedActions {
		return run.fail("replay_action_count_mismatch")
	}
	r.Passed, r.Reason = matches(c.Expect, r, "")
	return r
}

// serveRecorded makes the next dispatch of tool return what the wake's read returned.
func serveRecorded(mem *threads.Memory, fetched *[]trends.Trend, tool string, output json.RawMessage) error {
	var wrapped struct {
		Untrusted bool            `json:"untrusted"`
		Data      json.RawMessage `json:"data"`
	}
	data := output
	if json.Unmarshal(output, &wrapped) == nil && wrapped.Untrusted {
		data = wrapped.Data
	}
	if tool == "fetch_trends" {
		return json.Unmarshal(data, fetched)
	}
	var page threads.Page
	if err := json.Unmarshal(data, &page); err != nil {
		return err
	}
	mem.Pages[tool] = page
	return nil
}

func replayFailure(f ReplayFailure) error {
	cause := replayReasons[f.Reason]
	return &threads.StageError{Stage: f.Stage, Snapshot: f.Snapshot, Err: fmt.Errorf("threads reply: %w", cause)}
}
