package tools_test

import (
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/tools"
)

func TestReadTopicAfterTrendingPanel(t *testing.T) {
	h := newBrowserHarness(t, true)
	const topic = "https://www.threads.com/search?q=Synthetic%20one&serp_type=trends&trend_fbid=1"
	const permalink = "https://www.threads.com/@synthetic_viet/post/fixtureTopic1"
	h.backend.Pages[threads.OpReadFeed] = threads.Page{Kind: "feed", Trends: []threads.Trend{{Title: "Synthetic topic one", Summary: "Synthetic summary", Posts: "2K posts", URL: topic}}}
	h.backend.Pages[threads.OpReadTopic] = threads.Page{Kind: "topic", Posts: []threads.Post{{Username: "synthetic_viet", Text: "Bài mẫu", URL: permalink}}}

	if _, err := h.call("find_posts", `{"source":"topic","topic":"`+topic+`"}`); err == nil || !strings.Contains(err.Error(), "find_args") {
		t.Fatalf("a topic url not returned this wake was accepted: %v", err)
	}
	h.next()
	if out := h.mustCall("find_posts", `{"source":"feed"}`); !strings.Contains(out, "Synthetic summary") {
		t.Fatalf("trends missing from the feed result: %s", out)
	}
	seen := false
	for _, item := range h.guard.Seen() {
		seen = seen || (item.Ref == "t1" && strings.Contains(item.Text, "Synthetic summary"))
	}
	if !seen {
		t.Fatalf("trend not registered for ideas: %+v", h.guard.Seen())
	}
	h.next()
	if _, err := h.call("find_posts", `{"source":"topic","topic":"t9"}`); err == nil || !strings.Contains(err.Error(), "find_args") {
		t.Fatalf("an unknown ref was accepted: %v", err)
	}
	h.next()
	if out := h.mustCall("find_posts", `{"source":"topic","topic":"t1"}`); !strings.Contains(out, "Bài mẫu") || !strings.Contains(out, `"ref":"p1"`) {
		t.Fatalf("topic posts missing: %s", out)
	}
	if got := h.backend.Last(threads.OpReadTopic).Arg; got != topic {
		t.Fatalf("topic arg %q", got)
	}
	h.next()
	h.mustCall("find_posts", `{"source":"topic","query":"synthetic words"}`)
	if got := h.backend.Last(threads.OpReadTopic).Arg; got != "synthetic words" {
		t.Fatalf("query arg %q", got)
	}
	h.next()
	h.mustCall("reply", `{"text":"Synthetic reply","target":"p1"}`)

	for i := 0; i < tools.MaxTopicReadsPerWake; i++ {
		h.next()
		_, _ = h.call("find_posts", `{"source":"topic","query":"more"}`)
	}
	h.next()
	if _, err := h.call("find_posts", `{"source":"topic","query":"over"}`); err == nil || !strings.Contains(err.Error(), "limit_per_wake") {
		t.Fatalf("cap not enforced: %v", err)
	}
}

func TestReadTopicNeedsExactlyOneArgument(t *testing.T) {
	h := newBrowserHarness(t, true)
	for _, args := range []string{`{"source":"topic"}`, `{"source":"topic","topic":"t1","query":"x"}`, `{"source":"feed","query":"x"}`, `{"source":"search"}`, `{"source":"profile"}`} {
		h.next()
		if _, err := h.call("find_posts", args); err == nil || !strings.Contains(err.Error(), "find_args") {
			t.Fatalf("args %s: %v", args, err)
		}
	}
}

// A news link is not a topic page: it is refused before the browser and does
// not use up one of the wake's topic reads.
func TestReadTopicRefusesANewsLinkWithoutSpendingARead(t *testing.T) {
	h := newBrowserHarness(t, true)
	h.backend.Pages[threads.OpReadFeed] = threads.Page{Kind: "feed", Trends: []threads.Trend{{Title: "Synthetic news", Summary: "Synthetic summary", URL: "https://vnexpress.net/synthetic-story.html"}}}
	h.backend.Pages[threads.OpReadTopic] = threads.Page{Kind: "topic"}
	h.mustCall("find_posts", `{"source":"feed"}`)
	h.next()
	if _, err := h.call("find_posts", `{"source":"topic","topic":"t1"}`); err == nil || !strings.Contains(err.Error(), "find_args") {
		t.Fatalf("a news link was accepted: %v", err)
	}
	for i := 0; i < tools.MaxTopicReadsPerWake; i++ {
		h.next()
		h.mustCall("find_posts", `{"source":"topic","query":"synthetic words"}`)
	}
}
