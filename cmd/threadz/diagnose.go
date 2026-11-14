package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/stdx"
	"github.com/canhta/threadz/internal/store"
)

// diagnosis is the --json shape of `wake-diagnose`: one wake as a funnel from
// reads to published actions, read from the audit rows alone.
type diagnosis struct {
	Wake   wakeSummary     `json:"wake"`
	Funnel diagnosisFunnel `json:"funnel"`
	Calls  []diagnosedCall `json:"calls"`
}

type diagnosisFunnel struct {
	Calls       int `json:"calls"`
	Reads       int `json:"reads"`
	ReadsFailed int `json:"reads_failed"`
	Writes      int `json:"writes"`
	Blocked     int `json:"blocked"`
	Failed      int `json:"failed"`
	Published   int `json:"published"`
}

type diagnosedCall struct {
	Seq     int              `json:"seq"`
	Tool    string           `json:"tool"`
	At      time.Time        `json:"at"`
	Outcome string           `json:"outcome"` // ok, blocked, failed or unfinished
	Reason  string           `json:"reason,omitempty"`
	Args    map[string]any   `json:"args,omitempty"`
	Counts  map[string]int   `json:"counts,omitempty"`
	Ref     *store.RefTrace  `json:"ref,omitempty"`
	Tries   []store.TryTrace `json:"tries,omitempty"`
	Detail  string           `json:"detail,omitempty"`
	Stage   string           `json:"stage,omitempty"`
	// Evidence is the saved page snapshot of a failed browser step.
	Evidence string           `json:"evidence,omitempty"`
	Action   *diagnosedAction `json:"action,omitempty"`
}

type diagnosedAction struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	ThreadURL string `json:"thread_url,omitempty"`
	DryRun    bool   `json:"dry_run"`
	Failed    bool   `json:"failed"`
}

// maxDiagnosedField cuts one printed argument, in runes.
const maxDiagnosedField = 160

func ctlWakeDiagnose(ctx context.Context, getenv func(string) string, args []string, stdout, stderr io.Writer) int {
	id, f, ok := wakeCommand("wake-diagnose", getenv, args, stderr)
	if !ok {
		return exitUsage
	}
	st, ok := openReadOnly(ctx, *f.db, stderr)
	if !ok {
		return exitFailure
	}
	defer st.Close()
	d, err := diagnoseWake(ctx, st, *f.agent, id)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	return printResult(stdout, *f.json, d, renderDiagnosis)
}

// wakeCommand parses `<id> --agent ID ...` for the commands that read one wake.
func wakeCommand(name string, getenv func(string) string, args []string, stderr io.Writer, extra ...func(*flag.FlagSet)) (int64, ctlFlags, bool) {
	f := newCtlFlags(name, getenv)
	for _, define := range extra {
		define(f.fs)
	}
	if len(args) == 0 {
		ctlUsageError(stderr, name+" takes a wake id")
		return 0, f, false
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id < 1 {
		ctlUsageError(stderr, name+" takes a numeric wake id")
		return 0, f, false
	}
	_, ok := f.parse(args[1:], true, stderr)
	return id, f, ok
}

func diagnoseWake(ctx context.Context, st *store.Store, agentID string, id int64) (diagnosis, error) {
	var d diagnosis
	wake, err := findWake(ctx, st, agentID, id)
	if err != nil {
		return d, err
	}
	d.Wake = wake
	calls, err := st.WakeCalls(ctx, agentID, id)
	if err != nil {
		return d, err
	}
	var actionIDs []int64
	for _, c := range calls {
		if c.Detail.ActionID > 0 {
			actionIDs = append(actionIDs, c.Detail.ActionID)
		}
	}
	actions, err := st.ActionOutcomes(ctx, agentID, actionIDs)
	if err != nil {
		return d, err
	}
	d.Calls = []diagnosedCall{}
	for i, c := range calls {
		dc := diagnoseCall(i+1, c)
		if a, ok := actions[c.Detail.ActionID]; ok {
			dc.Action = &diagnosedAction{ID: c.Detail.ActionID, Kind: a.Kind, ThreadURL: a.ThreadURL, DryRun: a.DryRun, Failed: a.Failed}
		}
		d.Calls = append(d.Calls, dc)
		d.Funnel.add(dc)
	}
	return d, nil
}

func (f *diagnosisFunnel) add(c diagnosedCall) {
	f.Calls++
	spec, _ := guard.Spec(c.Tool)
	switch spec.Effect {
	case guard.EffectRead:
		f.Reads++
		if c.Outcome != "ok" {
			f.ReadsFailed++
		}
	case guard.EffectThreadsWrite:
		f.Writes++
		switch c.Outcome {
		case "blocked":
			f.Blocked++
		case "failed":
			f.Failed++
		}
		if c.Action != nil && !c.Action.Failed && !c.Action.DryRun && c.Action.ThreadURL != "" {
			f.Published++
		}
	}
}

func diagnoseCall(seq int, c store.CallRecord) diagnosedCall {
	dc := diagnosedCall{Seq: seq, Tool: c.Tool, At: c.At, Outcome: "unfinished", Counts: c.Detail.Counts, Ref: c.Detail.Ref, Tries: c.Detail.Tries, Detail: c.Detail.Detail, Stage: c.Detail.Stage, Evidence: c.Detail.Evidence}
	var result struct {
		Outcome string `json:"outcome"`
		Reason  string `json:"reason"`
	}
	_ = json.Unmarshal(c.Result, &result)
	switch {
	case c.BlockedBy != "":
		dc.Outcome, dc.Reason = "blocked", c.BlockedBy
	case result.Outcome == "success":
		dc.Outcome = "ok"
	case result.Outcome == "error":
		dc.Outcome, dc.Reason = "failed", result.Reason
	}
	spec, _ := guard.Spec(c.Tool)
	if spec.Effect == guard.EffectRead || spec.Effect == guard.EffectThreadsWrite {
		dc.Args = diagnosedArgs(c.Args)
	}
	return dc
}

// diagnosedArgs keeps what identifies a call: a text as its first line and
// length, a series as its part count, every other string cut short.
func diagnosedArgs(raw json.RawMessage) map[string]any {
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil {
		return nil
	}
	out := map[string]any{}
	for k, v := range args {
		switch v := v.(type) {
		case string:
			if k == "text" {
				first, _, _ := strings.Cut(strings.TrimSpace(v), "\n")
				out["text"] = cutRunes(first, maxDiagnosedField)
				out["text_runes"] = len([]rune(v))
			} else {
				out[k] = cutRunes(v, maxDiagnosedField)
			}
		case []any:
			out[k+"_count"] = len(v)
		default:
			out[k] = v
		}
	}
	return out
}

func cutRunes(s string, n int) string {
	if cut := stdx.CapRunes(s, n); cut != s {
		return cut + "..."
	}
	return s
}

func renderDiagnosis(w io.Writer, d diagnosis) {
	fmt.Fprintf(w, "wake #%d  %s  %s  %d steps  $%.4f\n", d.Wake.ID, d.Wake.Kind, orDash(d.Wake.EndReason), d.Wake.Steps, d.Wake.CostUSD)
	f := d.Funnel
	fmt.Fprintf(w, "funnel: %d calls; %d reads (%d failed); %d writes (%d blocked, %d failed); %d published\n", f.Calls, f.Reads, f.ReadsFailed, f.Writes, f.Blocked, f.Failed, f.Published)
	if len(d.Calls) == 0 {
		fmt.Fprintln(w, "no tool calls are stored for this wake")
	}
	for _, c := range d.Calls {
		verdict := c.Outcome
		if c.Reason != "" {
			verdict += " " + c.Reason
		}
		if c.Stage != "" {
			verdict += " at " + c.Stage
		}
		fmt.Fprintf(w, "\n#%d %s  %s  %s\n", c.Seq, c.At.UTC().Format("15:04:05"), c.Tool, verdict)
		if len(c.Counts) > 0 {
			fmt.Fprintf(w, "    got    %s\n", joinCounts(c.Counts))
		}
		if len(c.Args) > 0 {
			fmt.Fprintf(w, "    args   %s\n", joinArgs(c.Args))
		}
		if r := c.Ref; r != nil {
			fmt.Fprintf(w, "    %s  given %q", r.Field, r.Given)
			if r.Resolved != "" {
				fmt.Fprintf(w, " -> %q\n", r.Resolved)
			} else {
				fmt.Fprintf(w, " -> no noted item (%d refs this wake)\n", r.Noted)
			}
		}
		if c.Detail != "" {
			fmt.Fprintf(w, "    detail %s\n", c.Detail)
		}
		for i, t := range c.Tries {
			fmt.Fprintf(w, "    draft %d [%s] %v runes, first line %q", i+1, t.Source, t.Runes, t.First)
			if t.Source != "agent" || len(t.Scores) > 0 {
				fmt.Fprintf(w, "  pass=%t scores %s", t.Pass, t.Scores)
			}
			fmt.Fprintln(w)
			for _, p := range t.Problems {
				fmt.Fprintf(w, "        problem: %s\n", p)
			}
		}
		if c.Evidence != "" {
			fmt.Fprintf(w, "    snapshot %s\n", c.Evidence)
		}
		if a := c.Action; a != nil {
			state := "recorded"
			switch {
			case a.Failed:
				state = "failed"
			case a.DryRun:
				state = "dry run"
			case a.ThreadURL != "":
				state = "published " + a.ThreadURL
			}
			fmt.Fprintf(w, "    action #%d %s %s\n", a.ID, a.Kind, state)
		}
	}
}

func joinCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %d", k, m[k])
	}
	return strings.Join(parts, ", ")
}

func joinArgs(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%v", k, m[k])
		if s, ok := m[k].(string); ok {
			parts[i] = fmt.Sprintf("%s=%q", k, s)
		}
	}
	return strings.Join(parts, " ")
}
