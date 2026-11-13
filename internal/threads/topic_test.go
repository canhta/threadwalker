package threads

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agentbrowser"
)

func TestThreadsLiveTrendingPanel(t *testing.T) {
	base := "https://www.threads.com"
	feed, err := parseThreadsPage(liveFixture(t, "home-trending.ax.txt"), base+"/", "feed", base)
	if err != nil || len(feed.Posts) != 1 || len(feed.Trends) != 2 {
		t.Fatalf("feed=%+v err=%v", feed, err)
	}
	want := Trend{Title: "Synthetic topic one", Summary: "Synthetic summary of topic one", Posts: "2K posts", URL: base + "/search?q=Synthetic%20one&serp_type=trends&trend_fbid=1000000000000001"}
	if got := feed.Trends[0]; got.PostCount == nil || *got.PostCount != 2000 {
		t.Fatalf("post count=%v", got.PostCount)
	} else if got.PostCount = nil; got != want {
		t.Fatalf("trend=%+v want %+v", feed.Trends[0], want)
	}
}

func TestThreadsLiveTopicPage(t *testing.T) {
	base := "https://www.threads.com"
	page, err := parseThreadsPage(liveFixture(t, "topic.ax.txt"), base+"/search?q=Synthetic%20one&serp_type=trends", "topic", base)
	if err != nil || page.Kind != "topic" || len(page.Posts) != 2 || len(page.Trends) != 0 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	first := page.Posts[0]
	if first.Username != "synthetic_viet" || first.URL != base+"/@synthetic_viet/post/fixtureTopic1" || first.ID != "" || first.Text != "Bài mẫu tổng hợp bằng tiếng Việt về chủ đề một" {
		t.Fatalf("first=%+v", first)
	}
	if _, err = parseThreadsPage(liveFixture(t, "topic.ax.txt"), base+"/", "topic", base); !errors.Is(err, ErrUnknownPage) {
		t.Fatalf("topic on a non-search page: %v", err)
	}
}

// A search page renders three posts, one with its author and permalink inside
// generic wrappers, and loads the rest while scrolling; the earlier posts are
// dropped once scrolled past. A read keeps every post it saw.
func TestBrowserReadTopicScrollsAndAccumulates(t *testing.T) {
	origin := "https://www.threads.com/search?q=x"
	snap := func(name string) agentbrowser.Snapshot {
		return agentbrowser.Snapshot{Origin: origin, Text: liveFixture(t, name)}
	}
	cli := &fakeAgentCLI{openURL: origin, snapshots: []agentbrowser.Snapshot{
		snap("search-page.ax.txt"), snap("search-page-scrolled.ax.txt"),
	}, snapshot: snap("search-page-scrolled.ax.txt")}
	b := testAgentBrowser(t, cli, func(context.Context, time.Duration) error { return nil })
	page, err := b.ReadTopic(context.Background(), "", "x")
	if err != nil || len(page.Posts) != 5 {
		t.Fatalf("posts=%d err=%v page=%+v", len(page.Posts), err, page)
	}
	if got := page.Posts[2]; got.Username != "synthetic_user3" || got.Text != "Synthetic post number 3" {
		t.Fatalf("nested post=%+v", got)
	}
	if cli.scrolls < 1 {
		t.Fatalf("scrolls=%d", cli.scrolls)
	}
}

func TestBrowserReadTopic(t *testing.T) {
	cli := &fakeAgentCLI{openURL: "https://www.threads.com/search", snapshot: agentbrowser.Snapshot{Origin: "https://www.threads.com/search?q=x", Text: liveFixture(t, "topic.ax.txt")}}
	b := testAgentBrowser(t, cli, nil)
	page, err := b.ReadTopic(context.Background(), "", "xin chào")
	if err != nil || len(page.Posts) != 2 {
		t.Fatalf("query read: %+v %v", page, err)
	}
	if _, err = b.ReadTopic(context.Background(), "https://example.com/search?q=x", ""); !errors.Is(err, ErrMalformedPage) {
		t.Fatalf("foreign origin: %v", err)
	}
	if _, err = b.ReadTopic(context.Background(), "https://www.threads.com/@a", ""); !errors.Is(err, ErrMalformedPage) {
		t.Fatalf("non-search path: %v", err)
	}
}
