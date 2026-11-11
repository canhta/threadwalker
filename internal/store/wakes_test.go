package store_test

import (
	"context"
	"database/sql"
	"github.com/canhta/threadwalker/internal/store"
	"path/filepath"
	"testing"
	"time"
)

func TestInterruptedWakeRecoveryAdvancesScheduleAndNeverReplays(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err = st.EnsureAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	id, err := st.BeginWake(ctx, "a", now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DB().ExecContext(ctx, `INSERT INTO tool_calls(agent_id,wake_id,tool,args_json,at) VALUES(?,?,?,?,?)`, "a", id, "post", "{}", now.Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := st.RecoverWakes(ctx, "a", now.Add(2*time.Hour), now.Add(3*time.Hour))
	if err != nil || len(recovered) != 1 {
		t.Fatalf("%v %v", recovered, err)
	}
	state, err := st.AgentState(ctx, "a")
	if err != nil || !state.NextWake.Equal(now.Add(3*time.Hour)) {
		t.Fatalf("%+v %v", state, err)
	}
	var reason, result string
	st.DB().QueryRow(`SELECT end_reason FROM wakes WHERE id=?`, id).Scan(&reason)
	st.DB().QueryRow(`SELECT result_json FROM tool_calls WHERE wake_id=?`, id).Scan(&result)
	if reason != "interrupted" || result != `{"outcome":"unknown_assumed_sent","retry":false}` {
		t.Fatalf("%s %s", reason, result)
	}
	again, err := st.RecoverWakes(ctx, "a", now.Add(2*time.Hour), now.Add(3*time.Hour))
	if err != nil || len(again) != 0 {
		t.Fatalf("%v %v", again, err)
	}
}

func TestWakeLifecyclePreservesStateAndRequiresRunningAgent(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "lifecycle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err = st.AgentState(ctx, "missing"); err == nil {
		t.Fatal("missing state accepted")
	}
	if _, err = st.BeginWake(ctx, "missing", at, at.Add(time.Hour)); err == nil {
		t.Fatal("missing agent accepted")
	}
	if err = st.EnsureAgent(ctx, ""); err == nil {
		t.Fatal("empty agent id accepted")
	}
	if err = st.EnsureAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"paused", "killed"} {
		st.DB().Exec(`UPDATE agents SET status=?,self_note='private note' WHERE id='a'`, status)
		if err = st.EnsureAgent(ctx, "a"); err != nil {
			t.Fatal(err)
		}
		state, err := st.AgentState(ctx, "a")
		if err != nil || state.Status != status || state.SelfNote != "private note" {
			t.Fatalf("%+v %v", state, err)
		}
		if _, err = st.BeginWake(ctx, "a", at, at.Add(time.Hour)); err == nil {
			t.Fatal("paused/killed wake accepted")
		}
	}
	st.DB().Exec(`UPDATE agents SET status='running' WHERE id='a'`)
	id, err := st.BeginWake(ctx, "a", at, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.BeginWake(ctx, "a", at, at.Add(time.Hour)); err == nil {
		t.Fatal("overlapping wake accepted")
	}
	if err = st.FinishWake(ctx, id, at.Add(time.Minute), 3, "step_limit"); err != nil {
		t.Fatal(err)
	}
	if err = st.FinishWake(ctx, id, at, 0, "completed"); err == nil {
		t.Fatal("finished wake rewritten")
	}
	var steps int
	var reason string
	st.DB().QueryRow(`SELECT steps,end_reason FROM wakes WHERE id=?`, id).Scan(&steps, &reason)
	if steps != 3 || reason != "step_limit" {
		t.Fatalf("steps=%d reason=%s", steps, reason)
	}
	if _, err = st.RecoverWakes(ctx, "a", at, at); err == nil {
		t.Fatal("non-future recovery accepted")
	}
	st.DB().Exec(`UPDATE agents SET next_wake_at='bad-timestamp' WHERE id='a'`)
	if _, err = st.AgentState(ctx, "a"); err == nil {
		t.Fatal("invalid timestamp accepted")
	}
}

func TestWakeRecoveryTransactionAndFutureSchedule(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "recovery.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st.EnsureAgent(ctx, "a")
	id, err := st.BeginWake(ctx, "a", now, now.Add(12*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	st.DB().Exec(`CREATE TRIGGER fail_recovery BEFORE UPDATE ON wakes BEGIN SELECT RAISE(ABORT,'synthetic');END`)
	if _, err = st.RecoverWakes(ctx, "a", now, now.Add(time.Hour)); err == nil {
		t.Fatal("failure ignored")
	}
	var ended sql.NullString
	st.DB().QueryRow(`SELECT ended_at FROM wakes WHERE id=?`, id).Scan(&ended)
	if ended.Valid {
		t.Fatal("partial recovery committed")
	}
	st.DB().Exec(`DROP TRIGGER fail_recovery`)
	if _, err = st.RecoverWakes(ctx, "a", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	state, err := st.AgentState(ctx, "a")
	if err != nil || !state.NextWake.Equal(now.Add(12*time.Hour)) {
		t.Fatalf("%+v %v", state, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err = st.EnsureAgent(cancelled, "b"); err == nil {
		t.Fatal("cancel ignored")
	}
	if _, err = st.BeginWake(cancelled, "a", now, now.Add(time.Hour)); err == nil {
		t.Fatal("cancel ignored")
	}
	if _, err = st.RecoverWakes(cancelled, "a", now, now.Add(time.Hour)); err == nil {
		t.Fatal("cancel ignored")
	}
	if err = st.FinishWake(cancelled, id, now, 0, "completed"); err == nil {
		t.Fatal("cancel ignored")
	}
}
