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

func snapshotQuery(content bool) store.DashboardQuery {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	return store.DashboardQuery{AgentID: "alpha", From: time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC), Now: now, Content: content}
}

func TestDashboardSnapshotReadsOneAgentsFactsWithinBounds(t *testing.T) {
	ctx := context.Background()
	st := snapshotStore(t)
	const stampZ = "2006-01-02T15:04:05Z"
	exec(t, st, `UPDATE agents SET status='paused',next_wake_at='2026-11-12T13:00:00Z' WHERE id='alpha'`)
	exec(t, st, `INSERT INTO memory_index(agent_id,text,created_at,archived_at) VALUES('alpha','kept first','2026-11-12T00:00:00Z',NULL),('alpha','archived','2026-11-12T00:00:00Z','2026-11-12T01:00:00Z'),('alpha','kept second','2026-11-12T02:00:00Z',NULL),('beta','other agent','2026-11-12T00:00:00Z',NULL)`)

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
	exec(t, st, `INSERT INTO wakes(agent_id,started_at,kind) VALUES('alpha','2026-11-12T10:00:00.5+07:00','study')`)
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
	exec(t, st, `INSERT INTO trend_stories(agent_id,observed_at,phase,source,title,url,rank) VALUES('alpha','2026-11-12T08:00:00Z','study','hacker_news','In range','https://example.invalid/a',2)`)
	exec(t, st, `INSERT INTO trend_stories(agent_id,observed_at,phase,source,title,url,rank) VALUES('alpha','2026-10-01T08:00:00Z','study','hacker_news','Too old','https://example.invalid/b',1)`)
	exec(t, st, `INSERT INTO ledger_phases(agent_id,day,phase,cost_usd) VALUES('alpha','2026-11-05','agent',9),('alpha','2026-11-06','agent',0.1),('beta','2026-11-12','agent',7)`)
	exec(t, st, `INSERT INTO ledger_phases(agent_id,day,phase,cost_usd) VALUES('alpha','2026-11-12','study',0.05),('alpha','2026-11-12','agent',0.15),('alpha','2026-11-01','dream',8)`)

	// Learning.
	for i := 0; i < 22; i++ {
		status := []string{"testing", "established", "rejected"}[i%3]
		exec(t, st, `INSERT INTO playbook(agent_id,text,status,n,created_at) VALUES('alpha',?,?,2,'2026-11-01T00:00:00Z')`, fmt.Sprintf("idea %d", i), status)
	}
	exec(t, st, `INSERT INTO playbook_decisions(agent_id,hypothesis_id,revision,status,n,reason,decided_at) VALUES('alpha',22,1,'rejected',2,'first','2026-11-05T00:00:00Z'),('alpha',22,2,'established',2,'latest','2026-11-10T08:00:00+07:00')`)
	exec(t, st, `INSERT INTO playbook_outcomes(agent_id,action_id,hypothesis_id,revision,measured_at,reason,counted) VALUES('alpha',1,22,1,'2026-11-10T00:00:00Z','r',1),('alpha',2,22,1,'2026-11-10T00:00:00Z','r',1),('alpha',3,22,1,'2026-11-10T00:00:00Z','r',0)`)
	exec(t, st, `INSERT INTO notes(agent_id,text,created_at) VALUES('alpha','first','2026-11-10T00:00:00Z'),('alpha','second','2026-11-11T00:00:00Z'),('beta','other','2026-11-11T00:00:00Z')`)

	// Metrics: real posts and replies of this agent only, newest action first.
	exec(t, st, `INSERT INTO metrics(agent_id,action_id,measured_at,views,likes,replies,reposts) VALUES('alpha',1,'2026-11-09T00:00:00Z',9,0,0,0),('alpha',2,'2026-11-12T00:00:00Z',20,2,1,0),('alpha',2,'2026-11-10T00:00:00Z',10,1,0,0),('alpha',3,'2026-11-10T00:00:00Z',1,1,1,1)`)

	snap, err := st.DashboardSnapshot(ctx, snapshotQuery(false))
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Started || snap.Status != "paused" || !snap.NextWake.Equal(time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC)) || len(snap.MemoryIndex) != 0 {
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
	if len(snap.Phases) != 3 || snap.Phases[1].Phase != "agent" || snap.Phases[2].Phase != "study" {
		t.Fatalf("phases: %+v", snap.Phases)
	}
	if snap.Playbook != (store.DashboardPlaybook{Testing: 8, Established: 7, Rejected: 7, Samples: 44}) {
		t.Fatalf("playbook: %+v", snap.Playbook)
	}
	if len(snap.Hypotheses) != 20 || snap.Hypotheses[0].ID != 22 || snap.Hypotheses[0].Text != "idea 21" || snap.Hypotheses[0].Decision != nil || snap.Hypotheses[0].Outcomes != 0 {
		t.Fatalf("hypotheses without content: %+v", snap.Hypotheses[0])
	}
	if snap.NoteCount != 2 || len(snap.Notes) != 0 {
		t.Fatalf("notes without content: %d %+v", snap.NoteCount, snap.Notes)
	}
	if got := fmt.Sprint(metricKeys(snap.Metrics)); got != "[2:10 2:20 1:9]" {
		t.Fatalf("metrics: %v", got)
	}

	snap, err = st.DashboardSnapshot(ctx, snapshotQuery(true))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(snap.MemoryIndex) != "[kept first kept second]" {
		t.Fatalf("memory index %q, want alpha's live entries oldest first", snap.MemoryIndex)
	}
	h := snap.Hypotheses[0]
	if h.Text != "idea 21" || h.Decision == nil || h.Decision.Reason != "latest" || h.Decision.Status != "established" || h.Outcomes != 2 {
		t.Fatalf("hypothesis with content: %+v decision %+v", h, h.Decision)
	}
	if h2 := snap.Hypotheses[1]; h2.Decision != nil || h2.Outcomes != 0 {
		t.Fatalf("hypothesis without evidence: %+v", h2)
	}
	if len(snap.Notes) != 2 || snap.Notes[0].Text != "second" || snap.Notes[0].CreatedAt.IsZero() {
		t.Fatalf("notes: %+v", snap.Notes)
	}

	// The prior range is reached by the caller's From; a later range sees fewer rows.
	q := snapshotQuery(false)
	q.From = time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	if snap, err = st.DashboardSnapshot(ctx, q); err != nil || len(snap.Wakes) != 2 || len(snap.Ledger) != 1 {
		t.Fatalf("one-day range: %+v %v", snap, err)
	}

	q = snapshotQuery(false)
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
	if _, err := st.DashboardSnapshot(cancelled, snapshotQuery(false)); err == nil {
		t.Fatal("ignored cancellation")
	}
	for _, table := range []string{"agents", "actions", "wakes", "tool_calls", "trend_stories", "ledger_phases", "playbook", "notes", "metrics"} {
		t.Run(table, func(t *testing.T) {
			broken := snapshotStore(t)
			exec(t, broken, `DROP TABLE `+table)
			if _, err := broken.DashboardSnapshot(context.Background(), snapshotQuery(true)); err == nil {
				t.Fatal("hid a missing table")
			}
		})
	}
	// Content mode reads the decision tables too.
	broken := snapshotStore(t)
	exec(t, broken, `INSERT INTO playbook(agent_id,text,status,n,created_at) VALUES('alpha','x','testing',1,'2026-11-01T00:00:00Z')`)
	for _, table := range []string{"playbook_decisions", "playbook_outcomes"} {
		exec(t, broken, `ALTER TABLE `+table+` RENAME TO gone_`+table)
		if _, err := broken.DashboardSnapshot(context.Background(), snapshotQuery(true)); err == nil {
			t.Fatalf("hid missing %s", table)
		}
		exec(t, broken, `ALTER TABLE gone_`+table+` RENAME TO `+table)
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
	if _, err = ro.DashboardSnapshot(ctx, snapshotQuery(true)); err != nil {
		t.Fatal(err)
	}
	q := snapshotQuery(true)
	q.AgentID = "never-initialised"
	if _, err = ro.DashboardSnapshot(ctx, q); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = writable.DB().QueryRow(`SELECT COUNT(*) FROM agents`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("snapshot initialised an agent: %d %v", n, err)
	}
}

func TestDashboardSnapshotStrategyIsContentOnlyBoundedAndPerAgent(t *testing.T) {
	ctx := context.Background()
	st := snapshotStore(t)
	for v := 1; v <= store.DashboardStrategyShown+3; v++ {
		exec(t, st, `INSERT INTO strategy_revisions(agent_id,version,text,citations_json,created_at) VALUES('alpha',?,?,'["action:1"]','2026-11-11T00:00:00Z')`, v, fmt.Sprintf("v%d", v))
	}
	exec(t, st, `INSERT INTO strategy_revisions(agent_id,version,text,citations_json,created_at) VALUES('beta',1,'foreign','[]','2026-11-11T00:00:00Z')`)
	public, err := st.DashboardSnapshot(ctx, snapshotQuery(false))
	if err != nil || len(public.Strategy) != 0 {
		t.Fatalf("public strategy = %v, %v", public.Strategy, err)
	}
	snap, err := st.DashboardSnapshot(ctx, snapshotQuery(true))
	if err != nil || len(snap.Strategy) != store.DashboardStrategyShown+1 || snap.Strategy[0].Version != store.DashboardStrategyShown+3 || snap.Strategy[0].Citations[0] != "action:1" {
		t.Fatalf("strategy = %+v, %v", snap.Strategy, err)
	}
}

func TestDashboardSnapshotCarriesOutcomesGrowthAndTokens(t *testing.T) {
	ctx := context.Background()
	st := snapshotStore(t)
	exec(t, st, `INSERT INTO actions(id,agent_id,kind,text,dry_run,posted_at,thread_url,followers_at_post) VALUES(1,'alpha','post','the body',0,'2026-11-11T00:00:00Z','https://threads.net/@a/post/A',50)`)
	exec(t, st, `INSERT INTO action_outcomes(agent_id,action_id,measured_at,model_version,views,conversation_rate,applause_rate,reach_lift,resonance_lift,label) VALUES('alpha',1,'2026-11-12T00:00:00Z',1,10,0,0,0.5,-0.25,'viral')`)
	exec(t, st, `INSERT INTO playbook(id,agent_id,text,status,n,created_at) VALUES(3,'alpha','the plan','testing',0,'2026-11-01T00:00:00Z')`)
	exec(t, st, `INSERT INTO action_hypotheses(agent_id,action_id,hypothesis_id,revision) VALUES('alpha',1,3,1)`)
	exec(t, st, `INSERT INTO metrics(agent_id,action_id,measured_at,views,quotes,followers) VALUES('alpha',1,'2026-11-12T00:00:00Z',10,2,57)`)
	exec(t, st, `INSERT INTO ledger_phases(agent_id,day,phase,tokens_in,tokens_out,cost_usd) VALUES('alpha','2026-11-12','agent',900,100,0.5),('alpha','2026-10-01','dream',1,1,2)`)

	public, err := st.DashboardSnapshot(ctx, snapshotQuery(false))
	if err != nil {
		t.Fatal(err)
	}
	p := public.Publications[0]
	if p.Label != "viral" || p.ReachLift != 0.5 || p.ResonanceLift != -0.25 || p.FollowersAtPost == nil || *p.FollowersAtPost != 50 || p.Text != "the body" || len(p.Why) != 0 {
		t.Fatalf("public publication: %+v", p)
	}
	if m := public.Metrics[0]; m.Quotes != 2 || m.Followers == nil || *m.Followers != 57 {
		t.Fatalf("metric: %+v", m)
	}
	if public.SpendTotal != 2.5 || len(public.Phases) != 1 || public.Phases[0].TokensIn != 900 || public.Phases[0].TokensOut != 100 {
		t.Fatalf("spend: total %v phases %+v", public.SpendTotal, public.Phases)
	}
	content, err := st.DashboardSnapshot(ctx, snapshotQuery(true))
	if err != nil {
		t.Fatal(err)
	}
	if p = content.Publications[0]; p.Text != "the body" || len(p.Why) != 1 || p.Why[0] != "the plan" {
		t.Fatalf("content publication: %+v", p)
	}
}

func TestBestOwnPicksLivePostsThatDidWell(t *testing.T) {
	st := snapshotStore(t)
	for i, row := range []struct {
		dry         int
		label       string
		lift        float64
		posted, txt string
	}{
		{0, "ordinary", 0.2, "2026-11-10T00:00:00Z", "ok"},
		{0, "viral", 0.1, "2026-11-10T00:00:00Z", "viral"},
		{1, "viral", 9, "2026-11-10T00:00:00Z", "dry"},
		{0, "viral", 9, "2026-09-01T00:00:00Z", "old"},
		{0, "flop", 5, "2026-11-10T00:00:00Z", "flop"},
	} {
		id := i + 1
		exec(t, st, `INSERT INTO actions(id,agent_id,kind,text,dry_run,posted_at) VALUES(?,'alpha','post',?,?,?)`, id, row.txt, row.dry, row.posted)
		exec(t, st, `INSERT INTO action_outcomes(agent_id,action_id,measured_at,model_version,views,conversation_rate,applause_rate,reach_lift,resonance_lift,label) VALUES('alpha',?,'2026-11-12T00:00:00Z',1,10,0,0,?,0,?)`, id, row.lift, row.label)
	}
	got, err := st.BestOwn(context.Background(), "alpha", "post", time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC))
	if err != nil || len(got) != 2 || got[0] != "viral" || got[1] != "ok" {
		t.Fatalf("%v %v", got, err)
	}
}
