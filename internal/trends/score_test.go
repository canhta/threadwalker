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
	best := TargetParts{ReplyVelocity: 30, AgeMinutes: 20}
	fresh := scoreTarget(best, TierTarget, targets)
	if fresh.Score < 0.9 {
		t.Fatalf("best target %+v", fresh)
	}
	for name, worse := range map[string]TargetScore{
		"old":       scoreTarget(TargetParts{ReplyVelocity: 30, AgeMinutes: 600}, TierTarget, targets),
		"saturated": scoreTarget(TargetParts{ReplyVelocity: 30, AgeMinutes: 20, JokeSaturation: 9}, TierTarget, targets),
		"mega":      scoreTarget(best, TierMega, targets),
		"slow":      scoreTarget(TargetParts{ReplyVelocity: 0.1, AgeMinutes: 20}, TierTarget, targets),
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
		{"slow", TargetParts{AgeMinutes: 60, Likes: n(1), LikesPerHour: rate(1), Replies: n(0)}, "likes_per_hour"},
		{"crowded", TargetParts{AgeMinutes: 20, Likes: n(500), LikesPerHour: rate(1500), Replies: n(31)}, "replies"},
		{"old", TargetParts{AgeMinutes: 241, Likes: n(5000), LikesPerHour: rate(1245), Replies: n(2)}, "age"},
	} {
		got := scoreTarget(tc.parts, TierTarget, targets)
		if got.Score != 0 || len(got.Outside) != 1 || got.Outside[0] != tc.want || !strings.Contains(got.outside, tc.want) {
			t.Errorf("%s: %+v %q", tc.name, got, got.outside)
		}
	}
	fresh := TargetParts{AgeMinutes: 10, Likes: n(2), LikesPerHour: rate(12), Replies: n(0)}
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
