package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestRevertStrategyAppendsOldText(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "revert.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.EnsureAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if _, err = st.RevertStrategy(ctx, "a", 0, at); !errors.Is(err, ErrStrategyInput) {
		t.Fatalf("empty history: %v", err)
	}
	if _, err = st.db.ExecContext(ctx, `INSERT INTO notes(agent_id,text,created_at) VALUES('a','n','`+formatTime(at)+`')`); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"one", "two", "three"} {
		if _, err = st.WriteStrategy(ctx, "a", text, []string{"note:1"}, at); err != nil {
			t.Fatal(err)
		}
	}
	if v, err := st.RevertStrategy(ctx, "a", 0, at); err != nil || v != 4 {
		t.Fatalf("revert previous: v=%d err=%v", v, err)
	}
	if v, err := st.RevertStrategy(ctx, "a", 1, at); err != nil || v != 5 {
		t.Fatalf("revert to 1: v=%d err=%v", v, err)
	}
	if _, err = st.RevertStrategy(ctx, "a", 5, at); !errors.Is(err, ErrStrategyInput) {
		t.Fatalf("revert to newest: %v", err)
	}
	history, err := st.StrategyHistory(ctx, "a")
	if err != nil || len(history) != 5 || history[3].Text != "two" || history[4].Text != "one" || history[4].Citations[0] != "note:1" {
		t.Fatalf("history=%+v err=%v", history, err)
	}
}

func TestOwnerSummaryFailuresAndSilence(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "fail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.EnsureAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	ts := func(h int) string { return formatTime(day.Add(time.Duration(h) * time.Hour)) }
	if since, reason, err := st.OwnerSilence(ctx, "a", false); err != nil || !since.IsZero() || reason != "" {
		t.Fatalf("empty: %v %q %v", since, reason, err)
	}
	for i, reason := range []string{"completed", "step_limit", "step_limit", "provider_error", "already_ran"} {
		if _, err = st.db.ExecContext(ctx, `INSERT INTO wakes(agent_id,started_at,ended_at,end_reason) VALUES('a',?,?,?)`, ts(i+1), ts(i+1), reason); err != nil {
			t.Fatal(err)
		}
	}
	for _, blocked := range []string{"blocked_phrase", "blocked_phrase", "critic_rejected", "", "quota"} {
		if _, err = st.db.ExecContext(ctx, `INSERT INTO tool_calls(agent_id,wake_id,tool,args_json,blocked_by,at) VALUES('a',1,'post','{}',?,?)`, blocked, ts(2)); err != nil {
			t.Fatal(err)
		}
	}
	sum, err := st.OwnerSummary(ctx, "a", false, day, day.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	want := []Refusal{{"blocked_phrase", 2}, {"step_limit", 2}, {"critic_rejected", 1}}
	if sum.FailedWakes != 3 || len(sum.Refusals) != 3 || sum.Refusals[0] != want[0] || sum.Refusals[1] != want[1] || sum.Refusals[2] != want[2] {
		t.Fatalf("summary=%+v", sum)
	}
	since, reason, err := st.OwnerSilence(ctx, "a", false)
	if err != nil || !since.Equal(day.Add(time.Hour)) || reason != "already_ran" {
		t.Fatalf("no publish: %v %q %v", since, reason, err)
	}
	if _, err = st.db.ExecContext(ctx, `INSERT INTO actions(agent_id,kind,text,dry_run,posted_at) VALUES('a','post','x',0,?)`, ts(9)); err != nil {
		t.Fatal(err)
	}
	if since, _, err = st.OwnerSilence(ctx, "a", false); err != nil || !since.Equal(day.Add(9*time.Hour)) {
		t.Fatalf("published: %v %v", since, err)
	}
}
