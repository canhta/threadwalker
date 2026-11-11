package store_test

import (
	"context"
	"github.com/canhta/threadwalker/internal/store"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOwnerOutboxCorrelationSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "owner.db")
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 18, 0, 0, 0, time.UTC)
	if err = st.EnsureAgent(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	id, err := st.QueueOwner(ctx, "one", "question", "Which topic?", "question:1", now)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := st.QueueOwner(ctx, "one", "question", "Which topic?", "question:1", now)
	if err != nil || duplicate != id {
		t.Fatalf("dedupe: %d %v", duplicate, err)
	}
	pending, err := st.PendingOwner(ctx, []string{"one"})
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending %v %v", pending, err)
	}
	if ok, err := st.ClaimOwner(ctx, id); err != nil || !ok {
		t.Fatal(err)
	}
	if err = st.FinishOwner(ctx, id, 41, true); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ok, err := st.AnswerOwner(ctx, []string{"one"}, 41, "reply:9", "Build tooling.", now)
	if err != nil || !ok {
		t.Fatalf("answer %v %v", ok, err)
	}
	ok, err = st.AnswerOwner(ctx, []string{"one"}, 41, "reply:9", "Build tooling.", now)
	if err != nil || ok {
		t.Fatalf("duplicate %v %v", ok, err)
	}
	answers, err := st.OwnerAnswers(ctx, "one", 10)
	if err != nil || len(answers) != 1 || answers[0].Text != "Build tooling." || answers[0].AnsweredID != id {
		t.Fatalf("answers %v %v", answers, err)
	}
	other, err := st.OwnerAnswers(ctx, "two", 10)
	if err != nil || len(other) != 0 {
		t.Fatalf("isolation %v %v", other, err)
	}
}

func TestOwnerControlsDedupeAndTerminalKill(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.EnsureAgent(ctx, "one")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ command, key, status string }{{"pause", "a", "paused"}, {"resume", "b", "running"}, {"pause", "a", "running"}, {"kill", "c", "killed"}, {"resume", "d", "killed"}} {
		if err = st.OwnerControl(ctx, "one", tc.command, tc.key, "receipt", now); err != nil {
			t.Fatal(err)
		}
		state, _ := st.AgentState(ctx, "one")
		if state.Status != tc.status {
			t.Fatalf("%s: %s", tc.command, state.Status)
		}
	}
	for _, n := range []int64{10, 4, 11} {
		if err = st.SetOwnerOffset(ctx, "one", "fake", n); err != nil {
			t.Fatal(err)
		}
	}
	n, err := st.OwnerOffset(ctx, "one", "fake")
	if err != nil || n != 11 {
		t.Fatalf("offset %d %v", n, err)
	}
	if n, err = st.OwnerOffset(ctx, "one", "other"); err != nil || n != 0 {
		t.Fatalf("unknown bot %d %v", n, err)
	}
}

func TestOwnerUnknownAnswersAndUncertainDeliveryAreNeverReplayed(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.EnsureAgent(ctx, "one")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, question := range []int64{0, 999} {
		ok, err := st.AnswerOwner(ctx, []string{"one"}, question, "key", "unmatched", now)
		if err != nil || ok {
			t.Fatalf("unknown %v %v", ok, err)
		}
	}
	id, err := st.QueueOwner(ctx, "one", "notification", "Synthetic.", "", now)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		ok, err := st.ClaimOwner(ctx, id)
		if err != nil || ok != (i == 0) {
			t.Fatalf("claim %v %v", ok, err)
		}
	}
	pending, err := st.PendingOwner(ctx, []string{"one"})
	if err != nil || len(pending) != 0 {
		t.Fatal("sending item replayed")
	}
	if err = st.FinishOwner(ctx, id, 0, false); err != nil {
		t.Fatal(err)
	}
	pending, err = st.PendingOwner(ctx, []string{"one"})
	if err != nil || len(pending) != 0 {
		t.Fatal("failed item replayed")
	}
	if _, err = st.QueueOwner(ctx, "missing", "notification", "Synthetic.", "", now); err == nil {
		t.Fatal("unknown agent")
	}
	for _, text := range []string{"", strings.Repeat("x", 12001)} {
		if _, err = st.QueueOwner(ctx, "one", "notification", text, "", now); err == nil {
			t.Fatal("invalid text")
		}
	}
}
