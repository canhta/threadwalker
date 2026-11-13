package store

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStrategyRevisionsCiteEvidenceAndMeasureLift(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "strategy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, id := range []string{"a", "b"} {
		if err = st.EnsureAgent(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	day := func(d int) time.Time { return time.Date(2026, 11, d, 12, 0, 0, 0, time.UTC) }
	action := func(agent string, d int, views, likes int64) int64 {
		at := formatTime(day(d))
		res, e := st.db.ExecContext(ctx, `INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url) VALUES(?,'post','x',0,?,'https://threads.net/@a/post/1')`, agent, at)
		if e != nil {
			t.Fatal(e)
		}
		id, _ := res.LastInsertId()
		done := formatTime(day(d).Add(25 * time.Hour))
		if _, e = st.db.ExecContext(ctx, `INSERT INTO metric_jobs(agent_id,action_id,window_hours,due_at,not_before,completed_at) VALUES(?,?,24,?,?,?)`, agent, id, done, done, done); e != nil {
			t.Fatal(e)
		}
		if _, e = st.db.ExecContext(ctx, `INSERT INTO metrics(agent_id,action_id,measured_at,views,likes) VALUES(?,?,?,?,?)`, agent, id, done, views, likes); e != nil {
			t.Fatal(e)
		}
		lift := float64(likes) / 10
		if _, e = st.db.ExecContext(ctx, `INSERT INTO action_outcomes(agent_id,action_id,measured_at,model_version,views,conversation_rate,applause_rate,reach_lift,resonance_lift,label) VALUES(?,?,?,1,?,0,0,?,?,'ordinary')`, agent, id, done, views, lift, lift); e != nil {
			t.Fatal(e)
		}
		return id
	}
	first := action("a", 1, 100, 10)
	action("a", 2, 100, 10)
	foreign := action("b", 1, 100, 10)
	action("a", 4, 100, 20)
	action("a", 6, 100, 30)
	action("a", 7, 100, 50)

	for name, cites := range map[string][]string{
		"none":      nil,
		"malformed": {"first"},
		"unknown":   {"action:999"},
		"foreign":   {"action:" + itoa(foreign)},
		"too many":  {"action:1", "action:1", "action:1", "action:1", "action:1", "action:1", "action:1", "action:1", "action:1"},
	} {
		if _, err = st.WriteStrategy(ctx, "a", "voice", cites, day(3)); !errors.Is(err, ErrStrategyInput) {
			t.Errorf("%s citation: err = %v, want ErrStrategyInput", name, err)
		}
	}
	for _, text := range []string{" ", strings.Repeat("x", MaxStrategyRunes+1)} {
		if _, err = st.WriteStrategy(ctx, "a", text, []string{"action:" + itoa(first)}, day(3)); !errors.Is(err, ErrStrategyInput) {
			t.Errorf("text length %d: err = %v, want ErrStrategyInput", len(text), err)
		}
	}
	if history, e := st.StrategyHistory(ctx, "a"); e != nil || len(history) != 0 {
		t.Fatalf("refused revisions were stored: %v %v", history, e)
	}

	if v, e := st.WriteStrategy(ctx, "a", "first", []string{"action:" + itoa(first)}, day(3)); e != nil || v != 1 {
		t.Fatalf("first revision = %d, %v", v, e)
	}
	if _, e := st.db.ExecContext(ctx, `INSERT INTO notes(agent_id,text,created_at) VALUES('a','n','x')`); e != nil {
		t.Fatal(e)
	}
	if v, e := st.WriteStrategy(ctx, "a", "second", []string{"note:1"}, day(5)); e != nil || v != 2 {
		t.Fatalf("second revision = %d, %v", v, e)
	}
	history, err := st.StrategyHistory(ctx, "a")
	if err != nil || len(history) != 2 || history[0].Text != "first" || history[1].Citations[0] != "note:1" {
		t.Fatalf("history = %+v, %v", history, err)
	}
	near := func(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }
	if l := history[0].Lift; l.BeforeN != 2 || l.AfterN != 1 || !near(l.BeforeMean, 1) || !near(l.AfterMean, 2) || !near(l.Change, 1) {
		t.Errorf("first lift = %+v, want 1 before, 2 after", l)
	}
	if l := history[1].Lift; l.BeforeN != 1 || l.AfterN != 2 || !near(l.BeforeMean, 2) || !near(l.AfterMean, 4) || !near(l.Change, 2) {
		t.Errorf("second lift = %+v, want 2 before, 4 after", l)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
