package agent_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/trends"
)

func TestStudyPhaseSamplesThreadsAndBlocksWrites(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "study.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 1, 1, 8, 5, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	tmpl := agent.OfflineTemplate()
	tmpl.Schedule = config.ScheduleConfig{Timezone: "UTC", StudySlots: []string{"08:00"}}
	tmpl.Study.MaxThreadsReads = 2
	tmpl.Study.ThreadsKeywords = []string{"AI"}
	tmpl.Study.ReadPauseMS = 0
	nextWake := now.Add(3 * time.Hour)
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	if err = st.SetNextWake(ctx, tmpl.ID, nextWake); err != nil {
		t.Fatal(err)
	}
	fixture := agent.NewOfflineFixture(clock)
	fixture.Calls = []llm.ToolCall{
		agent.SyntheticCall("read_feed", `{}`),
		agent.SyntheticCall("fetch_trends", `{}`),
		agent.SyntheticCall("note_write", `{"topic":"trends","text":"hôm nay AI đang lên vì benchmark mới"}`),
		agent.SyntheticCall("post", `{"text":"a take that must be refused"}`),
	}
	fetchStudy := func(context.Context) (trends.StudyFeed, error) {
		return trends.StudyFeed{Items: []trends.Trend{
			{Source: "google_news_vn", Title: "Jensen Huang: GPU supply tight", URL: "https://news.invalid/1"},
			{Source: "the_verge", Title: "Nvidia CEO Jensen Huang on GPU supply", URL: "https://news.invalid/2"},
		}}, nil
	}
	runtime, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: clock, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends, FetchStudy: fetchStudy})
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	due, kind, key, ok, err := runtime.PhaseDue(ctx, now)
	if err != nil || !ok || kind != store.WakeStudy || due.After(now) {
		t.Fatalf("study due=%v kind=%s key=%s ok=%v err=%v", due, kind, key, ok, err)
	}
	wake, err := runtime.RunPhase(ctx, kind, key)
	if err != nil || wake.EndReason != "completed" {
		t.Fatalf("study wake=%+v err=%v", wake, err)
	}
	var kindStored string
	if err = st.DB().QueryRow(`SELECT kind FROM wakes WHERE id=?`, wake.ID).Scan(&kindStored); err != nil || kindStored != "study" {
		t.Fatalf("wake kind=%q err=%v", kindStored, err)
	}
	var observations int
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM threads_observations WHERE agent_id=?`, tmpl.ID).Scan(&observations); err != nil || observations < 2 {
		t.Fatalf("observations=%d err=%v", observations, err)
	}
	var stories int
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM trend_stories WHERE agent_id=? AND phase='study'`, tmpl.ID).Scan(&stories); err != nil || stories == 0 {
		t.Fatalf("stories=%d err=%v", stories, err)
	}
	var blocked int
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE wake_id=? AND blocked_by<>''`, wake.ID).Scan(&blocked); err != nil || blocked != 1 {
		t.Fatalf("calls to tools the study phase does not offer=%d err=%v", blocked, err)
	}
	var actions int
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM actions WHERE agent_id=?`, tmpl.ID).Scan(&actions); err != nil || actions != 0 {
		t.Fatalf("actions=%d err=%v", actions, err)
	}
	state, err := st.AgentState(ctx, tmpl.ID)
	if err != nil || !state.NextWake.Equal(nextWake) {
		t.Fatalf("study moved next wake: %v err=%v", state.NextWake, err)
	}
	usage, err := st.LLMUsageForDayPhases(ctx, tmpl.ID, now.Format("2006-01-02"), "study")
	if err != nil || usage.CostUSD <= 0 {
		t.Fatalf("study spend=%+v err=%v", usage, err)
	}
	// The next normal wake sees the study trend brief and stories.
	messages, err := runtime.Context(ctx, "agent")
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, message := range messages {
		joined += message.Content
	}
	if !strings.Contains(joined, "threads_trend_brief") || !strings.Contains(joined, "Jensen Huang") {
		t.Fatalf("normal context missing study trends")
	}
}

func TestDreamSpendIsSeparateFromDailyCap(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "dream-cap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 1, 1, 23, 45, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	tmpl := agent.OfflineTemplate()
	tmpl.Schedule = config.ScheduleConfig{Timezone: "UTC", DreamAt: "23:30"}
	tmpl.Limits.MaxUSDPerDream = 0.000001
	tmpl.Limits.MaxStepsPerDream = 10
	nextWake := now.Add(2 * time.Hour)
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	if err = st.SetNextWake(ctx, tmpl.ID, nextWake); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec(`INSERT INTO wakes(agent_id,started_at,ended_at,end_reason,kind,phase_key) VALUES(?,?,'completed','interrupted','dream','2025-12-31')`, tmpl.ID, "2025-12-31T23:30:00Z"); err != nil {
		t.Fatal(err)
	}
	fixture := agent.NewOfflineFixture(clock)
	fixture.Calls = []llm.ToolCall{
		agent.SyntheticCall("read_feed", `{}`),
		agent.SyntheticCall("note_write", `{"topic":"dream","text":"consolidated dream note"}`),
		agent.SyntheticCall("post", `{"text":"dream must not post"}`),
	}
	runtime, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: clock, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends, FetchStudy: fixture.Study})
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	due, kind, key, ok, err := runtime.PhaseDue(ctx, now)
	if err != nil || !ok || kind != store.WakeDream || due.After(now) {
		t.Fatalf("dream due=%v kind=%s key=%s ok=%v err=%v", due, kind, key, ok, err)
	}
	wake, err := runtime.RunPhase(ctx, kind, key)
	if err != nil || wake.EndReason != "budget" {
		t.Fatalf("dream wake=%+v err=%v", wake, err)
	}
	daily, err := st.LLMUsageForDayPhases(ctx, tmpl.ID, now.Format("2006-01-02"), "agent", "study")
	if err != nil || daily.CostUSD != 0 {
		t.Fatalf("dream hit the daily cap: %+v err=%v", daily, err)
	}
	dream, err := st.LLMUsageForDayPhases(ctx, tmpl.ID, now.Format("2006-01-02"), "dream")
	if err != nil || dream.CostUSD <= 0 {
		t.Fatalf("dream spend=%+v err=%v", dream, err)
	}
	state, err := st.AgentState(ctx, tmpl.ID)
	if err != nil || !state.NextWake.Equal(nextWake) {
		t.Fatalf("dream moved next wake: %v err=%v", state.NextWake, err)
	}
	var actions int
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM actions WHERE agent_id=?`, tmpl.ID).Scan(&actions); err != nil || actions != 0 {
		t.Fatalf("dream actions=%d err=%v", actions, err)
	}
}
