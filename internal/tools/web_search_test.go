package tools_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/tools"
	"github.com/canhta/threadz/internal/trends"
)

func TestWebSearchToolIsStudyOnlyAndRecordsStudySpend(t *testing.T) {
	search := func(context.Context, trends.WebSearchRequest) (trends.WebSearchResult, error) {
		return trends.WebSearchResult{Answer: "answer", Usage: store.LLMUsage{TokensIn: 1000, TokensOut: 1000}}, nil
	}
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "websearch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	tmpl := config.Template{ID: "viral", DryRun: true,
		LLM:    config.LLMConfig{APIKey: config.NewSecret("key"), PricePerMTokInUSD: 10, PricePerMTokOutUSD: 10},
		Limits: config.LimitsConfig{PostsPerDay: 3, RepliesPerDay: 5, RepliesPerUser7d: 1, MinSecondsBetweenActions: 1, MaxStepsPerWake: 10, MaxUSDPerDay: 1, MinMinutesBetweenWakes: 1},
		Study:  config.StudyConfig{WebSearch: true, WebSearchModel: "search-model", WebSearchBaseURL: "https://search.invalid", WebSearchMaxOutput: 256, WebSearchMaxResults: 5},
	}
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	wakeID, err := st.BeginPhaseWake(ctx, tmpl.ID, store.WakeStudy, "2026-01-01T09:00", now)
	if err != nil {
		t.Fatal(err)
	}
	d, err := tools.NewDispatcher(tools.Options{Embedder: testEmbedder(t, st), Scope: guard.Scope{Store: st, Template: tmpl, WakeID: wakeID, Phase: guard.PhaseStudy, Now: func() time.Time { return now }}, SearchWeb: search, FetchTrends: func(context.Context) ([]trends.Trend, error) { return nil, nil }, FetchStudy: func(context.Context) (trends.StudyFeed, error) { return trends.StudyFeed{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, definition := range d.Definitions() {
		names[definition.Function.Name] = true
	}
	if !names["web_search"] {
		t.Fatal("study dispatcher omits web_search")
	}
	out, err := d.Dispatch(ctx, llm.ToolCall{ID: "ws1", Type: "function", Function: llm.CallFunction{Name: "web_search", Arguments: `{"query":"Jensen Huang"}`}})
	if err != nil || !strings.Contains(out, "answer") || !strings.Contains(out, `"untrusted":true`) {
		t.Fatalf("out=%s err=%v", out, err)
	}
	usage, err := st.LLMUsageForDayPhases(ctx, tmpl.ID, "2026-01-01", "study")
	if err != nil || usage.CostUSD <= 0 {
		t.Fatalf("study spend=%+v err=%v", usage, err)
	}

	// A normal wake offers the tool, bounded per wake.
	if err = st.FinishWake(ctx, wakeID, now.Add(time.Minute), 3, "completed"); err != nil {
		t.Fatal(err)
	}
	agentWake, err := st.BeginWake(ctx, tmpl.ID, now.Add(time.Minute), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	agentDispatcher, err := tools.NewDispatcher(tools.Options{Embedder: testEmbedder(t, st), Scope: guard.Scope{Store: st, Template: tmpl, WakeID: agentWake, Now: func() time.Time { return now }}, SearchWeb: search, FetchTrends: func(context.Context) ([]trends.Trend, error) { return nil, nil }, FetchStudy: func(context.Context) (trends.StudyFeed, error) { return trends.StudyFeed{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	offered := false
	for _, definition := range agentDispatcher.Definitions() {
		offered = offered || definition.Function.Name == "web_search"
	}
	if !offered {
		t.Fatal("normal wake omits web_search")
	}
	for i := 1; i <= tools.MaxWebSearchesPerWake+1; i++ {
		out, err := agentDispatcher.Dispatch(ctx, llm.ToolCall{ID: fmt.Sprintf("a%d", i), Type: "function", Function: llm.CallFunction{Name: "web_search", Arguments: fmt.Sprintf(`{"query":"q%d"}`, i)}})
		if limited := err != nil && strings.Contains(err.Error(), "limit per wake"); limited != (i > tools.MaxWebSearchesPerWake) {
			t.Fatalf("call %d: out=%s err=%v", i, out, err)
		}
	}
}
