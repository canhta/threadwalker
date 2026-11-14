package trends

import (
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
)

var targets = config.DefaultTargets

var t0 = time.Date(2026, 1, 8, 12, 0, 0, 0, time.UTC)

func at(minutes int) time.Time { return t0.Add(time.Duration(minutes) * time.Minute) }

func TestScoreTarget(t *testing.T) {
	rate := func(v float64) *float64 { return &v }
	best := scoreTarget(TargetParts{AgeMinutes: 20, Replies: n(0), LikesPerHour: rate(60)}, targets)
	if best.Score < 0.7 {
		t.Fatalf("best target %+v", best)
	}
	for name, worse := range map[string]TargetScore{
		"older":   scoreTarget(TargetParts{AgeMinutes: 200, Replies: n(0), LikesPerHour: rate(60)}, targets),
		"crowded": scoreTarget(TargetParts{AgeMinutes: 20, Replies: n(25), LikesPerHour: rate(60)}, targets),
		"slower":  scoreTarget(TargetParts{AgeMinutes: 20, Replies: n(0), LikesPerHour: rate(5.5)}, targets),
		"no rate": scoreTarget(TargetParts{AgeMinutes: 20, Replies: n(0)}, targets),
	} {
		if worse.Score >= best.Score || worse.Score == 0 {
			t.Errorf("%s: %v not below %v", name, worse.Score, best.Score)
		}
	}
}

func TestScoreTargetOutsideWindowScoresZero(t *testing.T) {
	rate := func(v float64) *float64 { return &v }
	for _, tc := range []struct {
		name  string
		parts TargetParts
		want  string
	}{
		{"slow", TargetParts{AgeMinutes: 60, Likes: n(1), LikesPerHour: rate(1), Replies: n(0)}, "likes_per_hour"},
		{"crowded", TargetParts{AgeMinutes: 20, Likes: n(500), LikesPerHour: rate(1500), Replies: n(31)}, "replies"},
		{"old", TargetParts{AgeMinutes: 241, Likes: n(5000), LikesPerHour: rate(1245), Replies: n(2)}, "age"},
	} {
		got := scoreTarget(tc.parts, targets)
		if got.Score != 0 || len(got.Outside) != 1 || got.Outside[0] != tc.want || !strings.Contains(got.outside, tc.want) {
			t.Errorf("%s: %+v %q", tc.name, got, got.outside)
		}
	}
	fresh := TargetParts{AgeMinutes: 10, Likes: n(2), LikesPerHour: rate(12), Replies: n(0)}
	k := targets
	k.MinLikesPerHour = 10
	if got := scoreTarget(fresh, k); got.Score == 0 || len(got.Outside) != 0 {
		t.Fatalf("fresh post at 12 likes/hour is inside: %+v", got)
	}
	k.MinLikes = 30
	got := scoreTarget(fresh, k)
	if got.Score != 0 || len(got.Outside) != 1 || got.Outside[0] != "likes" || got.outside != "likes 2 < min_likes 30" {
		t.Fatalf("2 likes under min_likes 30: %+v %q", got, got.outside)
	}
}

func n(v int64) *int64 { return &v }
