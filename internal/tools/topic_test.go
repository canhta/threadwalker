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

	// The first reads above count toward the cap; the call past it is refused
	// and the source closes for the rest of the wake.
	var err error
	for i := 0; i < tools.MaxTopicReadsPerWake+1 && (err == nil || !strings.Contains(err.Error(), "limit_per_wake")); i++ {
		h.next()
		_, err = h.call("find_posts", `{"source":"topic","query":"more"}`)
	}
	if err == nil || !strings.Contains(err.Error(), "limit_per_wake") {
		t.Fatalf("cap not enforced: %v", err)
	}
	h.next()
	if _, err = h.call("find_posts", `{"source":"topic","query":"over"}`); err == nil || !strings.Contains(err.Error(), "tool_closed") {
		t.Fatalf("source not closed after its cap: %v", err)
	}
}

// A source refuses only when it is missing what it needs; arguments it does
// not take are ignored.
func TestFindPostsRefusesOnlyAMissingArgument(t *testing.T) {
	h := newBrowserHarness(t, true)
	for _, args := range []string{`{"source":"topic"}`, `{"source":"search"}`, `{"source":"profile"}`} {
		h.next()
		if _, err := h.call("find_posts", args); err == nil || !strings.Contains(err.Error(), "find_args") {
			t.Fatalf("args %s: %v", args, err)
		}
	}
	for _, args := range []string{`{"source":"feed","query":"x","recent":true}`, `{"source":"topic","query":"x","username":"u"}`} {
		h.next()
		if _, err := h.call("find_posts", args); err != nil {
			t.Fatalf("args %s refused: %v", args, err)
		}
	}
}

// A news trend has no topic page, so its title is searched on Threads instead
// of opening the news link.
func TestTopicOfANewsTrendSearchesItsTitle(t *testing.T) {
	h := newBrowserHarness(t, true)
	h.backend.Pages[threads.OpReadFeed] = threads.Page{Kind: "feed", Trends: []threads.Trend{{Title: "Synthetic news", Summary: "Synthetic summary", URL: "https://vnexpress.net/synthetic-story.html"}}}
	h.backend.Pages[threads.OpReadTopic] = threads.Page{Kind: "topic"}
	h.mustCall("find_posts", `{"source":"feed"}`)
	h.next()
	h.mustCall("find_posts", `{"source":"topic","topic":"t1"}`)
	if got := h.backend.Last(threads.OpReadTopic).Arg; got != "Synthetic news" {
		t.Fatalf("searched %q, want the trend title", got)
	}
}

// The agent knows its own posts by their local action id; read_replies maps it
// to the post's media id, and an id that is not the agent's is refused.
func TestReadRepliesTakesAnOwnActionID(t *testing.T) {
	h := newBrowserHarness(t, true)
	insertPipelineAction(t, h, 54, "agent", false, "https://threads.example/own")
	h.mustCall("read_replies", `{"id":"54"}`)
	if got := h.backend.Last(threads.OpReadReplies).Arg; got != mediaOf("https://threads.example/own") {
		t.Fatalf("read replies of %q, want the action's media id", got)
	}
	h.next()
	if _, err := h.call("read_replies", `{"id":"999"}`); err == nil || !strings.Contains(err.Error(), "unknown_target") {
		t.Fatalf("a foreign action id was read: %v", err)
	}
}
