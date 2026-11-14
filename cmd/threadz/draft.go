package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/quality"
	"github.com/canhta/threadz/internal/store"
)

// draftItem is one trend or feed post to answer. Angle and Fact are the brief
// the agent would give; when empty the writer gets the item text as the fact.
type draftItem struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"` // post (about a trend) or reply (to a feed post)
	Text  string `json:"text"` // the trend headline or the feed post
	Angle string `json:"angle,omitempty"`
	Fact  string `json:"fact,omitempty"`
}

type draftResult struct {
	ID     string        `json:"id"`
	Kind   string        `json:"kind"`
	Passed bool          `json:"passed"`
	Tries  []quality.Try `json:"tries"`
	Error  string        `json:"error,omitempty"`
}

const defaultAngle = "react in the persona's own voice with one take that readers following this would answer or share"

// ctlDraft runs the writer and the critic on a fixed set of items without
// publishing: no guard, no Threads, a scratch database for the spend ledger.
func ctlDraft(ctx context.Context, getenv func(string) string, args []string, stdout, stderr io.Writer) int {
	f := newCtlFlags("draft", getenv)
	input := f.fs.String("input", "", "JSON file with the items to draft")
	if _, ok := f.parse(args, true, stderr); !ok {
		return exitUsage
	}
	if *input == "" {
		return ctlUsageError(stderr, "draft needs --input FILE")
	}
	var file struct {
		Items []draftItem `json:"items"`
	}
	raw, err := os.ReadFile(*input)
	if err == nil {
		err = json.Unmarshal(raw, &file)
	}
	if err != nil || len(file.Items) == 0 {
		fmt.Fprintln(stderr, "reading draft input failed: a JSON file with a non-empty items list is required")
		return exitFailure
	}
	templates, err := f.load(config.Options{Getenv: getenv})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	var tmpl *config.Template
	for i := range templates {
		if templates[i].ID == *f.agent {
			tmpl = &templates[i]
		}
	}
	if tmpl == nil {
		fmt.Fprintln(stderr, "unknown agent "+*f.agent)
		return exitFailure
	}
	if tmpl.Writer.Model == "" {
		fmt.Fprintln(stderr, "this agent has no writer.llm block; there is nothing to draft with")
		return exitFailure
	}
	dir, err := os.MkdirTemp("", "threadz-draft-")
	if err != nil {
		fmt.Fprintln(stderr, "creating scratch database failed")
		return exitFailure
	}
	defer os.RemoveAll(dir)
	st, err := store.Open(ctx, filepath.Join(dir, "draft.db"))
	if err != nil {
		fmt.Fprintln(stderr, "opening scratch database failed")
		return exitFailure
	}
	defer st.Close()
	now := time.Now()
	var wakeID int64
	if err = st.EnsureAgent(ctx, tmpl.ID); err == nil {
		wakeID, err = st.BeginWake(ctx, tmpl.ID, now, now.Add(time.Hour))
	}
	if err != nil {
		fmt.Fprintln(stderr, "starting scratch wake failed")
		return exitFailure
	}
	rv := agent.NewReviewer(agent.ReviewerOptions{Store: st, Template: *tmpl, Now: time.Now, Redactor: config.NewRedactor(templates)}, wakeID)
	results := make([]draftResult, 0, len(file.Items))
	failed := false
	for _, it := range file.Items {
		res := draftResult{ID: it.ID, Kind: it.Kind}
		if it.Kind != quality.Post && it.Kind != quality.Reply {
			res.Error = "kind must be post or reply"
		} else {
			in := quality.WriterInput{Kind: it.Kind, Angle: orDefault(it.Angle, defaultAngle), Fact: orDefault(it.Fact, it.Text), Parent: it.Text, Draft: []string{}}
			res.Tries, res.Passed, err = quality.Revise(ctx, rv.Writer, rv.Critic, nil, in, 2)
			if err != nil {
				res.Error = err.Error()
			}
		}
		failed = failed || res.Error != ""
		results = append(results, res)
	}
	code := printResult(stdout, *f.json, results, func(w io.Writer, all []draftResult) {
		for _, r := range all {
			verdict := "REJECTED"
			if r.Passed {
				verdict = "PASSED"
			}
			fmt.Fprintf(w, "== %s (%s): %s\n", r.ID, r.Kind, verdict)
			for i, t := range r.Tries {
				fmt.Fprintf(w, "-- try %d\n", i+1)
				for _, text := range t.Texts {
					fmt.Fprintf(w, "%s\n", text)
				}
				s := t.Verdict.Scores
				fmt.Fprintf(w, "   critic pass=%v hook=%d vi=%d specific=%d self=%d fit=%d novelty=%d clear=%d\n", t.Verdict.Pass, s.Hook, s.NaturalVietnamese, s.Specificity, s.ForcedSelfReference, s.TargetFit, s.Novelty, s.Clarity)
				for _, p := range t.Verdict.Problems {
					fmt.Fprintf(w, "   problem: %s\n", p)
				}
			}
			if r.Error != "" {
				fmt.Fprintf(w, "   error: %s\n", r.Error)
			}
		}
	})
	if usage, uerr := st.LLMUsageForDay(ctx, tmpl.ID, tmpl.Limits.Day(now)); uerr == nil && !*f.json {
		fmt.Fprintf(stdout, "spend: $%.4f, %d tokens in, %d out\n", usage.CostUSD, usage.TokensIn, usage.TokensOut)
	}
	if failed {
		return exitFailure
	}
	return code
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
