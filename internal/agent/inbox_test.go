package agent_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
)

type inboxRig struct {
	r       *agent.Runtime
	st      *store.Store
	mem     *threads.Memory
	now     time.Time
	farm    time.Time
	fixture *agent.OfflineFixture
}

func newInboxRig(t *testing.T, poll int) *inboxRig {
	t.Helper()
	return newInboxRigWith(t, poll, func(*config.Template) {})
}

// newInboxRigWith is newInboxRig with the template changed by edit.
func newInboxRigWith(t *testing.T, poll int, edit func(*config.Template)) *inboxRig {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "inbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rig := &inboxRig{st: st, mem: threads.NewMemory(), now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	rig.mem.Pages[threads.OpReadReplies] = threads.Page{Kind: "replies"}
	tmpl := agent.OfflineTemplate()
	tmpl.Inbox = config.InboxConfig{PollMinutes: poll, MinMinutesAfterWake: 5}
	edit(&tmpl)
	fixture := agent.NewOfflineFixture(func() time.Time { return rig.now })
	rig.fixture = fixture
	rig.r, err = agent.New(agent.Options{Store: st, Template: tmpl, Now: func() time.Time { return rig.now }, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends,
		OwnUsernames:   []string{"synthetic_offline"},
		ThreadsFactory: func(context.Context, config.Template) (threads.Backend, error) { return rig.mem, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err = rig.r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SeedAction(ctx, store.SeededAction{AgentID: tmpl.ID, Kind: "post", Text: "ours", ThreadURL: "https://www.threads.com/@synthetic_offline/post/m1", MediaID: "m1", PostedAt: rig.now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	rig.farm = rig.now.Add(3 * time.Hour)
	rig.park(t)
	return rig
}

// park puts the scheduled wake far away.
func (g *inboxRig) park(t *testing.T) {
	t.Helper()
	if err := g.st.SetNextWake(context.Background(), "synthetic-offline", g.farm); err != nil {
		t.Fatal(err)
	}
}

func (g *inboxRig) reply(id, user string) {
	p := threads.Post{ID: id, Username: user, Text: "hello", URL: "https://www.threads.com/@" + user + "/post/" + id, Timestamp: g.now.Add(-time.Minute)}
	page := g.mem.Replies["m1"]
	page.Kind = "replies"
	page.Posts = append(page.Posts, p)
	if g.mem.Replies == nil {
		g.mem.Replies = map[string]threads.Page{}
	}
	g.mem.Replies["m1"] = page
}

func (g *inboxRig) poll(t *testing.T) time.Time {
	t.Helper()
	if err := g.r.PollInbox(context.Background(), g.now); err != nil {
		t.Fatal(err)
	}
	state, err := g.st.AgentState(context.Background(), "synthetic-offline")
	if err != nil {
		t.Fatal(err)
	}
	return state.NextWake
}

func TestPollInboxWakesEarlyOncePerReply(t *testing.T) {
	g := newInboxRig(t, 10)
	if next := g.poll(t); !next.Equal(g.farm) {
		t.Fatalf("nothing new moved the wake to %v", next)
	}
	g.now = g.now.Add(10 * time.Minute)
	g.reply("a1", "alice")
	if next := g.poll(t); !next.Equal(g.now) {
		t.Fatalf("new reply: wake at %v, want now %v", next, g.now)
	}

	g.park(t)
	g.now = g.now.Add(10 * time.Minute)
	if next := g.poll(t); !next.Equal(g.farm) {
		t.Fatalf("a seen reply woke the agent again: %v", next)
	}
	g.now = g.now.Add(10 * time.Minute)
	g.reply("a2", "alice")
	if next := g.poll(t); !next.Equal(g.now) {
		t.Fatalf("second new reply: wake at %v", next)
	}
}

func TestPollInboxHonoursThePollIntervalAndTheGapAfterAWake(t *testing.T) {
	g := newInboxRig(t, 10)
	g.reply("a1", "alice")
	if _, err := g.st.DB().Exec(`INSERT INTO wakes(agent_id,started_at) VALUES('synthetic-offline',?)`, g.now.Add(-time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if next := g.poll(t); !next.Equal(g.now.Add(4 * time.Minute)) {
		t.Fatalf("wake at %v, want 5 minutes after the last wake started", next)
	}
	calls := g.mem.Count(threads.OpReadReplies)
	g.now = g.now.Add(5 * time.Minute)
	g.poll(t)
	if g.mem.Count(threads.OpReadReplies) != calls {
		t.Fatal("polled again before inbox.poll_minutes passed")
	}
}

// An early wake for a reply still waits out the quiet window.
func TestPollInboxWakeEarlyWaitsOutQuietHours(t *testing.T) {
	g := newInboxRigWith(t, 10, func(tmpl *config.Template) {
		tmpl.Schedule = config.ScheduleConfig{Timezone: "UTC", QuietFrom: "11:00", QuietUntil: "13:30"}
	})
	g.reply("a1", "alice")
	if next, want := g.poll(t), time.Date(2026, 1, 1, 13, 30, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("wake at %v, want the end of the quiet window %v", next, want)
	}
}

// A capped day (daily USD spent) takes no early wake before it resets.
func TestPollInboxWakeEarlyWaitsOutACappedDay(t *testing.T) {
	g := newInboxRig(t, 10)
	if _, err := g.st.DB().Exec(`INSERT INTO ledger_phases(agent_id,day,phase,cost_usd) VALUES('synthetic-offline','2026-01-01','critic',5)`); err != nil {
		t.Fatal(err)
	}
	g.reply("a1", "alice")
	g.farm = g.now.Add(30 * time.Hour)
	g.park(t)
	if next, want := g.poll(t), time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("wake at %v, want the next day %v", next, want)
	}
}

func TestPollInboxDisabledReadsNothing(t *testing.T) {
	g := newInboxRig(t, 0)
	g.reply("a1", "alice")
	if next := g.poll(t); !next.Equal(g.farm) || g.mem.Count(threads.OpReadReplies) != 0 {
		t.Fatalf("disabled poll acted: %v %d", next, g.mem.Count(threads.OpReadReplies))
	}
}

func TestPollInboxFailureBacksOffAndKeepsTheProcessRunning(t *testing.T) {
	g := newInboxRig(t, 10)
	g.mem.Errors[threads.OpReadReplies] = errors.New("api down")
	g.reply("a1", "alice")
	if next := g.poll(t); !next.Equal(g.farm) {
		t.Fatalf("failed poll moved the wake: %v", next)
	}
	calls := g.mem.Count(threads.OpReadReplies)
	g.now = g.now.Add(14 * time.Minute)
	g.poll(t)
	if g.mem.Count(threads.OpReadReplies) != calls {
		t.Fatal("retried before the backoff passed")
	}
	delete(g.mem.Errors, threads.OpReadReplies)
	g.now = g.now.Add(2 * time.Minute)
	if next := g.poll(t); !next.Equal(g.now) {
		t.Fatalf("recovered poll: wake at %v", next)
	}
}

func TestWakeDataMessageCarriesTheInboxAfterTheState(t *testing.T) {
	g := newInboxRig(t, 10)
	g.reply("a1", "alice")
	var seen string
	g.fixture.Observe = func(m []llm.Message) {
		if seen == "" && len(m) > 1 {
			seen = m[1].Content
		}
	}
	if err := g.st.SetNextWake(context.Background(), "synthetic-offline", g.now); err != nil {
		t.Fatal(err)
	}
	if _, err := g.r.Wake(context.Background()); err != nil {
		t.Fatal(err)
	}
	if inbox := strings.Index(seen, "# Inbox: people who replied to you"); inbox < strings.Index(seen, `"counters"`) || !strings.Contains(seen, `"author":"alice"`) || !strings.Contains(seen, `"replying_to":"ours"`) {
		t.Fatalf("data message does not carry the inbox after the state: %.300s", seen)
	}
}

// A wake time stored inside the quiet window (a failed wake's fallback, a
// recovered wake, a stale next_wake_at) never starts a wake: the scheduler moves it.
func TestSchedulerDefersAnAgentWakeDueInQuietHours(t *testing.T) {
	g := newInboxRigWith(t, 0, func(tmpl *config.Template) {
		tmpl.Schedule = config.ScheduleConfig{Timezone: "UTC", QuietFrom: "11:00", QuietUntil: "13:30"}
	})
	if err := g.st.SetNextWake(context.Background(), "synthetic-offline", g.now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := (agent.Scheduler{Agents: []*agent.Runtime{g.r}, Now: func() time.Time { return g.now }}).Run(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	state, err := g.st.AgentState(context.Background(), "synthetic-offline")
	if want := time.Date(2026, 1, 1, 13, 30, 0, 0, time.UTC); err != nil || !state.NextWake.Equal(want) {
		t.Fatalf("next wake %v, want the end of the quiet window %v (err %v)", state.NextWake, want, err)
	}
	var wakes int
	if err = g.st.DB().QueryRow(`SELECT COUNT(*) FROM wakes`).Scan(&wakes); err != nil || wakes != 0 {
		t.Fatalf("wakes started in quiet hours: %d (err %v)", wakes, err)
	}
}

// An early wake follows the budget pacing too: $0.20 over one wake, $0.80 left
// for the 12 hours to midnight, is 3 hours after the last wake started.
func TestPollInboxWakeEarlyWaitsForThePacedWake(t *testing.T) {
	g := newInboxRig(t, 10)
	g.farm = g.now.Add(10 * time.Hour)
	g.park(t)
	for _, q := range []string{
		`INSERT INTO ledger_phases(agent_id,day,phase,cost_usd) VALUES('synthetic-offline','2026-01-01','agent',0.2)`,
		`INSERT INTO wakes(agent_id,started_at) VALUES('synthetic-offline','2026-01-01T11:00:00Z')`,
	} {
		if _, err := g.st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	g.reply("a1", "alice")
	if next, want := g.poll(t), time.Date(2026, 1, 1, 14, 0, 0, 0, time.UTC); next.Sub(want) < -time.Minute || next.Sub(want) > time.Minute {
		t.Fatalf("wake at %v, want about %v", next, want)
	}
}
