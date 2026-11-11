package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/store"
)

func TestCountActionsExactHalfOpenWindow(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "counts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	from := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	for _, a := range []struct {
		agent, kind string
		at          time.Time
		dry         int
	}{{"a", "post", from.Add(-time.Nanosecond), 1}, {"a", "post", from, 1}, {"a", "reply", to.Add(-time.Nanosecond), 0}, {"a", "post", to, 0}, {"b", "post", from, 1}} {
		if _, err := st.DB().Exec(`INSERT INTO actions(agent_id,kind,text,posted_at,dry_run) VALUES(?,?,'',?,?)`, a.agent, a.kind, a.at.Format(time.RFC3339Nano), a.dry); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.CountActions(ctx, "a", from, to)
	if err != nil || got.Posts != 1 || got.Replies != 1 {
		t.Fatalf("counts=%+v err=%v", got, err)
	}
	got, err = st.CountActions(ctx, "missing", from, to)
	if err != nil || got.Posts != 0 || got.Replies != 0 {
		t.Fatalf("empty=%+v err=%v", got, err)
	}
	if _, err = st.CountActions(ctx, "a", to, from); err == nil {
		t.Fatal("reversed interval accepted")
	}
}

func TestPermanentDoNotContactScopedAndNormalized(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "contacts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err = st.DB().Exec(`INSERT INTO agents(id,status) VALUES('a','running'),('b','paused')`); err != nil {
		t.Fatal(err)
	}
	if err = st.MarkDoNotContact(ctx, "a", " @Mixed.Name ", "stop_request"); err != nil {
		t.Fatal(err)
	}
	if err = st.MarkDoNotContact(ctx, "a", "mixed.name", "agent_request"); err != nil {
		t.Fatal(err)
	}
	var username, reason string
	var dnc int
	if err = st.DB().QueryRow(`SELECT username,do_not_contact,reason FROM contacts WHERE agent_id='a'`).Scan(&username, &dnc, &reason); err != nil || username != "mixed.name" || dnc != 1 || reason != "stop_request" {
		t.Fatalf("contact %q %d %q %v", username, dnc, reason, err)
	}
	var count int
	st.DB().QueryRow(`SELECT COUNT(*) FROM contacts WHERE agent_id='b'`).Scan(&count)
	if count != 0 {
		t.Fatal("cross-agent contact")
	}
	for _, tc := range []struct{ agent, user, reason string }{{"missing", "user", "owner_request"}, {"a", "bad name", "owner_request"}, {"a", "user", "private person description"}} {
		if err = st.MarkDoNotContact(ctx, tc.agent, tc.user, tc.reason); err == nil {
			t.Fatal("invalid contact accepted")
		}
	}
	if err = st.MarkDoNotContact(ctx, "b", "user", "owner_request"); err != nil {
		t.Fatal(err)
	}
}
