package agent_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/store"
)

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
	if _, err = st.DB().Exec(`INSERT INTO wakes(agent_id,started_at,ended_at,end_reason,kind,phase_key) VALUES(?,?,'2025-12-31T23:40:00Z','interrupted','dream','2025-12-31')`, tmpl.ID, "2025-12-31T23:30:00Z"); err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i < 5; i++ {
		seedEpisode(t, st, tmpl.ID, "post", "episode", now.Add(-time.Duration(12+i)*time.Hour), 6, 10, i, 0)
	}
	fixture := agent.NewOfflineFixture(clock)
	fixture.Calls = []llm.ToolCall{
		agent.SyntheticCall("read_feed", `{}`),
		agent.SyntheticCall("fetch_trends", `{}`),
		agent.SyntheticCall("post", `{"text":"dream must not post"}`),
	}
	runtime, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: clock, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends})
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
	daily, err := st.LLMUsageForDayPhases(ctx, tmpl.ID, now.Format("2006-01-02"), "agent")
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
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM actions WHERE agent_id=?`, tmpl.ID).Scan(&actions); err != nil || actions != 5 {
		t.Fatalf("dream actions=%d err=%v", actions, err)
	}
}
