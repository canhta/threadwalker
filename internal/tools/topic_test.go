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

	if _, err := h.call("read_topic", `{"url":"`+topic+`"}`); err == nil || !strings.Contains(err.Error(), "read_feed") {
		t.Fatalf("a topic url not returned this wake was accepted: %v", err)
	}
	h.next()
	if out := h.mustCall("read_feed", `{}`); !strings.Contains(out, "Synthetic summary") {
		t.Fatalf("trends missing from the feed result: %s", out)
	}
	seen := false
	for _, item := range h.guard.Seen() {
		seen = seen || (item.Ref == "r1" && strings.Contains(item.Text, "Synthetic summary"))
	}
	if !seen {
		t.Fatalf("trend not registered for ideas: %+v", h.guard.Seen())
	}
	h.next()
	if _, err := h.call("read_topic", `{"url":"r9"}`); err == nil || !strings.Contains(err.Error(), "read_feed") {
		t.Fatalf("an unknown ref was accepted: %v", err)
	}
	h.next()
	if out := h.mustCall("read_topic", `{"url":"r1"}`); !strings.Contains(out, "Bài mẫu") || !strings.Contains(out, `"ref":"r3"`) {
		t.Fatalf("topic posts missing: %s", out)
	}
	if got := h.backend.Last(threads.OpReadTopic).Arg; got != topic {
		t.Fatalf("topic arg %q", got)
	}
	h.next()
	h.mustCall("read_topic", `{"query":"synthetic words"}`)
	if got := h.backend.Last(threads.OpReadTopic).Arg; got != "synthetic words" {
		t.Fatalf("query arg %q", got)
	}
	h.next()
	h.mustCall("reply", `{"text":"Synthetic reply","target_url":"`+permalink+`","target_username":"synthetic_viet"}`)

	for _, args := range []string{`{}`, `{"url":"` + topic + `","query":"x"}`} {
		h.next()
		if _, err := h.call("read_topic", args); err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Fatalf("args %s: %v", args, err)
		}
	}
	for i := 0; i < tools.MaxTopicReadsPerWake; i++ {
		h.next()
		_, _ = h.call("read_topic", `{"query":"more"}`)
	}
	h.next()
	if _, err := h.call("read_topic", `{"query":"over"}`); err == nil || !strings.Contains(err.Error(), "limit per wake") {
		t.Fatalf("cap not enforced: %v", err)
	}
}
