package agent_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/store"
)

const judgeScores = `{"hook_strength":4,"specificity":3,"preachiness":5,"slop_free":2,"persona_fit":4,"target_fit":3}`

// seedPair stores one viral and one flop action of the same context, with outcomes.
func seedPair(t *testing.T, st *store.Store, agentID string, now time.Time) {
	t.Helper()
	for i, label := range []string{store.LabelViral, store.LabelFlop} {
		posted := now.Add(-time.Duration(72-24*i) * time.Hour).UTC().Format(time.RFC3339Nano)
		at := now.Add(-time.Duration(48-24*i) * time.Hour).UTC().Format(time.RFC3339Nano)
		res, err := st.DB().Exec(`INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url,media_id) VALUES(?,?,?,0,?,'https://fixture.invalid/p','m')`, agentID, "post", "post "+label, posted)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		for _, q := range []string{
			`INSERT INTO metrics(agent_id,action_id,measured_at,views,likes,replies,reposts) VALUES(?,?,?,500,5,5,0)`,
			`INSERT INTO metric_jobs(agent_id,action_id,window_hours,due_at,not_before,completed_at) VALUES(?,?,24,?,?,?)`,
		} {
			args := []any{agentID, id, at}
			if strings.Contains(q, "metric_jobs") {
				args = append(args, at, at)
			}
			if _, err = st.DB().Exec(q, args...); err != nil {
				t.Fatal(err)
			}
		}
		lift := 1.0
		if label == store.LabelFlop {
			lift = -1
		}
		if err = st.InsertOutcome(context.Background(), agentID, store.Outcome{ActionID: id, MeasuredAt: now.Add(-time.Duration(48-24*i) * time.Hour), Views: 500, ReachLift: lift, ResonanceLift: lift, Label: label}); err != nil {
			t.Fatal(err)
		}
	}
}

// dreamContext runs a dream on a store holding one pair and returns the post_mortem
// block of its context and the number of judge requests the transport saw.
func dreamContext(t *testing.T, review bool, dreamCap float64) (block map[string]any, judged int) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "pm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 3, 10, 23, 45, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	tmpl := agent.OfflineTemplate()
	tmpl.Schedule = config.ScheduleConfig{Timezone: "UTC", DreamAt: "23:30"}
	tmpl.Limits.MaxUSDPerDream, tmpl.Limits.MaxStepsPerDream = dreamCap, 10
	if review {
		tmpl.Review = config.LLMReview{Enabled: true, LLM: tmpl.LLM}
		tmpl.Review.LLM.Model = "synthetic-judge"
	}
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	if err = st.SetNextWake(ctx, tmpl.ID, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	seedPair(t, st, tmpl.ID, now)
	fixture := agent.NewOfflineFixture(clock)
	fixture.Calls = []llm.ToolCall{}
	fixture.Reply = judgeScores
	fixture.Observe = func(m []llm.Message) {
		if strings.Contains(m[0].Content, "reviewing a published") {
			judged++
		}
	}
	runtime, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: clock, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends, FetchStudy: fixture.Study})
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	wake, err := runtime.RunPhase(ctx, store.WakeDream, "2026-03-10")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := st.Transcript(ctx, tmpl.ID, wake.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Kind != store.TranscriptContext {
			continue
		}
		var body struct{ Data []string }
		if json.Unmarshal([]byte(e.Body), &body) != nil || len(body.Data) == 0 {
			t.Fatalf("context body: %.200s", e.Body)
		}
		var user struct {
			PostMortem map[string]any `json:"post_mortem"`
		}
		if err = json.Unmarshal([]byte(body.Data[0]), &user); err != nil {
			t.Fatal(err)
		}
		return user.PostMortem, judged
	}
	t.Fatal("no context in transcript")
	return nil, 0
}

func pairsOf(t *testing.T, block map[string]any) []map[string]any {
	t.Helper()
	raw, _ := block["pairs"].([]any)
	var out []map[string]any
	for _, p := range raw {
		out = append(out, p.(map[string]any))
	}
	return out
}

func TestDreamContextCarriesMatchedPairsWithJudgeScores(t *testing.T) {
	block, judged := dreamContext(t, true, 0.3)
	pairs := pairsOf(t, block)
	if len(pairs) != 1 || judged != 2 {
		t.Fatalf("pairs=%v judged=%d", block, judged)
	}
	p := pairs[0]
	if _, ok := block["judge_skipped"]; ok {
		t.Errorf("judge skipped: %v", block["judge_skipped"])
	}
	if p["exact"] != true || p["attribution"].(map[string]any)["explained_by"] == "" {
		t.Errorf("pair = %v", p)
	}
	for _, side := range []string{"viral", "flop"} {
		scores, _ := p[side].(map[string]any)["scores"].(map[string]any)
		if scores["hook_strength"] != float64(4) || scores["slop_free"] != float64(2) {
			t.Errorf("%s scores = %v", side, scores)
		}
	}
}

func TestPostMortemWithoutReviewConfigMakesNoJudgeRequest(t *testing.T) {
	block, judged := dreamContext(t, false, 0.3)
	if judged != 0 || block["judge_skipped"] != "not_configured" || len(pairsOf(t, block)) != 1 {
		t.Fatalf("judged=%d block=%v", judged, block)
	}
	if _, ok := pairsOf(t, block)[0]["viral"].(map[string]any)["scores"]; ok {
		t.Error("scores without a judge")
	}
}

func TestPostMortemJudgeStopsAtTheDreamCapAndKeepsThePairs(t *testing.T) {
	block, judged := dreamContext(t, true, 0.000001)
	if judged != 1 || block["judge_skipped"] != "dream_cap" || len(pairsOf(t, block)) != 1 {
		t.Fatalf("judged=%d block=%v", judged, block)
	}
	if _, ok := pairsOf(t, block)[0]["flop"].(map[string]any)["scores"]; ok {
		t.Error("flop scored after the cap")
	}
}
