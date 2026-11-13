package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestKPIStatsCountsOwnRecentActionsAndSpend(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 1, 8, 12, 0, 0, 0, time.UTC)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Action 1: 2h old, two measurements (only the newest counts). 2: 3 days old (outside 24h, inside 7d).
	// 3: another agent. 4: failed. 5: dry-run.
	exec(`INSERT INTO actions(id,agent_id,kind,text,dry_run,failed,posted_at) VALUES
 (1,'a','post','x',0,0,'2026-01-08T10:00:00Z'),(2,'a','reply','y',0,0,'2026-01-05T12:00:00Z'),
 (3,'b','post','z',0,0,'2026-01-08T10:00:00Z'),(4,'a','post','f',0,1,'2026-01-08T11:00:00Z'),(5,'a','post','d',1,0,'2026-01-08T11:30:00Z')`)
	exec(`INSERT INTO metrics(agent_id,action_id,measured_at,likes,replies,reposts,quotes,followers) VALUES
 ('a',1,'2026-01-08T10:30:00Z',9,9,9,9,9),('a',1,'2026-01-08T11:30:00Z',3,2,1,1,2),
 ('a',2,'2026-01-08T11:00:00Z',5,0,0,NULL,1),('b',3,'2026-01-08T11:00:00Z',50,0,0,0,50),('a',4,'2026-01-08T11:30:00Z',40,0,0,0,40)`)
	exec(`INSERT INTO ledger_phases(agent_id,day,phase,tokens_in,tokens_out,cost_usd) VALUES
 ('a','2026-01-08','agent',100,50,0.10),('a','2026-01-08','critic',10,5,0.02),('a','2026-01-01','agent',1000,0,9),('a','2026-01-03','agent',200,100,0.50),('b','2026-01-08','agent',1,1,5)`)
	exec(`INSERT INTO wakes(agent_id,started_at,ended_at,kind) VALUES
 ('a','2026-01-08T01:00:00Z','2026-01-08T01:01:00Z','agent'),('a','2026-01-08T06:00:00Z','2026-01-08T06:01:00Z','agent'),
 ('a','2026-01-08T07:00:00Z','2026-01-08T07:01:00Z','dream'),('a','2026-01-07T23:00:00Z','2026-01-07T23:01:00Z','agent')`)

	got, err := st.KPIStats(ctx, "a", false, now)
	if err != nil {
		t.Fatal(err)
	}
	want := KPIStats{Engagements: 7, Followers: 3, LastPublish: time.Date(2026, 1, 8, 10, 0, 0, 0, time.UTC), SpendToday: 0.12, TokensToday: 165, Spend7d: 0.62, Tokens7d: 465, WakesToday: 2}
	if got.Engagements != want.Engagements || got.Followers != want.Followers || !got.LastPublish.Equal(want.LastPublish) || got.TokensToday != want.TokensToday || got.Tokens7d != want.Tokens7d || got.WakesToday != want.WakesToday ||
		abs(got.SpendToday-want.SpendToday) > 1e-9 || abs(got.Spend7d-want.Spend7d) > 1e-9 {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	dry, err := st.KPIStats(ctx, "a", true, now)
	if err != nil || dry.Engagements != 0 || !dry.LastPublish.Equal(time.Date(2026, 1, 8, 11, 30, 0, 0, time.UTC)) {
		t.Fatalf("dry-run stats %+v err %v", dry, err)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
