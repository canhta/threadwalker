package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestPublishedConfirmationAndMetricsReadiness(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "actions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.EnsureAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	seed := func(dry int) int64 {
		res, e := st.db.ExecContext(ctx, `INSERT INTO actions(agent_id,kind,text,dry_run,posted_at) VALUES('a','post','x',?,?)`, dry, formatTime(at))
		if e != nil {
			t.Fatal(e)
		}
		id, _ := res.LastInsertId()
		return id
	}
	live, dry := seed(0), seed(1)

	if _, ok, e := st.ConfirmedAction(ctx, "a", live); e != nil || ok {
		t.Fatalf("unconfirmed ok=%v err=%v", ok, e)
	}
	if ok, e := st.ConfirmPublished(ctx, "a", dry, "https://x.invalid/1", "m1"); e != nil || ok {
		t.Fatalf("dry-run confirmed ok=%v err=%v", ok, e)
	}
	if ok, e := st.ConfirmPublished(ctx, "other", live, "https://x.invalid/1", "m1"); e != nil || ok {
		t.Fatalf("foreign confirmed ok=%v err=%v", ok, e)
	}
	if ok, e := st.ConfirmPublished(ctx, "a", live, "https://x.invalid/1", "m1"); e != nil || !ok {
		t.Fatalf("confirm ok=%v err=%v", ok, e)
	}
	if ok, e := st.ConfirmPublished(ctx, "a", live, "https://x.invalid/2", "m2"); e != nil || ok {
		t.Fatalf("reconfirmed ok=%v err=%v", ok, e)
	}
	if c, ok, e := st.ConfirmedAction(ctx, "a", live); e != nil || !ok || c.MediaID != "m1" || c.Kind != "post" {
		t.Fatalf("action=%+v ok=%v err=%v", c, ok, e)
	}
	if _, ok, e := st.ConfirmedAction(ctx, "a", dry); e != nil || ok {
		t.Fatalf("dry ok=%v err=%v", ok, e)
	}

	if ready, e := st.MetricsWakeReady(ctx, "a"); e != nil || !ready {
		t.Fatalf("ready=%v err=%v", ready, e)
	}
	wake, err := st.BeginWake(ctx, "a", at, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if ready, e := st.MetricsWakeReady(ctx, "a"); e != nil || ready {
		t.Fatalf("open wake ready=%v err=%v", ready, e)
	}
	if err = st.FinishWake(ctx, wake, at, 0, "completed"); err != nil {
		t.Fatal(err)
	}
	if ready, e := st.MetricsWakeReady(ctx, "missing"); e != nil || ready {
		t.Fatalf("unknown agent ready=%v err=%v", ready, e)
	}
}

func TestUnpublishedPostStopsCounting(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "unpublished.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.EnsureAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	res, err := st.db.ExecContext(ctx, `INSERT INTO actions(agent_id,kind,text,dry_run,posted_at) VALUES('a','post','never sent',0,?)`, formatTime(at))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	count := func() int {
		c, e := st.CountActions(ctx, "a", false, at.Add(-time.Hour), at.Add(time.Hour))
		if e != nil {
			t.Fatal(e)
		}
		return c.Posts
	}
	if pending, e := st.UnpublishedPosts(ctx, "a"); e != nil || len(pending) != 1 || count() != 1 {
		t.Fatalf("pending=%v err=%v count=%d", pending, e, count())
	}
	if err = st.MarkActionFailed(ctx, "a", id); err != nil {
		t.Fatal(err)
	}
	if pending, _ := st.UnpublishedPosts(ctx, "a"); len(pending) != 0 || count() != 0 {
		t.Fatalf("failed action still counted: pending=%v count=%d", pending, count())
	}
}
