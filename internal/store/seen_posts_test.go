package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
)

func i64(v int64) *int64 { return &v }

// threads.net, www. and threads.com name one post, so they share one address
// and one p ref.
func TestCanonicalThreadMapsEveryThreadsHostToOne(t *testing.T) {
	const want = "https://threads.com/@alice/post/abc"
	for _, raw := range []string{
		"https://threads.com/@alice/post/abc",
		"https://www.threads.com/@alice/post/abc/",
		"https://threads.net/@alice/post/abc?x=1#top",
		"http://www.threads.net/@alice/post/abc",
		"https://THREADS.NET/@alice/post/abc",
	} {
		if got, err := store.CanonicalThread(raw); err != nil || got != want {
			t.Errorf("%s: got %q, %v; want %q", raw, got, err, want)
		}
	}
	if got, err := store.CanonicalThread("https://www.example.com/a/"); err != nil || got != "https://example.com/a" {
		t.Errorf("other hosts only lose www.: %q, %v", got, err)
	}
}

func TestOnePostHasOnePRefAcrossThreadsHosts(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "hosts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, err := st.NoteSeenPost(ctx, "agent", store.SeenPost{URL: "https://threads.net/@alice/post/abc", Author: "alice", Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.NoteSeenPost(ctx, "agent", store.SeenPost{URL: "https://www.threads.com/@alice/post/abc", Author: "alice", Text: "x"})
	if err != nil || a != b {
		t.Fatalf("one post got ids %d and %d (err %v)", a, b, err)
	}
}

func TestSeenPostsKeepOneIdPerAgentAndURL(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "seen.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	first, err := s.NoteSeenPost(ctx, "a", store.SeenPost{URL: "https://www.threads.com/@x/post/1?xmt=abc", Author: "x", Text: "hello", Likes: i64(5), AgeMinutes: i64(20), ReadAt: at})
	if err != nil || first == 0 {
		t.Fatalf("first = %d, %v", first, err)
	}
	// The same post by another spelling, an hour later, now with its media id and no figures.
	again, err := s.NoteSeenPost(ctx, "a", store.SeenPost{URL: "https://threads.com/@x/post/1/", Author: "x", MediaID: "m1", ReadAt: at.Add(time.Hour)})
	if err != nil || again != first {
		t.Fatalf("same URL got id %d, %v; want %d", again, err, first)
	}
	other, _ := s.NoteSeenPost(ctx, "b", store.SeenPost{URL: "https://threads.com/@x/post/1", Author: "x", ReadAt: at})
	if other == first {
		t.Fatal("another agent shares the id")
	}
	p, ok, err := s.SeenPostByID(ctx, "a", first)
	if err != nil || !ok {
		t.Fatalf("lookup: %v %v", ok, err)
	}
	// Media id filled; text and figures survive a read that showed none; age and read time stay together.
	if p.MediaID != "m1" || p.Text != "hello" || p.Likes == nil || *p.Likes != 5 || *p.AgeMinutes != 20 || !p.ReadAt.Equal(at) || p.URL != "https://threads.com/@x/post/1" {
		t.Fatalf("row = %+v", p)
	}
	if _, ok, _ = s.SeenPostByID(ctx, "b", first); ok {
		t.Fatal("agent b read agent a's id")
	}
	// A read with an age refreshes the figures and the read time.
	if _, err = s.NoteSeenPost(ctx, "a", store.SeenPost{URL: "https://threads.com/@x/post/1", Author: "x", Likes: i64(9), AgeMinutes: i64(90), ReadAt: at.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	recent, err := s.SeenPostsSince(ctx, "a", at.Add(90*time.Minute))
	if err != nil || len(recent) != 1 || *recent[0].Likes != 9 || *recent[0].AgeMinutes != 90 {
		t.Fatalf("recent = %+v, %v", recent, err)
	}
	// A pruned row's id is never reused.
	if _, err = s.Prune(ctx, at.Add(30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	next, _ := s.NoteSeenPost(ctx, "a", store.SeenPost{URL: "https://threads.com/@y/post/2", Author: "y", ReadAt: at})
	if next <= other {
		t.Fatalf("id %d reused after prune (last %d)", next, other)
	}
}
