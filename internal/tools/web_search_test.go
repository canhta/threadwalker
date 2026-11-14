package tools_test

import (
	"context"
	"errors"
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

func TestWebSearchToolIsAgentOnlyAndRecordsSpend(t *testing.T) {
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
		LLM:       config.LLMConfig{APIKey: config.NewSecret("key"), PricePerMTokInUSD: 10, PricePerMTokOutUSD: 10},
		Limits:    config.LimitsConfig{PostsPerDay: 3, RepliesPerDay: 5, RepliesPerUser7d: 1, MinSecondsBetweenActions: 1, MaxStepsPerWake: 10, MaxUSDPerDay: 1, MinMinutesBetweenWakes: 1},
		WebSearch: config.WebSearchConfig{Enabled: true, Model: "search-model", BaseURL: "https://search.invalid", MaxOutput: 256, MaxResults: 5},
	}
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	wakeID, err := st.BeginWake(ctx, tmpl.ID, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	d, err := tools.NewDispatcher(tools.Options{Scope: guard.Scope{Store: st, Template: tmpl, WakeID: wakeID, Now: func() time.Time { return now }}, SearchWeb: search, FetchTrends: func(context.Context) ([]trends.Trend, error) { return nil, nil }})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, definition := range d.Definitions() {
		names[definition.Function.Name] = true
	}
	if !names["web_search"] {
		t.Fatal("agent dispatcher omits web_search")
	}
	out, err := d.Dispatch(ctx, llm.ToolCall{ID: "ws1", Type: "function", Function: llm.CallFunction{Name: "web_search", Arguments: `{"query":"Jensen Huang"}`}})
	if err != nil || !strings.Contains(out, "answer") || !strings.Contains(out, `"untrusted":true`) {
		t.Fatalf("out=%s err=%v", out, err)
	}
	usage, err := st.LLMUsageForDayPhases(ctx, tmpl.ID, "2026-01-01", "agent")
	if err != nil || usage.CostUSD <= 0 {
		t.Fatalf("web search spend=%+v err=%v", usage, err)
	}

	// The first call above counts: the tool is bounded per wake.
	for i := 2; i <= tools.MaxWebSearchesPerWake+1; i++ {
		out, err := d.Dispatch(ctx, llm.ToolCall{ID: fmt.Sprintf("a%d", i), Type: "function", Function: llm.CallFunction{Name: "web_search", Arguments: fmt.Sprintf(`{"query":"q%d"}`, i)}})
		if limited := err != nil && strings.Contains(err.Error(), "limit_per_wake"); limited != (i > tools.MaxWebSearchesPerWake) {
			t.Fatalf("call %d: out=%s err=%v", i, out, err)
		}
	}
}

// A search that failed after a paid turn still lands its cost in the ledger.
func TestFailedWebSearchStillRecordsItsSpend(t *testing.T) {
	search := func(context.Context, trends.WebSearchRequest) (trends.WebSearchResult, error) {
		return trends.WebSearchResult{Usage: store.LLMUsage{TokensIn: 1000, TokensOut: 0}}, errors.New("web search: empty response")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "websearch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	tmpl := config.Template{ID: "viral", DryRun: true,
		LLM:       config.LLMConfig{APIKey: config.NewSecret("key"), PricePerMTokInUSD: 10, PricePerMTokOutUSD: 10},
		Limits:    config.LimitsConfig{PostsPerDay: 3, RepliesPerDay: 5, RepliesPerUser7d: 1, MinSecondsBetweenActions: 1, MaxStepsPerWake: 10, MaxUSDPerDay: 1, MinMinutesBetweenWakes: 1},
		WebSearch: config.WebSearchConfig{Enabled: true, Model: "search-model", BaseURL: "https://search.invalid", MaxOutput: 256, MaxResults: 5},
	}
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	wakeID, err := st.BeginWake(ctx, tmpl.ID, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	d, err := tools.NewDispatcher(tools.Options{Scope: guard.Scope{Store: st, Template: tmpl, WakeID: wakeID, Now: func() time.Time { return now }}, SearchWeb: search, FetchTrends: func(context.Context) ([]trends.Trend, error) { return nil, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Dispatch(ctx, llm.ToolCall{ID: "ws1", Type: "function", Function: llm.CallFunction{Name: "web_search", Arguments: `{"query":"q"}`}}); err == nil {
		t.Fatal("a failed search returned no error")
	}
	usage, err := st.LLMUsageForDay(ctx, tmpl.ID, "2026-01-01")
	if err != nil || usage.CostUSD != 0.01 {
		t.Fatalf("spend %+v, want $0.01 err=%v", usage, err)
	}
}
