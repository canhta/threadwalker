package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/quality"
	"github.com/canhta/threadz/internal/store"
)

// qualityFixtureDigest binds a suite to the templates and rubric it scores, so
// a persona or rubric edit breaks comparisons.
func qualityFixtureDigest(cases []Case) string {
	h := sha256.New()
	seen := map[string]bool{}
	for _, c := range cases {
		if c.Quality == nil || seen[c.Quality.resolvedDir] {
			continue
		}
		seen[c.Quality.resolvedDir] = true
		paths, _ := filepath.Glob(filepath.Join(c.Quality.resolvedDir, "*.yaml"))
		sort.Strings(paths)
		for _, p := range paths {
			b, _ := os.ReadFile(p)
			h.Write(b)
		}
	}
	if len(seen) == 0 {
		return ""
	}
	h.Write(quality.RubricJSON())
	return hex.EncodeToString(h.Sum(nil))
}

// textBodies scripts one plain-text assistant reply for each protocol.
func textBodies(text string, tokensIn, tokensOut int) map[string][]Response {
	content, _ := json.Marshal(text)
	openai := fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":%d,"completion_tokens":%d}}`, content, tokensIn, tokensOut)
	anthropic := fmt.Sprintf(`{"id":"msg_quality","type":"message","role":"assistant","content":[{"type":"text","text":%s}],"stop_reason":"end_turn","usage":{"input_tokens":%d,"output_tokens":%d}}`, content, tokensIn, tokensOut)
	return map[string][]Response{
		"openai":    {{Status: 200, Body: json.RawMessage(openai)}},
		"anthropic": {{Status: 200, Body: json.RawMessage(anthropic)}},
	}
}

// runQualityCase builds the wake context the running agent would receive,
// asks the model for one draft, has the model judge it against the persona's
// rules, and compares the failed checks with the case's expectation.
func runQualityCase(ctx context.Context, c Case, p Profile, repeat int, opts Options, live *reservation) (r CaseResult) {
	r = CaseResult{CaseID: c.ID, ProfileID: p.ID, ProviderID: p.ProviderID, Model: p.Config.Model, Protocol: p.Config.Protocol, Repeat: repeat, EndReason: "completed", Reason: "quality_setup_failed", ToolCalls: []string{}}
	started := time.Now()
	defer func() { r.DurationMS = float64(time.Since(started).Nanoseconds()) / 1e6 }()
	q := c.Quality
	rubric, err := quality.DefaultRubric()
	if err != nil {
		return r
	}
	tmpl, wake, err := qualityWake(ctx, q, opts.Now().UTC().Add(48*time.Hour))
	if err != nil {
		return r
	}
	ask := llm.Message{Role: "user", Content: fmt.Sprintf("Write the text of the one %s you would send now. Reply with that text only.", q.Kind)}

	draftCase := Case{ID: c.ID, Messages: append(wake, ask), Responses: textBodies(q.Draft, 100, 20), Expect: Expectation{EndReason: "completed"}, Limits: c.Limits}
	dr, draft := runChat(ctx, draftCase, p, repeat, opts, live)
	judgeCase := Case{ID: c.ID, Messages: quality.JudgeMessages(rubric, tmpl.Persona, q.Kind, draft), Responses: textBodies(q.Judge, 100, 20), Expect: Expectation{EndReason: "completed"}, Limits: c.Limits}
	if !dr.Passed {
		return withUsage(r, dr, dr.EndReason, dr.Reason)
	}
	jr, verdictText := runChat(ctx, judgeCase, p, repeat, opts, live)
	r = withUsage(r, dr, "completed", "")
	r.TokensIn += jr.TokensIn
	r.TokensOut += jr.TokensOut
	r.CostUSD += jr.CostUSD
	if !jr.Passed {
		r.EndReason, r.Reason = jr.EndReason, jr.Reason
		return r
	}
	verdict, err := quality.ParseVerdict(verdictText, len(tmpl.Persona.StyleRules))
	if err != nil {
		r.Reason = "judge_verdict_invalid"
		return r
	}
	r.Quality = quality.Score(rubric, tmpl.Persona, q.Kind, draft, wake[1].Content, verdict)
	r.Passed, r.Reason = qualityOutcome(opts.Mode, r.Quality, q.Fails)
	return r
}

func withUsage(r, from CaseResult, end, reason string) CaseResult {
	r.Steps, r.TokensIn, r.TokensOut, r.CostUSD = from.Steps, from.TokensIn, from.TokensOut, from.CostUSD
	r.EndReason = end
	if reason != "" {
		r.Reason = reason
	}
	return r
}

// qualityOutcome gates an offline run on the recorded expectation. A live
// draft has no expectation: the case passes once scored, and the pass rates are
// the result.
func qualityOutcome(mode string, checks []quality.Check, want []string) (bool, string) {
	if mode == "live" {
		return true, "scored"
	}
	return failedChecksMatch(checks, want)
}

func failedChecksMatch(checks []quality.Check, want []string) (bool, string) {
	got := map[string]bool{}
	for _, c := range checks {
		if !c.Pass {
			got[c.ID] = true
		}
	}
	if len(got) != len(want) {
		return false, "quality_mismatch"
	}
	for _, id := range want {
		if !got[id] {
			return false, "quality_mismatch"
		}
	}
	return true, "matched"
}

// qualityWake returns the template and the messages its agent would receive on
// a normal wake at now, after the case's earlier actions.
func qualityWake(ctx context.Context, q *QualityCase, now time.Time) (*config.Template, []llm.Message, error) {
	tmpls, err := config.Load(q.resolvedDir, config.Options{Getenv: func(string) string { return "eval-offline-dummy-key" }})
	if err != nil {
		return nil, nil, err
	}
	var tmpl *config.Template
	for i := range tmpls {
		if tmpls[i].ID == q.TemplateID {
			tmpl = &tmpls[i]
		}
	}
	if tmpl == nil {
		return nil, nil, fmt.Errorf("selecting quality template: %q not found", q.TemplateID)
	}
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		return nil, nil, err
	}
	defer st.Close()
	for _, a := range q.OwnActions {
		at := now.Add(-time.Duration(a.HoursAgo) * time.Hour)
		if _, err = st.SeedAction(ctx, store.SeededAction{AgentID: tmpl.ID, Kind: a.Kind, Text: a.Text, DryRun: true, PostedAt: at}); err != nil {
			return nil, nil, fmt.Errorf("seeding own actions: %w", err)
		}
	}
	fixture := agent.NewOfflineFixture(func() time.Time { return now })
	rt, err := agent.New(agent.Options{Store: st, Template: *tmpl, Now: func() time.Time { return now }, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends})
	if err != nil {
		return nil, nil, err
	}
	if err = rt.Start(ctx); err != nil {
		return nil, nil, err
	}
	messages, err := rt.Context(ctx, "agent")
	return tmpl, messages, err
}
