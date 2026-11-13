package store_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
)

func TestSeriesPartsAreReservedWithTheActionAndConfirmedOnce(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err = st.DB().Exec(`INSERT INTO agents(id,status) VALUES('a','running')`); err != nil {
		t.Fatal(err)
	}
	var id int64
	err = st.Reserve(ctx, func(tx store.ReservationTx) error {
		id, err = tx.InsertIntent(ctx, store.NewIntent{AgentID: "a", Kind: "post", Text: "một", Hook: "number", Rationale: "r", Parts: []string{"hai", "ba"}, HypothesisIDsJSON: "[]", At: time.Now()})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	err = st.Reserve(ctx, func(tx store.ReservationTx) error {
		rows, err := tx.Actions(ctx, false)
		for _, r := range rows {
			texts = append(texts, r.Text)
		}
		return err
	})
	if err != nil || len(texts) != 1 || texts[0] != "một\nhai\nba" {
		t.Fatalf("texts=%q err=%v, want every part in the reserved text", texts, err)
	}
	if ok, err := st.ConfirmPart(ctx, "a", id, 2, "https://x/2", "m2"); err != nil || !ok {
		t.Fatalf("first confirm ok=%v err=%v", ok, err)
	}
	if ok, _ := st.ConfirmPart(ctx, "a", id, 2, "https://x/other", "m9"); ok {
		t.Fatal("a confirmed part must not be overwritten")
	}
	if ok, _ := st.ConfirmPart(ctx, "other", id, 3, "https://x/3", "m3"); ok {
		t.Fatal("another agent confirmed a part")
	}
	var published int
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM action_parts WHERE thread_url<>''`).Scan(&published); err != nil || published != 1 {
		t.Fatalf("published=%d err=%v", published, err)
	}
	own, err := st.RecentOwnActions(ctx, store.ActionScope{AgentID: "a"}, time.Now().Add(-time.Hour), 10)
	if err != nil || len(own) != 1 || own[0].Hook != "number" || own[0].Parts != 2 || strings.Contains(own[0].Text, "hai") {
		t.Fatalf("own=%+v err=%v", own, err)
	}
}
