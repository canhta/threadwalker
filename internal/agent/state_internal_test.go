package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/tools"
)

func TestStopCheckNeverNudgesTowardAHiddenTool(t *testing.T) {
	open := func() []string { return []string{"p1", "p2"} }
	called := map[string]bool{"ideas": true}
	if ask := behindSB.stopCheck(func(tool string) bool { return tool == "reply" }, open, 5)(called, nil); ask != "" {
		t.Fatalf("hidden reply still asked for: %q", ask)
	}
	if ask := behindSB.stopCheck(func(tool string) bool { return tool == "ideas" }, open, 5)(nil, nil); strings.Contains(ask, "call ideas") || !strings.Contains(ask, "p1, p2") {
		t.Fatalf("ask %q", ask)
	}
}

// The stop check rebuilds the candidate list at the end of the wake. A post
// that only became a candidate meanwhile has no ref this wake issued, so it
// is not named.
func TestStopCheckNamesOnlyRefsThisWakeIssued(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "stop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tmpl := OfflineTemplate()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	wake, err := st.BeginWake(ctx, tmpl.ID, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	n := func(v int64) *int64 { return &v }
	note := func(slug string) string {
		id, err := st.NoteSeenPost(ctx, tmpl.ID, store.SeenPost{URL: "https://threads.com/@alice/post/" + slug, Author: "alice", MediaID: "api-" + slug, Text: slug, Likes: n(300), Replies: n(2), AgeMinutes: n(20), ReadAt: now})
		if err != nil {
			t.Fatal(err)
		}
		return store.PostRef(id)
	}
	handedOut, other := note("handed-out"), note("other")
	g, err := guard.New(guard.Scope{Store: st, Template: tmpl, WakeID: wake, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if err = g.IssueCandidates(ctx, []guard.Candidate{{Ref: handedOut}}); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{opts: Options{Store: st, Template: tmpl, Now: func() time.Time { return now }}}
	if got := r.openCandidates(ctx, g); len(got) != 1 || got[0] != handedOut {
		t.Fatalf("stop check names %v, want only %s (not %s)", got, handedOut, other)
	}
}

func TestNothingToDoWhenNoWriteToolIsUsable(t *testing.T) {
	if !(wakeState{}).nothingToDo() {
		t.Fatal("post and reply both closed, inbox or not: nothing to do")
	}
	if (wakeState{PostsLeft: 1}).nothingToDo() || (wakeState{RepliesLeft: 1}).nothingToDo() {
		t.Fatal("an open write tool leaves something to do")
	}
}

func TestNothingToDoIsNotAFailedWake(t *testing.T) {
	if r := wakeEndReason(tools.ErrNothingToDo); r != "nothing_to_do" || !nonFatalWakeEnd(r) {
		t.Fatalf("end reason %q", r)
	}
}
