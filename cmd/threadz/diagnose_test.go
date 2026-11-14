package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/eval"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/quality"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/tools"
	"github.com/canhta/threadz/internal/trends"
)

const diagnoseNews = "https://news.google.com/rss/articles/CBMiSyntheticOpaqueIdentifierForDiagnoseFixtureAAAAAAAAAAAAAAAAAAAAAAAAAA?oc=5"

type seededWake struct {
	db   string
	wake int64
}

// seedWake runs real dispatcher calls on a file database and stores the
// transcript the LLM client would have: the calls the model made and each result.
func seedWake(t *testing.T, writer bool) seededWake {
	t.Helper()
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "d.db")
	st, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	tmpl := agent.OfflineTemplate()
	tmpl.ID = "diag"
	tmpl.Guard.TrendAnchor = true
	tmpl.Limits.PostsPerDay = 10
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	wake, err := st.BeginWake(ctx, tmpl.ID, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	scope := guard.Scope{Store: st, Template: tmpl, WakeID: wake, Now: func() time.Time { return now }}
	if writer {
		verdicts := []quality.CriticVerdict{{Problems: []string{"the first line does not name the story"}}}
		texts := []string{"Giá model giảm, hóa đơn dev thì không.", "Giá model giảm nửa: hóa đơn dev có giảm không?"}
		scope.Writer = func(context.Context, quality.WriterInput) ([]string, error) {
			out := texts[min(len(texts)-1, 0)]
			if len(texts) > 1 {
				texts = texts[1:]
			}
			return []string{out}, nil
		}
		scope.Critic = func(context.Context, quality.CriticInput) (quality.CriticVerdict, error) {
			if len(verdicts) == 0 {
				return quality.CriticVerdict{Pass: true, Scores: quality.CriticScores{Hook: 4, NaturalVietnamese: 4, Specificity: 4, ForcedSelfReference: 5, TargetFit: 4, Novelty: 4, Clarity: 5}}, nil
			}
			v := verdicts[0]
			verdicts = verdicts[1:]
			return v, nil
		}
	}
	mem := threads.NewMemory()
	mem.Pages[threads.OpReadFeed] = threads.Page{Kind: "feed", Posts: []threads.Post{{Username: "synthetic_author", URL: "https://www.threads.com/@synthetic_author/post/FixtureOne", Text: "a stranger's words"}}}
	d, err := tools.NewDispatcher(tools.Options{Scope: scope, Threads: mem,
		FetchTrends: func(context.Context) ([]trends.Trend, error) {
			return []trends.Trend{{Source: "google_trends_vn", Title: "Synthetic headline about a price cut", URL: diagnoseNews}}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	anchor := `"hook":"trend_detail","rationale":"anchors to the price cut, devs reply with their bills"`
	for i, c := range []struct{ name, args string }{
		{"fetch_trends", `{}`},
		{"find_posts", `{"source":"feed"}`},
		{"post", `{"text":"Giá model giảm nửa, hóa đơn dev đi đâu?","trend":"t9","angle":"price cut","fact":"synthetic",` + anchor + `}`},
		{"post", `{"text":"Giá model giảm nửa, hóa đơn dev đi đâu?","trend":"t1","angle":"price cut","fact":"synthetic",` + anchor + `}`},
		{"post", `{"text":"Giá model giảm nửa, hóa đơn dev đi đâu?","trend":"t1","angle":"price cut","fact":"synthetic",` + anchor + `}`},
	} {
		now = now.Add(time.Minute)
		call := llm.ToolCall{ID: "c" + string(rune('1'+i)), Type: "function", Function: llm.CallFunction{Name: c.name, Arguments: c.args}}
		output, _ := d.Dispatch(ctx, call)
		assistant, _ := json.Marshal(map[string]any{"request": i + 1, "tool_calls": []llm.ToolCall{call}})
		result, _ := json.Marshal(map[string]any{"request": i + 1, "tool": c.name, "tool_call_id": call.ID, "output": output})
		if err = st.AppendTranscript(ctx, tmpl.ID, wake, store.TranscriptAssistant, string(assistant), now); err != nil {
			t.Fatal(err)
		}
		if err = st.AppendTranscript(ctx, tmpl.ID, wake, store.TranscriptToolResult, string(result), now); err != nil {
			t.Fatal(err)
		}
	}
	return seededWake{db, wake}
}

func runDiagnose(t *testing.T, w seededWake, extra ...string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	args := append([]string{"wake-diagnose", "1", "--agent", "diag", "--db", w.db}, extra...)
	if code := runCtl(context.Background(), args, func(string) string { return "" }, &out, &errOut, defaultCtlDeps()); code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	return out.String()
}

func TestWakeDiagnoseShowsTheFunnel(t *testing.T) {
	out := runDiagnose(t, seedWake(t, false))
	t.Log("\n" + out)
	for _, want := range []string{
		"funnel: 5 calls; 2 reads (0 failed); 3 writes (2 blocked, 0 failed); 0 published",
		"fetch_trends  ok",
		"got    items 1",
		"find_posts  ok",
		"got    posts 1",
		"post  blocked no_trend_anchor",
		`text="Giá model giảm nửa, hóa đơn dev đi đâu?"`,
		"-> no noted item (",
		"post  ok",
		`-> "` + diagnoseNews,
		"action #1 post dry run",
		"post  blocked repeat_topic",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("diagnose output lacks %q", want)
		}
	}
}

func TestWakeDiagnoseShowsEveryDraftAndItsProblems(t *testing.T) {
	out := runDiagnose(t, seedWake(t, true))
	t.Log("\n" + out)
	for _, want := range []string{
		"draft 1 [agent] [39] runes",
		"draft 2 [writer]",
		"problem: the first line does not name the story",
		"draft 3 [writer]",
		"pass=true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("diagnose output lacks %q", want)
		}
	}
}

func TestWakeDiagnoseJSON(t *testing.T) {
	var d diagnosis
	if err := json.Unmarshal([]byte(runDiagnose(t, seedWake(t, false), "--json")), &d); err != nil {
		t.Fatal(err)
	}
	if d.Funnel.Writes != 3 || d.Funnel.Blocked != 2 || d.Calls[2].Ref == nil || d.Calls[2].Args["trend"] == nil {
		t.Fatalf("%+v", d.Funnel)
	}
}

func TestWakeExportRoundTripsThroughTheReplayKind(t *testing.T) {
	w := seedWake(t, false)
	var out, errOut bytes.Buffer
	args := []string{"wake-export", "1", "--agent", "diag", "--db", w.db}
	if code := runCtl(context.Background(), args, func(string) string { return "" }, &out, &errOut, defaultCtlDeps()); code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if strings.Contains(out.String(), "a stranger's words") {
		t.Fatal("the export kept the text of another user's post")
	}
	path := filepath.Join(t.TempDir(), "exported.json")
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	suite, err := eval.LoadSuite(path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := eval.Run(context.Background(), suite, eval.Options{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range report.Results {
		if !r.Passed {
			t.Fatalf("%s failed: %s", r.CaseID, r.Reason)
		}
	}
	if len(report.Results) == 0 {
		t.Fatal("nothing replayed")
	}
}

func TestWakeExportBriefsCarriesTheWriterExchange(t *testing.T) {
	w := seedWake(t, true)
	export := func(extra ...string) string {
		var out, errOut bytes.Buffer
		args := append([]string{"wake-export", "1", "--agent", "diag", "--db", w.db, "--briefs", "--agents", t.TempDir()}, extra...)
		if code := runCtl(context.Background(), args, func(string) string { return "" }, &out, &errOut, defaultCtlDeps()); code != exitOK {
			t.Fatalf("exit %d: %s", code, errOut.String())
		}
		return out.String()
	}
	out := export()
	if strings.Contains(out, "Synthetic headline") {
		t.Fatal("the export kept the text answered without --keep-text")
	}
	path := filepath.Join(t.TempDir(), "live-briefs.json")
	if err := os.WriteFile(path, []byte(export("--keep-text")), 0o600); err != nil {
		t.Fatal(err)
	}
	suite, err := eval.LoadSuite(path)
	if err != nil {
		t.Fatal(err)
	}
	b := suite.Cases[0].Briefs
	if suite.Cases[0].Kind != "briefs" || b.TemplateID != "diag" || len(b.Briefs) != 1 {
		t.Fatalf("%+v", suite.Cases[0])
	}
	got := b.Briefs[0]
	if got.Wake != 1 || got.Kind != "post" || got.Angle != "price cut" || got.Fact != "synthetic" || got.Hook != "trend_detail" || !strings.Contains(got.Parent, "Synthetic headline") || len(got.Recorded) != 1 || got.Recorded[0] != "Giá model giảm nửa: hóa đơn dev có giảm không?" {
		t.Fatalf("%+v", got)
	}
	if _, err = eval.Run(context.Background(), suite, eval.Options{Seed: 1}); err == nil {
		t.Fatal("an offline run called the live writer")
	}
}
