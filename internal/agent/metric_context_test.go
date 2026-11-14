package agent_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/store"
)

func TestContextContainsOnlyBoundedOwnConfirmedMetrics(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	tmpl := agent.OfflineTemplate()
	tmpl.Limits.MaxUSDPerDream = 1
	fixture := agent.NewOfflineFixture(func() time.Time { return now })
	r, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: func() time.Time { return now }, FetchTrends: fixture.Trends})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for _, a := range []struct {
		id    int
		agent string
		dry   int
		url   string
	}{{1, tmpl.ID, 0, "https://fixture.test/own"}, {2, "other", 0, "https://fixture.test/foreign"}, {3, tmpl.ID, 1, "https://fixture.test/dry"}, {4, tmpl.ID, 0, ""}} {
		if _, err = st.DB().Exec(`INSERT INTO actions(id,agent_id,kind,text,dry_run,posted_at,thread_url,media_id) VALUES(?,?,'post','PRIVATE BODY',?,?,?,'m')`, a.id, a.agent, a.dry, now.Add(-24*time.Hour).Format(time.RFC3339Nano), a.url); err != nil {
			t.Fatal(err)
		}
		if _, err = st.DB().Exec(`INSERT INTO metrics(agent_id,action_id,measured_at,views,likes,replies,reposts) VALUES(?,?,?,42,3,2,1)`, a.agent, a.id, now.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	for _, phase := range []string{"agent", "dream"} {
		messages, err := r.Context(ctx, phase)
		if err != nil {
			t.Fatal(err)
		}
		var data struct {
			Metrics []struct {
				ActionID   int64     `json:"action_id"`
				Views      int64     `json:"views"`
				MeasuredAt time.Time `json:"measured_at"`
			} `json:"metrics"`
		}
		if err = json.Unmarshal([]byte(messages[1].Content), &data); err != nil {
			t.Fatal(err)
		}
		if len(data.Metrics) != 1 || data.Metrics[0].ActionID != 1 || data.Metrics[0].Views != 42 || !data.Metrics[0].MeasuredAt.Equal(now) {
			t.Fatalf("%s metrics=%+v", phase, data.Metrics)
		}
	}
}

func TestSchedulerPollsMetricsBeforeAgentChosen24HourWake(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start
	clock := func() time.Time { return now }
	tmpl := agent.OfflineTemplate()
	fixture := agent.NewOfflineFixture(clock)
	fixture.Calls = nil
	// Preexisting publication is synthetic setup, never a real post.
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	if err = st.SetNextWake(ctx, tmpl.ID, start.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec(`INSERT INTO actions(id,agent_id,kind,text,dry_run,posted_at,thread_url,media_id) VALUES(1,?,'post','',0,?,'https://fixture.test/own','m')`, tmpl.ID, start.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	// Missing source is deferred, and must still be scheduled independently.
	r, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: clock, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends})
	if err != nil {
		t.Fatal(err)
	}
	waited := time.Duration(0)
	err = (agent.Scheduler{Agents: []*agent.Runtime{r}, Now: clock, Wait: func(ctx context.Context, d time.Duration) error { now = now.Add(d); waited += d; return ctx.Err() }}).Run(ctx, true)
	if err != nil || waited != time.Hour {
		t.Fatalf("metrics missed 1h target: waited=%v err=%v", waited, err)
	}
	state, err := st.AgentState(ctx, tmpl.ID)
	if err != nil || !state.NextWake.Equal(start.Add(24*time.Hour)) {
		t.Fatalf("agent schedule changed %+v %v", state, err)
	}
	var maintenance, model int
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM wakes WHERE kind='metrics'`).Scan(&maintenance); err != nil {
		t.Fatal(err)
	}
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM wakes WHERE kind='agent'`).Scan(&model); err != nil || maintenance != 1 || model != 0 {
		t.Fatalf("maintenance=%d model=%d err=%v", maintenance, model, err)
	}
}
