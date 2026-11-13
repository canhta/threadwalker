package store

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
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
	if sum.FailedWakes != 3 || !slices.Equal(sum.FailReasons, []Refusal{{"step_limit", 2}, {"provider_error", 1}}) || !slices.Equal(sum.Blocked, []Refusal{{"blocked_phrase", 2}, {"critic_rejected", 1}, {"quota", 1}}) {
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

func TestOwnerSummaryTopPostAndFollowers(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "top.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.EnsureAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	sum, err := st.OwnerSummary(ctx, "a", false, day, day.Add(24*time.Hour))
	if err != nil || sum.Top.ActionID != 0 || sum.Followers != nil || sum.FollowersBefore != nil {
		t.Fatalf("empty: %+v %v", sum, err)
	}
	ts := func(h int) string { return formatTime(day.Add(time.Duration(h) * time.Hour)) }
	for _, a := range []struct {
		kind, url string
		dry       int
	}{{"post", "https://example.test/p/1", 0}, {"post", "https://example.test/p/2", 0}, {"reply", "", 0}, {"post", "", 1}} {
		if _, err = st.db.ExecContext(ctx, `INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url) VALUES('a',?,'x',?,?,?)`, a.kind, a.dry, ts(-30), a.url); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []struct {
		action, hour, views, likes int
		followers                  any
	}{
		{1, -20, 50, 1, 100}, // before the day: the follower baseline
		{1, 3, 80, 4, nil},
		{2, 5, 300, 2, 112},  // the top post by views
		{2, 4, 999, 9, nil},  // superseded by its later sample
		{3, 6, 5000, 0, nil}, // a reply is never the top post
		{4, 6, 9000, 0, nil}, // nor is a dry-run post
	} {
		if _, err = st.db.ExecContext(ctx, `INSERT INTO metrics(agent_id,action_id,measured_at,views,likes,replies,reposts,quotes,followers) VALUES('a',?,?,?,?,1,0,1,?)`, m.action, ts(m.hour), m.views, m.likes, m.followers); err != nil {
			t.Fatal(err)
		}
	}
	sum, err = st.OwnerSummary(ctx, "a", false, day, day.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if sum.Top != (TopPost{ActionID: 2, URL: "https://example.test/p/2", Views: 300, Engagements: 4}) {
		t.Fatalf("top = %+v", sum.Top)
	}
	if sum.Followers == nil || *sum.Followers != 112 || sum.FollowersBefore == nil || *sum.FollowersBefore != 100 {
		t.Fatalf("followers = %v %v", sum.Followers, sum.FollowersBefore)
	}
}
