package guard_test

import (
	"context"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/store"
)

func TestOpenCandidatesAreRankedAgedAndFiltered(t *testing.T) {
	ctx := context.Background()
	st, opts := setup(t)
	tmpl := opts.Template
	tmpl.Targets = config.DefaultTargets
	tmpl.Threads.Username = "me"
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	n := func(v int64) *int64 { return &v }
	note := func(slug, author string, likes, replies, age int64, readAgo time.Duration) string {
		id, err := st.NoteSeenPost(ctx, tmpl.ID, store.SeenPost{URL: "https://threads.com/@" + author + "/post/" + slug, Author: author, Text: "text " + slug, Likes: n(likes), Replies: n(replies), AgeMinutes: n(age), ReadAt: now.Add(-readAgo)})
		if err != nil {
			t.Fatal(err)
		}
		return "p" + string(rune('0'+id))
	}
	best := note("best", "alice", 300, 2, 20, 10*time.Minute) // young and fast
	okay := note("okay", "bob", 60, 2, 60, 10*time.Minute)    // fine, scores lower
	note("old", "carol", 300, 2, 200, 3*time.Hour)            // 200 + 180 minutes: past the window
	note("cold", "dave", 1, 0, 30, 10*time.Minute)            // too few likes
	note("old-read", "erin", 300, 2, 20, 7*time.Hour)         // read before the 6h window
	note("mine", "me", 300, 2, 20, 10*time.Minute)            // the account's own post
	note("blocked", "frank", 300, 2, 20, 10*time.Minute)      // do not contact
	note("answered", "gina", 300, 2, 20, 10*time.Minute)      // already replied to
	if err := st.MarkDoNotContact(ctx, tmpl.ID, "frank", "owner_request"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,target_url) VALUES(?,'reply','x',?,?,?)`, tmpl.ID, tmpl.DryRun, now.Add(-time.Hour).Format(time.RFC3339Nano), "https://www.threads.com/@gina/post/answered?x=1"); err != nil {
		t.Fatal(err)
	}
	got, err := guard.OpenCandidates(ctx, st, tmpl, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Ref != best || got[1].Ref != okay {
		t.Fatalf("candidates %+v, want %s then %s", got, best, okay)
	}
	if got[0].AgeMinutes != 30 || got[0].Author != "alice" || got[0].Text != "text best" {
		t.Fatalf("first candidate %+v: its age is the age at read plus the time since", got[0])
	}
	// The list is capped.
	for i := 0; i < 2*guard.MaxCandidates; i++ {
		note("bulk"+string(rune('a'+i)), "user"+string(rune('a'+i)), 300, 2, 20, time.Minute)
	}
	if got, err = guard.OpenCandidates(ctx, st, tmpl, nil, now); err != nil || len(got) != guard.MaxCandidates {
		t.Fatalf("%d candidates, %v; want %d", len(got), err, guard.MaxCandidates)
	}
}
