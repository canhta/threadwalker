package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
)

func snapshotStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "snapshot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, id := range []string{"alpha", "beta"} {
		if err = st.EnsureAgent(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func exec(t *testing.T, st *store.Store, q string, args ...any) {
	t.Helper()
	if _, err := st.DB().Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func snapshotQuery() store.DashboardQuery {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	return store.DashboardQuery{AgentID: "alpha", From: time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC), Now: now}
}

func TestDashboardSnapshotReadsOneAgentsFactsWithinBounds(t *testing.T) {
	ctx := context.Background()
	st := snapshotStore(t)
	const stampZ = "2006-01-02T15:04:05Z"
	exec(t, st, `UPDATE agents SET status='paused',next_wake_at='2026-11-12T13:00:00Z' WHERE id='alpha'`)

	// Publications: all real actions, newest first, with the long link blanked;
	// drafts are only counted.
	exec(t, st, `INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url) VALUES('alpha','post','t',0,'2026-11-01T00:00:00Z','https://threads.net/@a/post/OLD')`)
	exec(t, st, `INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url) VALUES('alpha','reply','t',0,'2026-11-11T00:00:00Z',?)`, "https://threads.net/@a/post/"+strings.Repeat("x", 600))
	exec(t, st, `INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url) VALUES('alpha','post','t',1,'2026-11-11T00:00:00Z','')`)
	exec(t, st, `INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url) VALUES('beta','post','t',0,'2026-11-11T00:00:00Z','https://threads.net/@b/post/B')`)
	for i := 0; i < 25; i++ {
		exec(t, st, `INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url) VALUES('alpha','post','t',0,'2026-11-11T00:00:00Z',?)`, fmt.Sprintf("https://threads.net/@a/post/P%d", i))
	}

	// Wakes: the range holds the newest 20 by instant, whatever offset or
	// precision was stored (10:00 at +07:00 is 03:00 UTC); the last wake is read whatever its age.
	exec(t, st, `INSERT INTO wakes(agent_id,started_at,ended_at,end_reason,kind,steps,cost_usd) VALUES('alpha','2026-11-01T00:00:00Z','2026-11-01T00:10:00Z','completed','agent',2,0.5)`)
	exec(t, st, `INSERT INTO wakes(agent_id,started_at,kind) VALUES('alpha','2026-11-12T10:00:00.5+07:00','dream')`)
	exec(t, st, `INSERT INTO wakes(agent_id,started_at,kind) VALUES('alpha','2026-11-12T09:00:00Z','agent')`)
	for i := 0; i < 21; i++ {
		exec(t, st, `INSERT INTO wakes(agent_id,started_at,kind) VALUES('alpha',?,'agent')`, time.Date(2026, 11, 8, 0, i, 0, 0, time.UTC).Format(stampZ))
	}
	exec(t, st, `INSERT INTO wakes(agent_id,started_at,kind) VALUES('beta','2026-11-12T11:00:00Z','agent')`)

	// Tool calls: range only, long results blanked.
	exec(t, st, `INSERT INTO tool_calls(agent_id,wake_id,tool,args_json,result_json,blocked_by,at) VALUES('alpha',1,'search','secret','{"outcome":"error"}','','2026-11-12T11:00:00Z')`)
	exec(t, st, `INSERT INTO tool_calls(agent_id,wake_id,tool,args_json,result_json,blocked_by,at) VALUES('alpha',1,'post','secret',?,'phase_read_only','2026-11-12T11:30:00Z')`, strings.Repeat("r", 2000))
	exec(t, st, `INSERT INTO tool_calls(agent_id,wake_id,tool,args_json,at) VALUES('alpha',1,'old','x','2026-10-01T00:00:00Z')`)

	// Trends, spend.
	exec(t, st, `INSERT INTO trend_stories(agent_id,observed_at,source,title,url,rank) VALUES('alpha','2026-11-12T08:00:00Z','hacker_news','In range','https://example.invalid/a',2)`)
	exec(t, st, `INSERT INTO trend_stories(agent_id,observed_at,source,title,url,rank) VALUES('alpha','2026-10-01T08:00:00Z','hacker_news','Too old','https://example.invalid/b',1)`)
	exec(t, st, `INSERT INTO ledger_phases(agent_id,day,phase,cost_usd) VALUES('alpha','2026-11-05','agent',9),('alpha','2026-11-06','agent',0.1),('beta','2026-11-12','agent',7)`)
	exec(t, st, `INSERT INTO ledger_phases(agent_id,day,phase,cost_usd) VALUES('alpha','2026-11-12','critic',0.05),('alpha','2026-11-12','agent',0.15),('alpha','2026-11-01','dream',8)`)

	// Metrics: real posts and replies of this agent only, newest action first.
	exec(t, st, `INSERT INTO metrics(agent_id,action_id,measured_at,views,likes,replies,reposts) VALUES('alpha',1,'2026-11-09T00:00:00Z',9,0,0,0),('alpha',2,'2026-11-12T00:00:00Z',20,2,1,0),('alpha',2,'2026-11-10T00:00:00Z',10,1,0,0),('alpha',3,'2026-11-10T00:00:00Z',1,1,1,1)`)

	snap, err := st.DashboardSnapshot(ctx, snapshotQuery())
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Started || snap.Status != "paused" || !snap.NextWake.Equal(time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("agent: %+v", snap)
	}
	if snap.Drafts != 1 || len(snap.Publications) != 27 {
		t.Fatalf("drafts %d publications %d", snap.Drafts, len(snap.Publications))
	}
	if p := snap.Publications; p[0].ID <= p[1].ID || p[len(p)-1].URL == "" || p[len(p)-2].URL != "" || p[len(p)-2].Kind != "reply" || !p[len(p)-1].PostedAt.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("publications: %+v", p)
	}
	if len(snap.Wakes) != 20 || snap.Wakes[0].StartedAt.Hour() != 9 || snap.Wakes[0].Ended || snap.Wakes[1].StartedAt.Hour() != 3 {
		t.Fatalf("wakes: %d first %+v", len(snap.Wakes), snap.Wakes[:2])
	}
	if snap.LastWake == nil || snap.LastWake.StartedAt.Hour() != 9 || snap.LastWake.Kind != "agent" {
		t.Fatalf("last wake: %+v", snap.LastWake)
	}
	if len(snap.ToolCalls) != 2 || snap.ToolCalls[0].Tool != "post" || snap.ToolCalls[0].Result != "" || snap.ToolCalls[0].BlockedBy != "phase_read_only" || snap.ToolCalls[1].Result != `{"outcome":"error"}` {
		t.Fatalf("tool calls: %+v", snap.ToolCalls)
	}
	if len(snap.Trends) != 1 || snap.Trends[0].Title != "In range" || snap.Trends[0].Rank != 2 || snap.Trends[0].ObservedAt.IsZero() {
		t.Fatalf("trends: %+v", snap.Trends)
	}
	if len(snap.Ledger) != 2 || snap.Ledger[0].Day != "2026-11-06" || snap.Ledger[1].CostUSD < 0.1999 || snap.Ledger[1].CostUSD > 0.2001 {
		t.Fatalf("ledger: %+v", snap.Ledger)
	}
	if len(snap.Phases) != 3 || snap.Phases[1].Phase != "agent" || snap.Phases[2].Phase != "critic" {
		t.Fatalf("phases: %+v", snap.Phases)
	}
	if got := fmt.Sprint(metricKeys(snap.Metrics)); got != "[2:10 2:20 1:9]" {
		t.Fatalf("metrics: %v", got)
	}

	// The prior range is reached by the caller's From; a later range sees fewer rows.
	q := snapshotQuery()
	q.From = time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	if snap, err = st.DashboardSnapshot(ctx, q); err != nil || len(snap.Wakes) != 2 || len(snap.Ledger) != 1 {
		t.Fatalf("one-day range: %+v %v", snap, err)
	}

	q = snapshotQuery()
	q.AgentID = "unknown"
	if snap, err = st.DashboardSnapshot(ctx, q); err != nil || snap.Started || snap.LastWake != nil || len(snap.Publications) != 0 {
		t.Fatalf("unknown agent: %+v %v", snap, err)
	}
}

func metricKeys(ms []store.DashboardMetric) []string {
	var out []string
	for _, m := range ms {
		out = append(out, fmt.Sprintf("%d:%d", m.ActionID, m.Views))
	}
	return out
}

func TestDashboardSnapshotFailsClosed(t *testing.T) {
	st := snapshotStore(t)
	for _, q := range []store.DashboardQuery{{}, {AgentID: "alpha"}, {AgentID: "alpha", From: time.Now(), Now: time.Now().Add(-time.Hour)}} {
		if _, err := st.DashboardSnapshot(context.Background(), q); err == nil {
			t.Fatalf("accepted invalid scope %+v", q)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := st.DashboardSnapshot(cancelled, snapshotQuery()); err == nil {
		t.Fatal("ignored cancellation")
	}
	for _, table := range []string{"agents", "actions", "wakes", "tool_calls", "trend_stories", "ledger_phases", "metrics"} {
		t.Run(table, func(t *testing.T) {
			broken := snapshotStore(t)
			exec(t, broken, `DROP TABLE `+table)
			if _, err := broken.DashboardSnapshot(context.Background(), snapshotQuery()); err == nil {
				t.Fatal("hid a missing table")
			}
		})
	}
}

func TestDashboardSnapshotNeverWrites(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "readonly.db")
	writable, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer writable.Close()
	if err = writable.EnsureAgent(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	ro, err := store.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err = ro.DashboardSnapshot(ctx, snapshotQuery()); err != nil {
		t.Fatal(err)
	}
	q := snapshotQuery()
	q.AgentID = "never-initialised"
	if _, err = ro.DashboardSnapshot(ctx, q); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = writable.DB().QueryRow(`SELECT COUNT(*) FROM agents`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("snapshot initialised an agent: %d %v", n, err)
	}
}

func TestDashboardSnapshotCarriesGrowthAndTokens(t *testing.T) {
	ctx := context.Background()
	st := snapshotStore(t)
	exec(t, st, `INSERT INTO actions(id,agent_id,kind,text,dry_run,posted_at,thread_url,followers_at_post) VALUES(1,'alpha','post','the body',0,'2026-11-11T00:00:00Z','https://threads.net/@a/post/A',50)`)
	exec(t, st, `INSERT INTO metrics(agent_id,action_id,measured_at,views,quotes,followers) VALUES('alpha',1,'2026-11-12T00:00:00Z',10,2,57)`)
	exec(t, st, `INSERT INTO ledger_phases(agent_id,day,phase,tokens_in,tokens_out,cost_usd) VALUES('alpha','2026-11-12','agent',900,100,0.5),('alpha','2026-10-01','dream',1,1,2)`)

	snap, err := st.DashboardSnapshot(ctx, snapshotQuery())
	if err != nil {
		t.Fatal(err)
	}
	p := snap.Publications[0]
	if p.FollowersAtPost == nil || *p.FollowersAtPost != 50 || p.Text != "the body" {
		t.Fatalf("publication: %+v", p)
	}
	if m := snap.Metrics[0]; m.Quotes != 2 || m.Followers == nil || *m.Followers != 57 {
		t.Fatalf("metric: %+v", m)
	}
	if snap.SpendTotal != 2.5 || len(snap.Phases) != 1 || snap.Phases[0].TokensIn != 900 || snap.Phases[0].TokensOut != 100 {
		t.Fatalf("spend: total %v phases %+v", snap.SpendTotal, snap.Phases)
	}
}
