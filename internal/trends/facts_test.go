package trends

import (
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
)

func TestPostFactsScoresTheWindow(t *testing.T) {
	post := func(age, likes, replies int64) threads.Post {
		return threads.Post{Text: "x", AgeMinutes: &age, Likes: &likes, Replies: &replies}
	}
	good, err := PostFacts(post(45, 120, 8), targets)
	if err != nil || good.Score != 0.8 || good.Line != "[45m old · 120 likes · 8 replies · target score 0.80]" {
		t.Fatalf("%+v %v", good, err)
	}
	if strings.Contains(good.Line, "outside") {
		t.Fatalf("in window: %s", good.Line)
	}
	for name, worse := range map[string]threads.Post{
		"old":      post(900, 120, 8),
		"crowded":  post(45, 120, 400),
		"unliked":  post(600, 2, 8),
		"too slow": post(45, 0, 8),
	} {
		got, _ := PostFacts(worse, targets)
		if got.Score >= good.Score || !strings.Contains(got.Line, "outside window") {
			t.Errorf("%s: %+v vs %v", name, got, good.Score)
		}
	}
	old, _ := PostFacts(post(900, 120, 400), targets)
	if !strings.Contains(old.Line, "outside window: age, replies") {
		t.Errorf("%s", old.Line)
	}
	if !strings.Contains(good.TargetScoreJSON, `"likes_per_hour":160`) || *good.ParentReplies != 8 || *good.ParentAgeMinutes != 45 {
		t.Errorf("%+v", good)
	}
}

func TestPostFactsKeepsAbsentAbsent(t *testing.T) {
	if f, err := PostFacts(threads.Post{Text: "x"}, targets); err != nil || f.Line != "" || f.Score != 0 || f.TargetScoreJSON != "" {
		t.Fatalf("no figures, no facts: %+v %v", f, err)
	}
	likes := int64(30)
	f, _ := PostFacts(threads.Post{Likes: &likes}, targets)
	if f.ParentAgeMinutes != nil || f.ParentReplies != nil || strings.Contains(f.Line, "old") || strings.Contains(f.Line, "replies") || strings.Contains(f.TargetScoreJSON, "likes_per_hour") {
		t.Fatalf("invented a figure: %+v", f)
	}
}

func TestTopicFactsNormalisesHeat(t *testing.T) {
	posts := func(n int64) *int64 { return &n }
	prev := func(n int) *int { return &n }
	hot := TopicFacts(Heat{Posts: posts(2100), Sources: 3, FirstSeen: at(-120), Rank: 0, PrevRank: prev(3)}, at(0))
	if hot.Line != "[topic heat 0.72 · 2.1K posts · 3 sources · seen 2h · up 3]" {
		t.Fatalf("%+v", hot)
	}
	for name, cooler := range map[string]Heat{
		"fewer posts":   {Posts: posts(40), Sources: 3, FirstSeen: at(-120), Rank: 0, PrevRank: prev(3)},
		"one source":    {Posts: posts(2100), Sources: 1, FirstSeen: at(-120), Rank: 0, PrevRank: prev(3)},
		"older":         {Posts: posts(2100), Sources: 3, FirstSeen: at(-3000), Rank: 0, PrevRank: prev(3)},
		"falling ranks": {Posts: posts(2100), Sources: 3, FirstSeen: at(-120), Rank: 5, PrevRank: prev(1)},
	} {
		if got := TopicFacts(cooler, at(0)); got.Score >= hot.Score {
			t.Errorf("%s: %v not below %v", name, got.Score, hot.Score)
		}
	}
	if got := TopicFacts(Heat{Posts: posts(5)}, at(0)); strings.Contains(got.Line, "sources") || strings.Contains(got.Line, "seen") || got.Score >= 0.1 {
		t.Errorf("only the post count is known: %+v", got)
	}
	if got := TopicFacts(Heat{}, at(0)); got.Line != "" {
		t.Errorf("no evidence, no facts: %+v", got)
	}
}

func TestStoryHeatsReadsTheHistoryOfEarlierFetches(t *testing.T) {
	stories := []Story{{Title: "AI thay dev", Sources: []string{"a", "b"}}, {Title: "Chuyện mới", Sources: []string{"a"}}}
	history := []store.TrendStory{
		{Title: "ai thay dev", ObservedAt: at(-60), Rank: 4},
		{Title: "AI thay dev", ObservedAt: at(-300), Rank: 1},
	}
	heats := StoryHeats(stories, history, at(0))
	if heats[0].Sources != 2 || !heats[0].FirstSeen.Equal(at(-300)) || heats[0].PrevRank == nil || *heats[0].PrevRank != 4 || heats[0].Rank != 0 {
		t.Fatalf("seen story: %+v", heats[0])
	}
	if heats[1].PrevRank != nil || !heats[1].FirstSeen.Equal(at(0)) || heats[1].Rank != 1 {
		t.Fatalf("new story: %+v", heats[1])
	}
}

func TestFormatCountAndAge(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1K", 2100: "2.1K", 1200000: "1.2M"} {
		if got := formatCount(n); got != want {
			t.Errorf("%d: %s", n, got)
		}
	}
	for m, want := range map[int64]string{5: "5m", 59: "59m", 60: "1h", 1439: "23h", 1440: "1d", 4400: "3d"} {
		if got := formatAge(m); got != want {
			t.Errorf("%d: %s", m, got)
		}
	}
}
