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

// A post the clip dropped was never shown to the model, so it is never offered
// as an open target.
func TestOpenTargetsOfferOnlyRefsTheModelWasShown(t *testing.T) {
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
	out, err := d.Dispatch(context.Background(), call("read_feed", "{}"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "clipped_lists") {
		t.Fatal("the feed was not clipped; the test needs a larger result")
	}
	open := d.OpenTargets(context.Background())
	if len(open) == 0 {
		t.Fatal("no open targets at all")
	}
	for _, r := range open {
		if !strings.Contains(out, `"ref":"`+r+`"`) {
			t.Fatalf("open target %s was never shown to the model", r)
		}
	}
}
