package tools_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/guard"
	"github.com/canhta/threadwalker/internal/llm"
	"github.com/canhta/threadwalker/internal/store"
	"github.com/canhta/threadwalker/internal/tools"
)

func webSearchServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("unexpected path/auth: %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var request struct {
			Model     string                  `json:"model"`
			Tools     []struct{ Type string } `json:"tools"`
			MaxOutput int                     `json:"max_output_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "search-model" || len(request.Tools) != 1 || request.Tools[0].Type != "web_search" || request.MaxOutput != 256 {
			t.Errorf("request=%+v", request)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSearchWebParsesBoundedAnswerSourcesAndUsage(t *testing.T) {
	server := webSearchServer(t, http.StatusOK, `{
		"output":[{"type":"message","content":[{"type":"output_text","text":"Jensen Huang is in the news.","annotations":[
			{"type":"url_citation","url":"https://a.invalid/1","title":"A"},
			{"type":"url_citation","url":"https://a.invalid/1","title":"duplicate"},
			{"type":"url_citation","url":"https://b.invalid/2","title":"B"},
			{"type":"url_citation","url":"https://c.invalid/3","title":"C"}]}]}],
		"usage":{"input_tokens":120,"output_tokens":40}
	}`)
	result, err := tools.SearchWeb(context.Background(), tools.WebSearchRequest{HTTPClient: server.Client(), BaseURL: server.URL, Model: "search-model", APIKey: "key", Query: "Jensen Huang news", MaxOutput: 256, MaxResults: 3, Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if result.Answer != "Jensen Huang is in the news." || len(result.Sources) != 3 {
		t.Fatalf("result=%+v", result)
	}
	if result.Usage.TokensIn != 120 || result.Usage.TokensOut != 40 {
		t.Fatalf("usage=%+v", result.Usage)
	}
}

func TestSearchWebFailsClosed(t *testing.T) {
	server := webSearchServer(t, http.StatusInternalServerError, `{"error":"nope"}`)
	if _, err := tools.SearchWeb(context.Background(), tools.WebSearchRequest{HTTPClient: server.Client(), BaseURL: server.URL, Model: "search-model", APIKey: "key", Query: "q", MaxOutput: 256, Timeout: time.Second}); err == nil {
		t.Fatal("HTTP 500 accepted")
	}
	missingUsage := webSearchServer(t, http.StatusOK, `{"output":[{"content":[{"type":"output_text","text":"answer"}]}]}`)
	if _, err := tools.SearchWeb(context.Background(), tools.WebSearchRequest{HTTPClient: missingUsage.Client(), BaseURL: missingUsage.URL, Model: "search-model", APIKey: "key", Query: "q", MaxOutput: 256, Timeout: time.Second}); err == nil {
		t.Fatal("missing usage accepted")
	}
	empty := webSearchServer(t, http.StatusOK, `{"output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
	if _, err := tools.SearchWeb(context.Background(), tools.WebSearchRequest{HTTPClient: empty.Client(), BaseURL: empty.URL, Model: "search-model", APIKey: "key", Query: "q", MaxOutput: 256, Timeout: time.Second}); err == nil {
		t.Fatal("empty answer accepted")
	}
}

func TestWebSearchToolIsStudyOnlyAndRecordsStudySpend(t *testing.T) {
	server := webSearchServer(t, http.StatusOK, `{"output":[{"content":[{"type":"output_text","text":"answer","annotations":[{"type":"url_citation","url":"https://a.invalid/1","title":"A"}]}]}],"usage":{"input_tokens":1000,"output_tokens":1000}}`)
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
		Study:  config.StudyConfig{WebSearch: true, WebSearchModel: "search-model", WebSearchBaseURL: server.URL, WebSearchMaxOutput: 256, WebSearchMaxResults: 5},
	}
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	wakeID, err := st.BeginPhaseWake(ctx, tmpl.ID, store.WakeStudy, "2026-01-01T09:00", now)
	if err != nil {
		t.Fatal(err)
	}
	g, err := guard.New(guard.Options{Store: st, Template: tmpl, WakeID: wakeID, Phase: guard.PhaseStudy, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	d, err := tools.NewDispatcher(g, tools.Options{Store: st, Template: tmpl, WakeID: wakeID, Phase: guard.PhaseStudy, Now: func() time.Time { return now }, WebSearchClient: server.Client()})
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

	// A normal wake does not offer the tool.
	if err = st.FinishWake(ctx, wakeID, now.Add(time.Minute), 3, "completed"); err != nil {
		t.Fatal(err)
	}
	agentWake, err := st.BeginWake(ctx, tmpl.ID, now.Add(time.Minute), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	agentGuard, err := guard.New(guard.Options{Store: st, Template: tmpl, WakeID: agentWake, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	agentDispatcher, err := tools.NewDispatcher(agentGuard, tools.Options{Store: st, Template: tmpl, WakeID: agentWake, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range agentDispatcher.Definitions() {
		if definition.Function.Name == "web_search" {
			t.Fatal("normal wake offers web_search")
		}
	}
}
