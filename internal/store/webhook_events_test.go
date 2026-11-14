package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// keep is a settle function that leaves the time as it is.
func keep(at time.Time) (time.Time, error) { return at, nil }

func TestWakeEarlySettlesTheTimeAfterTheMinimumGap(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.EnsureAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err = st.SetNextWake(ctx, "a", at.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	later := func(t time.Time) (time.Time, error) { return t.Add(2 * time.Hour), nil }
	if err = st.WakeEarly(ctx, "a", at, time.Hour, later); err != nil {
		t.Fatal(err)
	}
	if s, err := st.AgentState(ctx, "a"); err != nil || !s.NextWake.Equal(at.Add(2*time.Hour)) {
		t.Fatalf("settled wake: %v err=%v", s.NextWake, err)
	}
}

func TestWebhookEventDedupesAndWakeEarlyKeepsTheMinimumGap(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.EnsureAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if fresh, err := st.RecordWebhookEvent(ctx, "a", "replies", "r1", at); err != nil || !fresh {
		t.Fatalf("first: fresh=%v err=%v", fresh, err)
	}
	if fresh, err := st.RecordWebhookEvent(ctx, "a", "replies", "r1", at); err != nil || fresh {
		t.Fatalf("redelivery: fresh=%v err=%v", fresh, err)
	}
	if fresh, _ := st.RecordWebhookEvent(ctx, "a", "mentions", "r1", at); !fresh {
		t.Fatal("same id under another field is a new event")
	}

	next := func() time.Time {
		t.Helper()
		s, err := st.AgentState(ctx, "a")
		if err != nil {
			t.Fatal(err)
		}
		return s.NextWake
	}
	if err = st.SetNextWake(ctx, "a", at.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = st.WakeEarly(ctx, "a", at, time.Hour, keep); err != nil || !next().Equal(at) {
		t.Fatalf("no prior wake: next=%v err=%v", next(), err)
	}
	id, err := st.BeginWake(ctx, "a", at.Add(-10*time.Minute), at.Add(5*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err = st.FinishWake(ctx, id, at.Add(-5*time.Minute), 1, "done"); err != nil {
		t.Fatal(err)
	}
	if err = st.WakeEarly(ctx, "a", at, time.Hour, keep); err != nil || !next().Equal(at.Add(50*time.Minute)) {
		t.Fatalf("gap not kept: next=%v err=%v", next(), err)
	}
	if err = st.WakeEarly(ctx, "a", at.Add(3*time.Hour), time.Hour, keep); err != nil || !next().Equal(at.Add(50*time.Minute)) {
		t.Fatalf("an earlier wake was postponed: next=%v err=%v", next(), err)
	}
}

func TestWakeOnWebhookFiresOnlyForAWebhookSinceTheLatestWake(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, id := range []string{"a", "b"} {
		if err = st.EnsureAgent(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(5 * time.Hour)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO actions(id,agent_id,kind,text,posted_at) VALUES(1,'a','post','x','2026-01-01T00:00:00Z'),(2,'b','post','y','2026-01-01T00:00:00Z')`)
	next := func() time.Time {
		t.Helper()
		s, err := st.AgentState(ctx, "a")
		if err != nil {
			t.Fatal(err)
		}
		return s.NextWake
	}
	check := func(want time.Time, why string) {
		t.Helper()
		if err := st.SetNextWake(ctx, "a", deadline); err != nil {
			t.Fatal(err)
		}
		if err := st.WakeOnWebhook(ctx, "a", now, time.Hour, keep); err != nil || !next().Equal(want) {
			t.Fatalf("%s: next=%v err=%v", why, next(), err)
		}
	}

	check(deadline, "no webhook")
	if _, err = st.RecordWebhookEvent(ctx, "a", "replies", "r1", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	check(now, "webhook reply")

	if _, err = st.BeginWake(ctx, "a", now, deadline); err != nil {
		t.Fatal(err)
	}
	check(deadline, "the webhook predates the new wake")
}
