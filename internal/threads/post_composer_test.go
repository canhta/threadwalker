package threads

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agentbrowser"
)

const (
	composerImage  = `            - image "synthetic_own's profile picture"`
	composerExpand = `            - button "Expand composer" [ref=e519]`
)

// post-replies.ax.txt is a sanitized snapshot of a real post page with replies
// (root post with the inline composer, then the replies). A reply parses it.
func TestAgentBrowserReplyOnPostPageWithReplies(t *testing.T) {
	target := "https://www.threads.com/@synthetic_author/post/fixtureRealPost2"
	full := liveFixture(t, "post-replies.ax.txt")
	filled := strings.Replace(full, `[ref=e533]: `, `[ref=e533]: Synthetic reply.`, 1)
	filled = strings.Replace(filled, composerExpand, composerExpand+"\n"+`            - button "Reply" [ref=e590]`, 1)
	cli := &fakeAgentCLI{openURL: target, snapshots: []agentbrowser.Snapshot{
		{Origin: target, Text: full}, {Origin: target, Text: full}, {Origin: target, Text: filled}, {Origin: target, Text: filled},
	}}
	if _, err := testAgentBrowser(t, cli, nil).Reply(context.Background(), target, "Synthetic reply."); errors.Is(err, ErrMalformedPage) {
		t.Fatalf("post page with replies refused: %v", err)
	}
	if cli.clicks != 1 || len(cli.fills) != 1 {
		t.Fatalf("clicks=%d fills=%v", cli.clicks, cli.fills)
	}
}

// Threads renders the posts of a post page before its inline composer. A post
// read waits for the composer instead of returning a page a reply must refuse.
func TestAgentBrowserPostPageWaitsForComposer(t *testing.T) {
	target := "https://www.threads.com/@synthetic_author/post/fixtureRealPost2"
	full := liveFixture(t, "post-replies.ax.txt")
	start, end := strings.Index(full, composerImage), strings.Index(full, composerExpand)
	if start < 0 || end < start {
		t.Fatal("composer lines not found in fixture")
	}
	bare := full[:start] + full[end+len(composerExpand)+1:]
	cli := &fakeAgentCLI{openURL: target, snapshots: []agentbrowser.Snapshot{
		{Origin: target, Text: bare}, {Origin: target, Text: bare}, {Origin: target, Text: full},
	}}
	b := testAgentBrowser(t, cli, func(context.Context, time.Duration) error { return nil })
	page, err := b.driver.page(context.Background(), "post")
	if err != nil || page.Form == nil || !page.Form.Composer || len(page.Posts) < 2 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if cli.snapCalls != 3 {
		t.Fatalf("snapshots=%d, want 3", cli.snapCalls)
	}
}

// A post with a topic tag nests its timestamp permalink inside a generic, so the
// post block has no direct post link. The composer is still recognized.
func TestPostPageWithTopicTagHasComposer(t *testing.T) {
	text := liveFixture(t, "post-tagged.ax.txt")
	origin := "https://www.threads.com/@synthetic_author/post/fixtureTaggedPost"
	page, err := parseThreadsPage(text, origin, "post", "https://www.threads.com")
	if err != nil || page.Form == nil || !page.Form.Composer {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}
