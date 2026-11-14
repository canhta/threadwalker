package tools_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/tools"
)

func TestReadFeedShowsNumbersAndStoresThemOnTheReply(t *testing.T) {
	h := newBrowserHarness(t, true, func(o *tools.Options) { o.Template.Targets = config.DefaultTargets })
	num := func(v int64) *int64 { return &v }
	const topic = "https://www.threads.com/search?q=Synthetic%20one&serp_type=trends&trend_fbid=1"
	const hot = "https://www.threads.com/@synthetic_viet/post/hot"
	const cold = "https://www.threads.com/@synthetic_viet/post/cold"
	h.backend.Pages[threads.OpReadFeed] = threads.Page{Kind: "feed",
		Trends: []threads.Trend{{Title: "Synthetic topic one", Summary: "s", Posts: "2K posts", PostCount: num(2000), URL: topic}},
		Posts: []threads.Post{
			{Username: "synthetic_viet", Text: "cold", URL: cold, AgeMinutes: num(2000), Likes: num(3), Replies: num(90)},
			{Username: "synthetic_viet", Text: "hot", URL: hot, AgeMinutes: num(30), Likes: num(80), Replies: num(4)},
		}}
	if out := h.mustCall("find_posts", `{"source":"feed"}`); !strings.Contains(out, `"likes":80`) || !strings.Contains(out, `"post_count":2000`) || !strings.Contains(out, `"age_minutes":30`) || !strings.Contains(out, `"ref":"p2"`) {
		t.Fatalf("the tool output dropped the figures: %s", out)
	}
	seen := h.guard.Seen()
	if len(seen) != 3 || seen[0].Ref != "t1" && seen[0].Ref != "p2" || seen[2].Ref != "p1" {
		t.Fatalf("not best first: %+v", seen)
	}
	if !strings.HasPrefix(seen[0].Facts, "[") || !strings.Contains(seen[2].Facts, "outside window: age, replies") {
		t.Fatalf("facts: %+v", seen)
	}
	topicFacts := ""
	for _, item := range seen {
		if item.Ref == "t1" {
			topicFacts = item.Facts
		}
	}
	if !strings.HasPrefix(topicFacts, "[topic heat ") || !strings.Contains(topicFacts, "2K posts") {
		t.Fatalf("topic facts %q", topicFacts)
	}

	h.next()
	h.mustCall("reply", `{"text":"Synthetic reply","target":"p2"}`)
	var replies, age sql.NullInt64
	if err := h.store.DB().QueryRow(`SELECT parent_replies,parent_age_minutes FROM actions WHERE kind='reply' ORDER BY id DESC LIMIT 1`).Scan(&replies, &age); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := h.store.DB().QueryRow(`SELECT target_url FROM actions WHERE kind='reply' ORDER BY id DESC LIMIT 1`).Scan(&stored); err != nil || stored != "https://threads.com/@synthetic_viet/post/hot" {
		t.Fatalf("stored target %q, want the canonical permalink: %v", stored, err)
	}
	if replies.Int64 != 4 || age.Int64 != 30 || !replies.Valid || !age.Valid {
		t.Fatalf("stored features: %+v %+v", replies, age)
	}
}
