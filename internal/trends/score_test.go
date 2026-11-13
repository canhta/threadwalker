package trends

import (
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
)

var targets = config.DefaultTargets

var t0 = time.Date(2026, 1, 8, 12, 0, 0, 0, time.UTC)

func at(minutes int) time.Time { return t0.Add(time.Duration(minutes) * time.Minute) }

func TestRates(t *testing.T) {
	for _, tc := range []struct {
		name         string
		samples      []sample
		velocity, ac float64
	}{
		{"none", nil, 0, 0},
		{"one", []sample{{at(0), 5}}, 0, 0},
		{"two", []sample{{at(0), 2}, {at(60), 8}}, 6, 0},
		{"accelerating", []sample{{at(0), 0}, {at(60), 2}, {at(120), 8}}, 6, 4},
		{"decelerating unordered", []sample{{at(120), 8}, {at(0), 0}, {at(60), 6}}, 2, -4},
		{"same instant", []sample{{at(0), 1}, {at(0), 9}}, 0, 0},
	} {
		v, a := rates(tc.samples)
		if v != tc.velocity || a != tc.ac {
			t.Errorf("%s: got %v, %v want %v, %v", tc.name, v, a, tc.velocity, tc.ac)
		}
	}
}

func TestWindowOf(t *testing.T) {
	for _, tc := range []struct {
		saturation int
		v, a       float64
		want       Window
	}{
		{0, 1, 0, WindowEarly},
		{2, 1, 1, WindowEarly},
		{2, 1, -1, WindowPeak},
		{3, 1, 1, WindowPeak},
		{7, 0, 0, WindowPeak},
		{8, 5, 5, WindowLate},
		{0, -1, 0, WindowLate},
	} {
		if got := windowOf(tc.saturation, tc.v, tc.a); got != tc.want {
			t.Errorf("%+v: got %s", tc, got)
		}
	}
}

func TestSignalOfLateIsNotRecommended(t *testing.T) {
	s := signalOf([]sample{{at(0), 9}, {at(60), 3}}, at(0), 3, externalScoreHalf, 1, 0, at(90))
	if s.Window != WindowLate || s.Recommended || s.AgeMinutes != 90 || s.Heat <= 0 || s.Heat >= 0.3 {
		t.Fatalf("%+v", s)
	}
	e := signalOf([]sample{{at(0), 1}, {at(60), 9}}, at(0), 9, externalScoreHalf, 0, 0, at(60))
	if e.Window != WindowEarly || !e.Recommended || e.Heat <= s.Heat || e.Heat > 1 {
		t.Fatalf("%+v", e)
	}
}

func TestSaturationOf(t *testing.T) {
	title := []string{"openai", "launches", "agent"}
	posts := []string{
		"openai launches agent xong thay dev",
		"openai agent launches thay dev luôn",
		"openai launches agent mà chưa ai thử",
		"recipe nấu phở",
	}
	saturation, angle := saturationOf(title, posts)
	if saturation != 3 || angle != 2 {
		t.Fatalf("saturation %d angle %d", saturation, angle)
	}
	if s, a := saturationOf(title, nil); s != 0 || a != 0 {
		t.Fatalf("empty: %d %d", s, a)
	}
}

func TestSizeTier(t *testing.T) {
	for _, tc := range []struct {
		author, own int64
		want        string
	}{{0, 100, TierUnknown}, {500, 0, TierUnknown}, {150, 100, TierNear}, {200, 100, TierTarget}, {1000, 100, TierTarget}, {1001, 100, TierMega}} {
		if got := sizeTier(tc.author, tc.own); got != tc.want {
			t.Errorf("%+v: %s", tc, got)
		}
	}
}

func TestScoreTarget(t *testing.T) {
	best := TargetParts{ReplyVelocity: 30, AgeMinutes: 20, OnTopic: true}
	fresh := scoreTarget(best, TierTarget, targets)
	if fresh.Score < 0.9 {
		t.Fatalf("best target %+v", fresh)
	}
	for name, worse := range map[string]TargetScore{
		"old":       scoreTarget(TargetParts{ReplyVelocity: 30, AgeMinutes: 600, OnTopic: true}, TierTarget, targets),
		"saturated": scoreTarget(TargetParts{ReplyVelocity: 30, AgeMinutes: 20, OnTopic: true, JokeSaturation: 9}, TierTarget, targets),
		"off topic": scoreTarget(TargetParts{ReplyVelocity: 30, AgeMinutes: 20}, TierTarget, targets),
		"mega":      scoreTarget(best, TierMega, targets),
		"slow":      scoreTarget(TargetParts{ReplyVelocity: 0.1, AgeMinutes: 20, OnTopic: true}, TierTarget, targets),
	} {
		if worse.Score >= fresh.Score {
			t.Errorf("%s: %v not below %v", name, worse.Score, fresh.Score)
		}
	}
	if u := scoreTarget(best, TierUnknown, targets); u.Score >= fresh.Score || u.Score <= scoreTarget(best, TierMega, targets).Score {
		t.Errorf("unknown tier must score between target and mega: %v", u.Score)
	}
	replied := best
	replied.AlreadyReplied = true
	if got := scoreTarget(replied, TierTarget, targets); got.Score != 0 || !got.AlreadyReplied {
		t.Errorf("already replied: %+v", got)
	}
}

func TestScoreTargetOutsideWindowScoresZero(t *testing.T) {
	rate := func(v float64) *float64 { return &v }
	for _, tc := range []struct {
		name  string
		parts TargetParts
		want  string
	}{
		{"slow", TargetParts{AgeMinutes: 60, Likes: n(1), LikesPerHour: rate(1), Replies: n(0), OnTopic: true}, "likes_per_hour"},
		{"crowded", TargetParts{AgeMinutes: 20, Likes: n(500), LikesPerHour: rate(1500), Replies: n(31), OnTopic: true}, "replies"},
		{"old", TargetParts{AgeMinutes: 241, Likes: n(5000), LikesPerHour: rate(1245), Replies: n(2), OnTopic: true}, "age"},
	} {
		got := scoreTarget(tc.parts, TierTarget, targets)
		if got.Score != 0 || len(got.Outside) != 1 || got.Outside[0] != tc.want || !strings.Contains(got.outside, tc.want) {
			t.Errorf("%s: %+v %q", tc.name, got, got.outside)
		}
	}
	fresh := TargetParts{AgeMinutes: 10, Likes: n(2), LikesPerHour: rate(12), Replies: n(0), OnTopic: true}
	k := targets
	k.MinLikesPerHour = 10
	if got := scoreTarget(fresh, TierTarget, k); got.Score == 0 || len(got.Outside) != 0 {
		t.Fatalf("fresh post at 12 likes/hour is inside: %+v", got)
	}
	k.MinLikes = 30
	got := scoreTarget(fresh, TierTarget, k)
	if got.Score != 0 || len(got.Outside) != 1 || got.Outside[0] != "likes" || got.outside != "likes 2 < min_likes 30" {
		t.Fatalf("2 likes under min_likes 30: %+v %q", got, got.outside)
	}
}

func n(v int64) *int64 { return &v }

func TestComputeLifecycleAndTargets(t *testing.T) {
	post := func(id, author, text string, minutes int, replies int64) store.ThreadsObservation {
		return store.ThreadsObservation{PostURL: "https://threads.net/@" + author + "/post/" + id, Author: author, Snippet: text, Replies: n(replies), Kind: "feed", ObservedAt: at(minutes)}
	}
	in := Input{
		Now:     at(0),
		Targets: targets,
		Include: []string{"ai"},
		Observations: []store.ThreadsObservation{
			post("1", "hot", "ai agent thay dev xong", -50, 2), post("1", "hot", "ai agent thay dev xong", -10, 22),
			post("2", "b", "ai agent thay dev luôn", -30, 0),
			post("3", "c", "ai agent thay dev chưa", -20, 0),
			post("4", "replied", "ai chuyện khác hẳn", -40, 1), post("4", "replied", "ai chuyện khác hẳn", -10, 31),
		},
		Stories: []store.TrendStory{
			{Source: "x", Title: "AI agent thay dev", Score: 10, ObservedAt: at(-1)},
			{Source: "x", Title: "AI agent thay dev", Score: 9, ObservedAt: at(-60)},
			{Source: "x", Title: "AI agent thay dev", Score: 6, ObservedAt: at(-120)},
		},
		Replied:         map[string]bool{"https://threads.net/@replied/post/4": true},
		OwnFollowers:    100,
		AuthorFollowers: map[string]int64{"hot": 500},
	}
	brief := Compute(in)
	if len(brief.HotPosts) != 2 {
		t.Fatalf("hot posts: %+v", brief.HotPosts)
	}
	top, last := brief.HotPosts[0], brief.HotPosts[1]
	if top.Author != "hot" || top.SizeTier != TierTarget || top.Target.Score <= 0 || top.Target.AgeMinutes != 50 || top.Target.JokeSaturation != 2 || top.Target.AlreadyReplied {
		t.Fatalf("top target %+v", top)
	}
	if last.Author != "replied" || last.Target.Score != 0 || !last.Target.AlreadyReplied || last.SizeTier != TierUnknown {
		t.Fatalf("replied target %+v", last)
	}
	var story MergedStory
	for _, candidate := range brief.Stories {
		if candidate.Title == "AI agent thay dev" {
			story = candidate
		}
	}
	if story.Coverage != "both" || story.Saturation != 3 || !story.FirstSeen.Equal(at(-120)) || story.AgeMinutes != 120 || story.Velocity <= 0 || story.Acceleration >= 0 {
		t.Fatalf("story %+v", story)
	}
	if story.Window != WindowEarly && story.Window != WindowPeak {
		t.Fatalf("window %s", story.Window)
	}
	if story.Heat <= 0 || story.Heat > 1 {
		t.Fatalf("heat %v", story.Heat)
	}
	if len(brief.Rising) == 0 || brief.Rising[0].Window == "" {
		t.Fatalf("keywords carry no signal: %+v", brief.Rising)
	}
}
