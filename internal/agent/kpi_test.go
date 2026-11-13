package agent_test

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/store"
)

var kpiNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// kpiRuntime is an offline runtime with a kpi block whose store holds one own
// action posted publishedAgo ago, with engagements likes, $0.12 spent today and 4 wakes today.
func kpiRuntime(t *testing.T, publishedAgo time.Duration, engagements int) (*agent.Runtime, *store.Store, *agent.OfflineFixture) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "kpi.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tmpl := agent.OfflineTemplate()
	tmpl.KPI = config.KPIConfig{EngagementsPerDay: 10, FollowersPerWeek: 5, MaxHoursWithoutPublish: 8, OnTargetMinutes: 180}
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	id, err := st.SeedAction(ctx, store.SeededAction{AgentID: tmpl.ID, Kind: "post", Text: "seed", DryRun: true, PostedAt: kpiNow.Add(-publishedAgo)})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO metrics(agent_id,action_id,measured_at,likes) VALUES(?1,` + itoa(id) + `,'2026-01-01T11:00:00Z',` + itoa(int64(engagements)) + `)`,
		`INSERT INTO ledger_phases(agent_id,day,phase,tokens_in,tokens_out,cost_usd) VALUES(?1,'2026-01-01','agent',1000,500,0.12)`,
		`INSERT INTO wakes(agent_id,started_at,ended_at) VALUES(?1,'2026-01-01T01:00:00Z','2026-01-01T01:01:00Z'),(?1,'2026-01-01T03:00:00Z','2026-01-01T03:01:00Z'),(?1,'2026-01-01T06:00:00Z','2026-01-01T06:01:00Z'),(?1,'2026-01-01T09:00:00Z','2026-01-01T09:01:00Z')`,
	} {
		if _, err = st.DB().Exec(q, tmpl.ID); err != nil {
			t.Fatal(err)
		}
	}
	fixture := agent.NewOfflineFixture(func() time.Time { return kpiNow })
	r, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: func() time.Time { return kpiNow }, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends, FetchStudy: fixture.Study})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return r, st, fixture
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestScoreboardTopsTheWakeAndStatesTheGap(t *testing.T) {
	r, _, _ := kpiRuntime(t, 9*time.Hour, 3)
	messages, err := r.Context(context.Background(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	user := messages[1].Content
	for _, want := range []string{
		"# Scoreboard (computed by code)\n",
		"Spend: today $0.12 (1500 tokens), last 7 days $0.12 (1500 tokens). Wakes today: 4.",
		"Last published action: 9h ago (most you may go: 8h).",
		"Engagements (likes, replies, reposts, quotes on your actions) in 24h: 3 of 10. Followers gained in 7 days: 0 of 5.",
		"Cost per engagement: $0.040",
		"behind: 3 of 10 engagements in 24h after $0.12 and 4 wakes today; nothing published for 9h\n",
		"never lower quality. The critic still decides and the caps still hold.",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("scoreboard lacks %q in:\n%.900s", want, user)
		}
	}
	if !strings.HasPrefix(user, "# Scoreboard") || !strings.Contains(user, "\n\n{") {
		t.Fatal("scoreboard must come first and the state JSON after it")
	}
}

func TestScoreboardOnTargetHasNoStopRule(t *testing.T) {
	r, _, _ := kpiRuntime(t, time.Hour, 12)
	messages, err := r.Context(context.Background(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	user := messages[1].Content
	if !strings.Contains(user, "on target: 12 of 10 engagements in 24h; last published 1h ago") || strings.Contains(user, "may not end this wake idle") {
		t.Fatalf("on-target scoreboard:\n%.900s", user)
	}
}

func TestBehindAgentGetsBoundedExtraTurnsWhenItStopsIdle(t *testing.T) {
	r, _, fixture := kpiRuntime(t, 9*time.Hour, 3)
	fixture.Calls = []llm.ToolCall{} // the model stops at once
	nudges := 0
	fixture.Observe = func(messages []llm.Message) {
		nudges = 0
		for _, m := range messages[2:] {
			if m.Role == "user" && strings.Contains(m.Content, "Before you stop: try at least one post or reply, then write a one-line note_write") {
				nudges++
			}
		}
	}
	wake, err := r.Wake(context.Background())
	if err != nil || wake.EndReason != "completed" || nudges != llm.MaxStopNudges {
		t.Fatalf("wake %+v err %v nudges %d", wake, err, nudges)
	}
}

func TestOnTargetAgentMayStopIdle(t *testing.T) {
	r, _, fixture := kpiRuntime(t, time.Hour, 12)
	fixture.Calls = []llm.ToolCall{}
	extra := false
	fixture.Observe = func(messages []llm.Message) {
		for _, m := range messages[2:] {
			extra = extra || m.Role == "user" && strings.Contains(m.Content, "may not stop idle")
		}
	}
	if _, err := r.Wake(context.Background()); err != nil || extra {
		t.Fatalf("err %v extra turn %v", err, extra)
	}
}

func TestBehindAgentThatPublishedIsNotNudged(t *testing.T) {
	r, _, fixture := kpiRuntime(t, 9*time.Hour, 3)
	extra := false
	fixture.Observe = func(messages []llm.Message) {
		for _, m := range messages[2:] {
			extra = extra || m.Role == "user" && strings.Contains(m.Content, "may not stop idle")
		}
	}
	if _, err := r.Wake(context.Background()); err != nil || extra { // the default wake posts
		t.Fatalf("err %v extra turn %v", err, extra)
	}
}

func TestNextWakeComesFromTheGap(t *testing.T) {
	for _, tc := range []struct {
		name         string
		publishedAgo time.Duration
		engagements  int
		after        time.Duration
	}{
		{"behind waits the template minimum", 9 * time.Hour, 3, 20 * time.Minute},
		{"on target waits the longer interval", time.Hour, 12, 180 * time.Minute},
		{"engagements met but silent for too long is behind", 9 * time.Hour, 12, 20 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, st, fixture := kpiRuntime(t, tc.publishedAgo, tc.engagements)
			fixture.Calls = []llm.ToolCall{llm.ToolCall{Type: "function", Function: llm.CallFunction{Name: "read_feed", Arguments: `{}`}}}
			// The wake publishes nothing, so the publish gap stays what the seed made it.
			if _, err := r.Wake(context.Background()); err != nil {
				t.Fatal(err)
			}
			state, err := st.AgentState(context.Background(), "synthetic-offline")
			if err != nil || !state.NextWake.Equal(kpiNow.Add(tc.after)) {
				t.Fatalf("next wake %v, want %v (err %v)", state.NextWake, kpiNow.Add(tc.after), err)
			}
		})
	}
}

func TestCappedDayWaitsForTheNextUTCDay(t *testing.T) {
	tomorrow := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, st *store.Store)
		want  time.Time
	}{
		{"nothing capped: behind still wakes at the minimum", func(*testing.T, *store.Store) {}, kpiNow.Add(20 * time.Minute)},
		{"daily USD spent", func(t *testing.T, st *store.Store) {
			if _, err := st.DB().Exec(`INSERT INTO ledger_phases(agent_id,day,phase,cost_usd) VALUES('synthetic-offline','2026-01-01','study',0.9)`); err != nil {
				t.Fatal(err)
			}
		}, tomorrow},
		{"posts and replies both used", func(t *testing.T, st *store.Store) {
			for i := 0; i < 15; i++ {
				seedToday(t, st, "reply", i)
			}
			for i := 0; i < 2; i++ {
				seedToday(t, st, "post", 100+i)
			}
		}, tomorrow},
		{"only the post cap used: replies still possible", func(t *testing.T, st *store.Store) {
			for i := 0; i < 3; i++ {
				seedToday(t, st, "post", i)
			}
		}, kpiNow.Add(20 * time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, st, fixture := kpiRuntime(t, 9*time.Hour, 3) // one seeded post at 03:00 counts toward posts_per_day 3
			fixture.Calls = []llm.ToolCall{agent.SyntheticCall("read_feed", `{}`)}
			tc.setup(t, st)
			if _, err := r.Wake(context.Background()); err != nil {
				t.Fatal(err)
			}
			state, err := st.AgentState(context.Background(), "synthetic-offline")
			if err != nil || !state.NextWake.Equal(tc.want) {
				t.Fatalf("next wake %v, want %v (err %v)", state.NextWake, tc.want, err)
			}
		})
	}
}

func seedToday(t *testing.T, st *store.Store, kind string, n int) {
	t.Helper()
	if _, err := st.SeedAction(context.Background(), store.SeededAction{AgentID: "synthetic-offline", Kind: kind, Text: kind + strconv.Itoa(n), DryRun: true, PostedAt: kpiNow.Add(-time.Duration(n+1) * time.Minute)}); err != nil {
		t.Fatal(err)
	}
}
