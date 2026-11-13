package trends_test

import (
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/trends"
)

func counter(v int64) *int64 { return &v }

func TestComputeThreadsBriefUsesDeltasKeywordsAndAvoidTopics(t *testing.T) {
	now := time.Date(2026, 1, 8, 12, 0, 0, 0, time.UTC)
	observations := []store.ThreadsObservation{
		// Two observations of the same hot post: reply velocity 10/hour.
		{PostURL: "https://threads.net/@hot/post/1", Author: "hot", Snippet: "AI thay dev là xong, benchmark đâu?", Replies: counter(10), Kind: "feed", ObservedAt: now.Add(-4 * time.Hour)},
		{PostURL: "https://threads.net/@hot/post/1", Author: "hot", Snippet: "AI thay dev là xong, benchmark đâu?", Replies: counter(50), Kind: "feed", ObservedAt: now.Add(-1 * time.Hour)},
		// A rising AI keyword across three recent posts.
		{PostURL: "https://threads.net/@a/post/2", Author: "a", Snippet: "AI agent tự viết code", Kind: "search", ObservedAt: now.Add(-2 * time.Hour)},
		{PostURL: "https://threads.net/@b/post/3", Author: "b", Snippet: "AI agent giá rẻ", Kind: "search", ObservedAt: now.Add(-90 * time.Minute)},
		// Avoid-topic-only post must not become a hot keyword.
		{PostURL: "https://threads.net/@c/post/4", Author: "c", Snippet: "crypto airdrop mùa này", Kind: "search", ObservedAt: now.Add(-time.Hour)},
		// Baseline post from earlier in the week.
		{PostURL: "https://threads.net/@d/post/5", Author: "d", Snippet: "AI cũ", Kind: "feed", ObservedAt: now.Add(-72 * time.Hour)},
		// Do-not-contact author is excluded from hot posts.
		{PostURL: "https://threads.net/@dnc/post/6", Author: "dnc", Snippet: "AI drama", Replies: counter(1), Kind: "feed", ObservedAt: now.Add(-3 * time.Hour)},
		{PostURL: "https://threads.net/@dnc/post/6", Author: "dnc", Snippet: "AI drama", Replies: counter(100), Kind: "feed", ObservedAt: now.Add(-time.Hour)},
	}
	stories := []store.TrendStory{
		{Source: "google_news_vn,the_verge", Title: "AI thay dev và benchmark mới", URL: "https://news.invalid/1", Score: 9, Phase: "study"},
		{Source: "techcrunch", Title: "Cooking recipe", URL: "https://news.invalid/2", Score: 1, Phase: "study"},
	}
	brief := trends.Compute(trends.Input{
		Observations: observations,
		Stories:      stories,
		Include:      []string{"AI", "benchmark"},
		Avoid:        []string{"crypto", "airdrop"},
		OwnUsernames: []string{"giaosullm"},
		DoNotContact: map[string]bool{"dnc": true},
		Targets:      config.DefaultTargets,
		Now:          now,
	})
	foundAI := false
	for _, keyword := range brief.Rising {
		if keyword.Keyword == "ai" && keyword.Recent >= 2 {
			foundAI = true
		}
	}
	if !foundAI {
		t.Fatalf("rising ai missing: %+v", brief.Rising)
	}
	for _, keyword := range brief.Rising {
		if keyword.Keyword == "crypto" || keyword.Keyword == "airdrop" {
			t.Fatalf("avoid topic rose: %+v", brief.Rising)
		}
	}
	if len(brief.HotPosts) != 1 || brief.HotPosts[0].Author != "hot" || brief.HotPosts[0].ReplyVelocity != 13.33 {
		t.Fatalf("hot posts=%+v", brief.HotPosts)
	}
	if !brief.HotPosts[0].OnTopic {
		t.Fatalf("on-topic post flagged off: %+v", brief.HotPosts[0])
	}
	if len(brief.Formats) == 0 {
		t.Fatal("missing format counts")
	}
	coverage := map[string]string{}
	for _, story := range brief.Stories {
		coverage[story.URL] = story.Coverage
	}
	if coverage["https://news.invalid/1"] != "both" {
		t.Fatalf("external story sharing Threads tokens should be both: %+v", brief.Stories)
	}
	if coverage["https://news.invalid/2"] != "external_only" {
		t.Fatalf("unmatched external story should be external_only: %+v", brief.Stories)
	}

}

func TestComputeBriefWithoutDataIsEmptyNotFabricated(t *testing.T) {
	brief := trends.Compute(trends.Input{Now: time.Date(2026, 1, 8, 12, 0, 0, 0, time.UTC)})
	if len(brief.Rising) != 0 || len(brief.HotPosts) != 0 || len(brief.Formats) != 0 || len(brief.Stories) != 0 {
		t.Fatalf("empty brief invented data: %+v", brief)
	}
}
