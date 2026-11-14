package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
)

// A refusal is cleared in the transaction of the reservation that succeeded:
// when that transaction rolls back, the row keeps the refusal.
func TestClearBlockedRunsInTheReservationTransaction(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err = st.DB().Exec(`INSERT INTO agents(id,status) VALUES('a','running')`); err != nil {
		t.Fatal(err)
	}
	audit, err := st.RecordToolAttempt(ctx, "a", 0, "post", "{}", "min_seconds_between_actions", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	blockedBy := func() (v string) {
		if err := st.DB().QueryRow(`SELECT blocked_by FROM tool_calls WHERE id=?`, audit).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	boom := errors.New("boom")
	err = st.Reserve(ctx, func(tx store.ReservationTx) error {
		if _, err := tx.InsertIntent(ctx, store.NewIntent{AgentID: "a", Kind: "post", Text: "x", At: time.Now()}); err != nil {
			return err
		}
		if err := tx.ClearBlocked(ctx, audit); err != nil {
			return err
		}
		return boom
	})
	var actions int
	st.DB().QueryRow(`SELECT COUNT(*) FROM actions`).Scan(&actions)
	if !errors.Is(err, boom) || actions != 0 || blockedBy() != "min_seconds_between_actions" {
		t.Fatalf("rollback: err %v actions %d blocked_by %q", err, actions, blockedBy())
	}
	err = st.Reserve(ctx, func(tx store.ReservationTx) error {
		if _, err := tx.InsertIntent(ctx, store.NewIntent{AgentID: "a", Kind: "post", Text: "x", At: time.Now()}); err != nil {
			return err
		}
		return tx.ClearBlocked(ctx, audit)
	})
	if err != nil || blockedBy() != "" {
		t.Fatalf("commit: err %v blocked_by %q", err, blockedBy())
	}
}
