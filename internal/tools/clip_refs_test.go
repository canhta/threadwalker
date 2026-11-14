package tools_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/tools"
)

// A clipped result drops posts from what the model reads, but every post read
// is stored under its ref and the best of them are listed as open candidates.
func TestClippedFeedStillStoresEveryPostAndListsCandidates(t *testing.T) {
	st, opts, _ := setup(t)
	opts.Template.Targets = config.DefaultTargets
	opts.Template.Limits.BrowserRepliesPerDay = 10
	opts.Scope = guard.Scope{Store: st, Template: opts.Template, WakeID: 1, Now: opts.Now}
	m := threads.NewMemory()
	var posts []threads.Post
	for i := 0; i < 60; i++ {
		likes, replies, age := int64(200), int64(3), int64(30)
		posts = append(posts, threads.Post{Username: "u", Text: strings.Repeat("chữ ", 60) + fmt.Sprint(i), URL: fmt.Sprintf("https://www.threads.com/@u/post/p%d", i), Likes: &likes, Replies: &replies, AgeMinutes: &age})
	}
	m.Pages[threads.OpReadFeed] = threads.Page{Kind: "feed", Posts: posts}
	opts.Threads = m
	d, err := tools.NewDispatcher(opts)
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.Dispatch(context.Background(), call("find_posts", `{"source":"feed"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "clipped_lists") {
		t.Fatal("the feed was not clipped; the test needs a larger result")
	}
	stored, err := st.SeenPostsSince(context.Background(), "agent", opts.Now().Add(-guard.CandidateWindow))
	if err != nil || len(stored) != 60 {
		t.Fatalf("stored %d posts, %v; want all 60", len(stored), err)
	}
	open, err := guard.OpenCandidates(context.Background(), st, opts.Template, nil, opts.Now())
	if err != nil || len(open) != guard.MaxCandidates {
		t.Fatalf("%d open candidates, %v; want %d", len(open), err, guard.MaxCandidates)
	}
}
