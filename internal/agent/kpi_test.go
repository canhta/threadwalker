package agent_test

import (
	"context"
	"path/filepath"
	"regexp"
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
	return kpiRuntimeSpaced(t, publishedAgo, engagements, 0)
}

func kpiRuntimeSpaced(t *testing.T, publishedAgo time.Duration, engagements, minMinutesBetweenPosts int) (*agent.Runtime, *store.Store, *agent.OfflineFixture) {
	t.Helper()
	return kpiRuntimeFull(t, "", publishedAgo, engagements, minMinutesBetweenPosts)
}

// kpiRuntimeIn is kpiRuntime with the spend day counted in zone (empty is UTC).
func kpiRuntimeIn(t *testing.T, zone string, publishedAgo time.Duration, engagements int) (*agent.Runtime, *store.Store, *agent.OfflineFixture) {
	t.Helper()
	return kpiRuntimeFull(t, zone, publishedAgo, engagements, 0)
}

func kpiRuntimeFull(t *testing.T, zone string, publishedAgo time.Duration, engagements, minMinutesBetweenPosts int) (*agent.Runtime, *store.Store, *agent.OfflineFixture) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "kpi.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tmpl := agent.OfflineTemplate()
	tmpl.Limits.MinMinutesBetweenPosts = minMinutesBetweenPosts
	tmpl.Limits.Timezone = zone
	tmpl.KPI = config.KPIConfig{EngagementsPerDay: 10, FollowersPerWeek: 5, MaxHoursWithoutPublish: 8, OnTargetMinutes: 180}
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	id, err := st.SeedAction(ctx, store.SeededAction{AgentID: tmpl.ID, Kind: "post", Text: "seed", DryRun: true, PostedAt: kpiNow.Add(-publishedAgo)})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		// The account gained exactly the weekly follower target: 100 at the first read, 105 at the newest.
		`INSERT INTO metrics(agent_id,action_id,measured_at,likes,followers) VALUES(?1,` + itoa(id) + `,'2025-12-30T00:00:00Z',0,100)`,
		`INSERT INTO metrics(agent_id,action_id,measured_at,likes,followers) VALUES(?1,` + itoa(id) + `,'2026-01-01T11:00:00Z',` + itoa(int64(engagements)) + `,105)`,
		`INSERT INTO ledger_phases(agent_id,day,phase,tokens_in,tokens_out,cost_usd) VALUES(?1,'2026-01-01','agent',1000,500,0.12)`,
		`INSERT INTO wakes(agent_id,started_at,ended_at) VALUES(?1,'2026-01-01T01:00:00Z','2026-01-01T01:01:00Z'),(?1,'2026-01-01T03:00:00Z','2026-01-01T03:01:00Z'),(?1,'2026-01-01T06:00:00Z','2026-01-01T06:01:00Z'),(?1,'2026-01-01T09:00:00Z','2026-01-01T09:01:00Z')`,
	} {
		if _, err = st.DB().Exec(q, tmpl.ID); err != nil {
			t.Fatal(err)
		}
	}
	fixture := agent.NewOfflineFixture(func() time.Time { return kpiNow })
	r, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: func() time.Time { return kpiNow }, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return r, st, fixture
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestLimitsShowNextAllowedPost(t *testing.T) {
	const line = "; next post allowed at "
	// Posted 30 min ago, 120 min apart: allowed at 13:30 UTC = 20:30 in Asia/Ho_Chi_Minh, in 90 min.
	for _, c := range []struct {
		ago  time.Duration
		gap  int
		want string
	}{
		{30 * time.Minute, 120, line + "20:30 local (in 90 min).\n"},
		{30 * time.Minute, 0, ""},
		{5 * time.Hour, 120, ""},
	} {
		r, _, _ := kpiRuntimeFull(t, "Asia/Ho_Chi_Minh", c.ago, 3, c.gap)
		messages, err := r.Context(context.Background(), "agent")
		if err != nil {
			t.Fatal(err)
		}
		user := messages[1].Content
		if c.want == "" && strings.Contains(user, line) {
			t.Errorf("gap %d, posted %v ago: unexpected next-post line in:\n%.900s", c.gap, c.ago, user)
		}
		if c.want != "" && !strings.Contains(user, c.want) {
			t.Errorf("limits lack %q in:\n%.900s", c.want, user)
		}
	}
}

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
		"Engagements (likes, replies, reposts, quotes on your actions) in 24h: 3 of 10. Followers gained in 7 days: 5 of 5.",
		"Cost per engagement: $0.040",
		"behind: 3 of 10 engagements in 24h, 5 of 5 followers gained in 7 days, after $0.12 and 4 wakes today; nothing published for 9h\n",
		"never lower quality. The critic still decides and the caps still hold.",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("scoreboard lacks %q in:\n%.900s", want, user)
		}
	}
	if !strings.HasPrefix(user, "{") || !strings.Contains(user, "}\n\n# Scoreboard") {
		t.Fatal("the state JSON must come first and the scoreboard after it")
	}
}

func TestScoreboardOnTargetHasNoStopRule(t *testing.T) {
	r, _, _ := kpiRuntime(t, time.Hour, 12)
	messages, err := r.Context(context.Background(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	user := messages[1].Content
	if !strings.Contains(user, "on target: 12 of 10 engagements in 24h, 5 of 5 followers gained in 7 days; last published 1h ago") || strings.Contains(user, "may not end this wake idle") {
		t.Fatalf("on-target scoreboard:\n%.900s", user)
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
			fixture.Calls = []llm.ToolCall{llm.ToolCall{Type: "function", Function: llm.CallFunction{Name: "find_posts", Arguments: `{"source":"feed"}`}}}
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

func TestCappedDayWaitsForTheNextUTCDayWithoutAZone(t *testing.T) {
	tomorrow := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, st *store.Store)
		want  time.Time
	}{
		{"nothing capped: behind still wakes at the minimum", func(*testing.T, *store.Store) {}, kpiNow.Add(20 * time.Minute)},
		{"daily USD spent", func(t *testing.T, st *store.Store) {
			if _, err := st.DB().Exec(`INSERT INTO ledger_phases(agent_id,day,phase,cost_usd) VALUES('synthetic-offline','2026-01-01','agent',0.9) ON CONFLICT(agent_id,day,phase) DO UPDATE SET cost_usd=cost_usd+0.9`); err != nil {
				t.Fatal(err)
			}
		}, tomorrow},
		{"critic and dream spend count toward the daily USD", func(t *testing.T, st *store.Store) {
			if _, err := st.DB().Exec(`INSERT INTO ledger_phases(agent_id,day,phase,cost_usd) VALUES('synthetic-offline','2026-01-01','critic',0.5),('synthetic-offline','2026-01-01','dream',0.5)`); err != nil {
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
			fixture.Calls = []llm.ToolCall{agent.SyntheticCall("find_posts", `{"source":"feed"}`)}
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

// Spend and both action caps reset at the same next local midnight (17:00Z at UTC+7).
func TestCappedSpendWaitsForLocalMidnight(t *testing.T) {
	localMidnight := time.Date(2026, 1, 1, 17, 0, 0, 0, time.UTC)
	spend := func(day string) func(t *testing.T, st *store.Store) {
		return func(t *testing.T, st *store.Store) {
			if _, err := st.DB().Exec(`INSERT INTO ledger_phases(agent_id,day,phase,cost_usd) VALUES('synthetic-offline',?,'agent',0.9) ON CONFLICT(agent_id,day,phase) DO UPDATE SET cost_usd=cost_usd+0.9`, day); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, st *store.Store)
		want  time.Time
	}{
		{"spend", spend("2026-01-01"), localMidnight},
		{"spend of the previous local day does not cap", spend("2025-12-31"), kpiNow.Add(20 * time.Minute)},
		{"spend and action caps", func(t *testing.T, st *store.Store) {
			spend("2026-01-01")(t, st)
			for i := 0; i < 15; i++ {
				seedToday(t, st, "reply", i)
			}
			for i := 0; i < 2; i++ {
				seedToday(t, st, "post", 100+i)
			}
		}, localMidnight},
		{"action caps alone", func(t *testing.T, st *store.Store) {
			for i := 0; i < 15; i++ {
				seedToday(t, st, "reply", i)
			}
			for i := 0; i < 3; i++ {
				seedToday(t, st, "post", 100+i)
			}
		}, localMidnight},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, st, fixture := kpiRuntimeIn(t, "Asia/Ho_Chi_Minh", 9*time.Hour, 3)
			fixture.Calls = []llm.ToolCall{agent.SyntheticCall("find_posts", `{"source":"feed"}`)}
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

// Budget pacing: $0.62 spent over 5 wakes today (the wake that just ran counts)
// is $0.124 a wake; $0.38 is left for the 12 hours to midnight, so the next wake
// is 12h * 0.124 / 0.38 = about 235 minutes away, not the 20 minute minimum.
func TestBehindAgentPacesItsBudgetOverTheRestOfTheDay(t *testing.T) {
	r, st, fixture := kpiRuntime(t, 9*time.Hour, 3)
	fixture.Calls = []llm.ToolCall{agent.SyntheticCall("find_posts", `{"source":"feed"}`)}
	if _, err := st.DB().Exec(`INSERT INTO ledger_phases(agent_id,day,phase,cost_usd) VALUES('synthetic-offline','2026-01-01','critic',0.5)`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Wake(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := st.AgentState(context.Background(), "synthetic-offline")
	if err != nil {
		t.Fatal(err)
	}
	if got := state.NextWake.Sub(kpiNow); got < 234*time.Minute || got > 236*time.Minute {
		t.Fatalf("next wake in %v, want about 235 minutes", got)
	}
}

func seedToday(t *testing.T, st *store.Store, kind string, n int) {
	t.Helper()
	if _, err := st.SeedAction(context.Background(), store.SeededAction{AgentID: "synthetic-offline", Kind: kind, Text: kind + strconv.Itoa(n), DryRun: true, PostedAt: kpiNow.Add(-time.Duration(n+1) * time.Minute)}); err != nil {
		t.Fatal(err)
	}
}

// The host zone never reaches the model: the wake context carries no timestamp,
// only ages and local clock times.
func TestWakeContextHasNoTimestamps(t *testing.T) {
	old := time.Local
	time.Local = time.FixedZone("host", 8*3600)
	t.Cleanup(func() { time.Local = old })
	r, _, _ := kpiRuntimeFull(t, "Asia/Ho_Chi_Minh", 30*time.Minute, 3, 120)
	messages, err := r.Context(context.Background(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	if stamp := regexp.MustCompile(`\d{4}-\d\d-\d\dT|\+08:00`); stamp.MatchString(messages[1].Content) {
		t.Errorf("context carries a timestamp:\n%.1500s", messages[1].Content)
	}
	if !strings.Contains(messages[1].Content, "next post allowed at 20:30 local") {
		t.Errorf("context lacks the template-zone clock time:\n%.1500s", messages[1].Content)
	}
}

func TestLimitsStateShowsWhatIsLeftAndAnIdleWakeIsSkipped(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name                string
		posts, replies, gap int
		wantLine            string
	}{
		{"open", 0, 0, 0, "- Posts: 3 of 3 left today.\n- Replies: 15 of 15 left today."},
		{"posts used", 3, 0, 0, "- Posts: 0 of 3 left today.\n"},
		{"post gap outlasts the wake", 1, 0, 600, "next post allowed at"},
		{"replies used", 0, 15, 0, "- Replies: 0 of 15 left today."},
	} {
		r, st, _ := kpiRuntimeFull(t, "", 24*time.Hour, 3, tc.gap)
		if _, err := st.DB().Exec(`DELETE FROM actions`); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < tc.posts; i++ {
			seedToday(t, st, "post", i)
		}
		for i := 0; i < tc.replies; i++ {
			seedToday(t, st, "reply", 100+i)
		}
		messages, err := r.Context(ctx, "agent")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(messages[1].Content, tc.wantLine) {
			t.Errorf("%s: line %q missing:\n%s", tc.name, tc.wantLine, messages[1].Content)
		}
	}

	calls := 0
	r, st, fixture := kpiRuntimeFull(t, "", 24*time.Hour, 3, 0)
	fixture.Observe = func([]llm.Message) { calls++ }
	fixture.Calls = []llm.ToolCall{agent.SyntheticCall("find_posts", `{"source":"feed"}`)}
	for i := 0; i < 3; i++ {
		seedToday(t, st, "post", i)
	}
	for i := 0; i < 15; i++ {
		seedToday(t, st, "reply", 100+i)
	}
	wake, err := r.Wake(ctx)
	if err != nil || wake.EndReason != "nothing_to_do" || calls != 0 {
		t.Fatalf("wake %+v err %v model calls %d", wake, err, calls)
	}
	state, err := st.AgentState(ctx, "synthetic-offline")
	if err != nil || !state.NextWake.After(kpiNow) {
		t.Fatalf("next wake %v err %v", state.NextWake, err)
	}
}

// Engagements over target with followers under the weekly target is still
// behind, so the wake cadence stays at the template minimum.
func TestFollowersUnderTheWeeklyTargetAreBehind(t *testing.T) {
	r, st, fixture := kpiRuntime(t, time.Hour, 30)
	if _, err := st.DB().Exec(`UPDATE metrics SET followers=100 WHERE measured_at='2026-01-01T11:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	messages, err := r.Context(context.Background(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	user := messages[1].Content
	if want := "behind: 30 of 10 engagements in 24h, 0 of 5 followers gained in 7 days, after $0.12 and 4 wakes today\n"; !strings.Contains(user, want) || strings.Contains(user, "on target") {
		t.Fatalf("scoreboard lacks %q in:\n%.900s", want, user)
	}
	fixture.Calls = []llm.ToolCall{{Type: "function", Function: llm.CallFunction{Name: "find_posts", Arguments: `{"source":"feed"}`}}}
	if _, err = r.Wake(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := st.AgentState(context.Background(), "synthetic-offline")
	if want := kpiNow.Add(20 * time.Minute); err != nil || !state.NextWake.Equal(want) {
		t.Fatalf("next wake %v, want the template minimum %v (err %v)", state.NextWake, want, err)
	}
}
