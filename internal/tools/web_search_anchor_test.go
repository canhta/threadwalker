package tools_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/quality"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/tools"
	"github.com/canhta/threadz/internal/trends"
)

// A post anchored to a web_search source reaches the critic with what the
// search returned (the source's title and the answer), not the bare URL.
func TestPostAnchoredToAWebSearchSourceCarriesWhatTheSearchReturned(t *testing.T) {
	search := func(context.Context, trends.WebSearchRequest) (trends.WebSearchResult, error) {
		return trends.WebSearchResult{Answer: "Giá vé tăng lên 3 triệu.", Sources: []trends.WebSource{{Title: "Vé concert", URL: "https://news.invalid/ve"}}}, nil
	}
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "anchor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	tmpl := config.Template{ID: "viral", DryRun: true,
		LLM:       config.LLMConfig{APIKey: config.NewSecret("key")},
		Limits:    config.LimitsConfig{PostsPerDay: 3, RepliesPerDay: 5, RepliesPerUser7d: 1, MinSecondsBetweenActions: 1, MaxStepsPerWake: 10, MaxUSDPerDay: 1, MinMinutesBetweenWakes: 1},
		WebSearch: config.WebSearchConfig{Enabled: true, Model: "search-model", BaseURL: "https://search.invalid", MaxOutput: 256, MaxResults: 5},
	}
	tmpl.Guard.TrendAnchor = true
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	wakeID, err := st.BeginWake(ctx, tmpl.ID, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var seen quality.CriticInput
	critic := func(_ context.Context, in quality.CriticInput) (quality.CriticVerdict, error) {
		seen = in
		return quality.CriticVerdict{Pass: true, Scores: quality.CriticScores{Hook: 3, NaturalVietnamese: 3, Specificity: 3, ForcedSelfReference: 5, TargetFit: 3, Novelty: 3, Clarity: 5}}, nil
	}
	d, err := tools.NewDispatcher(tools.Options{Scope: guard.Scope{Store: st, Template: tmpl, WakeID: wakeID, Now: func() time.Time { return now }, Critic: critic}, SearchWeb: search, FetchTrends: func(context.Context) ([]trends.Trend, error) { return nil, nil }})
	if err != nil {
		t.Fatal(err)
	}
	call := func(name, args string) string {
		t.Helper()
		out, err := d.Dispatch(ctx, llm.ToolCall{ID: name, Type: "function", Function: llm.CallFunction{Name: name, Arguments: args}})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}
	if out := call("web_search", `{"query":"vé concert"}`); !strings.Contains(out, `"ref":"t1"`) {
		t.Fatalf("source has no ref: %s", out)
	}
	call("post", `{"text":"vé lên 3 triệu rồi mn","hook":"number","rationale":"r","trend":"t1"}`)
	if !strings.Contains(seen.Parent, "Vé concert") || !strings.Contains(seen.Parent, "Giá vé tăng lên 3 triệu.") {
		t.Fatalf("critic parent %q lacks what the search returned", seen.Parent)
	}
}
