package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/metrics"
	"github.com/canhta/threadz/internal/quality"
	"github.com/canhta/threadz/internal/store"
)

const (
	maxPostMortemPairs = 5
	ownHistoryWindow   = 7 * 24 * time.Hour
	trendLookback      = 24 * time.Hour
)

// Reasons the judge adds no scores.
const (
	judgeNotConfigured = "not_configured"
	judgeDreamCap      = "dream_cap"
	judgeFailed        = "judge_failed"
)

type pmAction struct {
	ActionID   int64              `json:"action_id"`
	Hook       string             `json:"hook,omitempty"`
	Parts      int                `json:"parts,omitempty"`
	Rationale  string             `json:"rationale,omitempty"`
	Covariates metrics.Covariates `json:"covariates"`
	Features   quality.Features   `json:"features"`
	Scores     *quality.Scores    `json:"scores,omitempty"`
}

type pmPair struct {
	PairID       string              `json:"pair_id"`
	Distance     int                 `json:"distance"`
	Exact        bool                `json:"exact"`
	Attribution  metrics.Attribution `json:"attribution"`
	Viral        pmAction            `json:"viral"`
	Flop         pmAction            `json:"flop"`
	FeatureDiffs []string            `json:"feature_differences"`
}

// postMortem is the dream context block: matched viral and flop pairs of the
// last 14 days with the code features, the independent judge's scores and the
// split of the gap between context and content.
type postMortem struct {
	Pairs        []pmPair `json:"pairs"`
	JudgeSkipped string   `json:"judge_skipped,omitempty"`
}

// postMortem builds the block. The judge is billed to the dream phase under the
// dream cap; a reached cap or a failed judge leaves the pairs without scores.
func (r *Runtime) postMortem(ctx context.Context, wakeID int64) (postMortem, error) {
	t, now := r.opts.Template, r.opts.Now()
	block := postMortem{Pairs: []pmPair{}}
	tz, err := time.LoadLocation(t.Schedule.Timezone)
	if err != nil {
		return block, err
	}
	pairs, err := metrics.MatchedPairs(ctx, r.opts.Store, t.ID, tz, now)
	if err != nil || len(pairs) == 0 {
		return block, err
	}
	pairs = pairs[:min(len(pairs), maxPostMortemPairs)]
	texts, err := r.opts.Store.ActionTextsSince(ctx, t.ID, now.Add(-metrics.PairWindow-ownHistoryWindow))
	if err != nil {
		return block, err
	}
	stories, err := r.opts.Store.RecentTrendStories(ctx, t.ID, now.Add(-metrics.PairWindow-trendLookback), 500)
	if err != nil {
		return block, err
	}
	observed, err := r.opts.Store.RecentThreadsObservations(ctx, t.ID, now.Add(-metrics.PairWindow), 2000)
	if err != nil {
		return block, err
	}
	rubric, err := quality.DefaultRubric()
	if err != nil {
		return block, err
	}
	var judge *llm.Client
	if t.Review.Enabled {
		judge, err = llm.New(t.Review.LLM, t.Limits, r.opts.Store, llm.Options{Logger: r.opts.Logger, Now: r.opts.Now, Sleep: r.opts.Wait, HTTPClient: r.opts.HTTPClient, ReasoningEffort: t.Review.LLM.ReasoningEffort, Phase: "dream", CapUSD: t.Limits.MaxUSDPerDream, MaxSteps: 2})
		if err != nil {
			return block, err
		}
	} else {
		block.JudgeSkipped = judgeNotConfigured
	}
	byID := map[int64]store.ActionText{}
	for _, a := range texts {
		byID[a.ID] = a
	}
	describe := func(c metrics.Contender) pmAction {
		a := byID[c.ActionID]
		fc := quality.FeatureContext{BlockedPhrases: t.Guard.BlockedPhrases, Parent: parentSnippet(observed, a.TargetURL)}
		for _, s := range stories {
			if !s.ObservedAt.After(a.PostedAt) && !s.ObservedAt.Before(a.PostedAt.Add(-trendLookback)) {
				fc.TrendTitles = append(fc.TrendTitles, s.Title)
			}
		}
		for _, o := range texts {
			if o.ID != a.ID && o.PostedAt.Before(a.PostedAt) && !o.PostedAt.Before(a.PostedAt.Add(-ownHistoryWindow)) {
				fc.OwnHistory = append(fc.OwnHistory, o.Text)
			}
		}
		out := pmAction{ActionID: c.ActionID, Hook: a.Hook, Parts: a.Parts, Rationale: a.Rationale, Covariates: c.Cov, Features: quality.ExtractFeatures(a.Text, fc)}
		if judge != nil && block.JudgeSkipped == "" {
			scores, jerr := r.judgeAction(ctx, judge, rubric, wakeID, c.Cov.Kind, a.Text, fc.Parent)
			switch {
			case jerr == nil:
				out.Scores = &scores
			case errors.Is(jerr, llm.ErrBudget):
				block.JudgeSkipped = judgeDreamCap
			default:
				block.JudgeSkipped = judgeFailed
			}
			if jerr != nil {
				logging.Emit(ctx, r.opts.Logger, slog.LevelWarn, logging.Record{Event: logging.PostMortemJudged, AgentID: t.ID, Phase: "dream", WakeID: wakeID, Operation: "judge", Outcome: "skipped", Reason: block.JudgeSkipped})
			}
		}
		return out
	}
	for _, p := range pairs {
		v, f := describe(p.Viral), describe(p.Flop)
		block.Pairs = append(block.Pairs, pmPair{PairID: pairID(p), Distance: p.Distance, Exact: p.Exact, Attribution: p.Attribution(), Viral: v, Flop: f, FeatureDiffs: featureDiffs(v.Features, f.Features)})
	}
	return block, nil
}

func (r *Runtime) judgeAction(ctx context.Context, judge *llm.Client, rubric quality.Rubric, wakeID int64, kind, text, parent string) (quality.Scores, error) {
	redact := r.redactor().String
	msgs := quality.PostMortemMessages(rubric, r.opts.Template.Persona, kind, redact(text), redact(parent))
	res, err := judge.Run(ctx, r.opts.Template.ID, wakeID, msgs, nil, nil)
	if err != nil {
		return quality.Scores{}, err
	}
	return quality.ParseScores(res.Content)
}

func parentSnippet(observed []store.ThreadsObservation, target string) string {
	want, err := store.CanonicalThread(target)
	if err != nil {
		return ""
	}
	for _, o := range observed {
		if got, err := store.CanonicalThread(o.PostURL); err == nil && got == want {
			return o.Snippet
		}
	}
	return ""
}

func pairID(p metrics.Pair) string {
	return fmt.Sprintf("pm:%d-%d", p.Viral.ActionID, p.Flop.ActionID)
}

// featureDiffs names the features on which the viral and the flop action differ.
func featureDiffs(a, b quality.Features) []string {
	var ma, mb map[string]any
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	_ = json.Unmarshal(ja, &ma)
	_ = json.Unmarshal(jb, &mb)
	out := []string{}
	for k := range ma {
		if ma[k] != mb[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
