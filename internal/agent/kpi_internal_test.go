package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
)

var (
	testNow  = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	behindSB = scoreboard{KPI: config.KPIConfig{EngagementsPerDay: 10, FollowersPerWeek: 5, MaxHoursWithoutPublish: 8, OnTargetMinutes: 180}, Now: testNow}
)

func TestPacedInterval(t *testing.T) {
	for _, c := range []struct {
		name     string
		timeLeft time.Duration
		spent    float64
		wakes    int64
		usdLeft  float64
		want     time.Duration
	}{
		{"no cost history", 10 * time.Hour, 0, 0, 1, 0},
		{"no spend yet", 10 * time.Hour, 0, 3, 1, 0},
		{"no wake yet", 10 * time.Hour, 0.3, 0, 1, 0},
		{"no budget left", 10 * time.Hour, 1, 5, 0, 0},
		{"day over", 0, 0.3, 3, 0.7, 0},
		// $0.10 a wake, $0.70 for 10 hours: 7 more wakes, one every 10h * 0.10 / 0.70.
		{"even spread", 10 * time.Hour, 0.3, 3, 0.7, 10 * time.Hour / 7},
		// Little left: never later than the end of the day.
		{"tiny budget", 2 * time.Hour, 0.99, 3, 0.01, 2 * time.Hour},
	} {
		if got := pacedInterval(c.timeLeft, c.spent, c.wakes, c.usdLeft); got < c.want-time.Millisecond || got > c.want+time.Millisecond {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestStopCheckShowsOpenTargetsToABehindAgentWhileBudgetIsLeft(t *testing.T) {
	open := []string{"p3", "p8"}
	check := behindSB.stopCheck(nil, func() []string { return open }, 1.20)
	if ask := check(nil, nil); !strings.Contains(ask, "2 posts you read") || !strings.Contains(ask, "p3, p8") {
		t.Fatalf("ask %q", ask)
	}
	open = nil
	if ask := check(map[string]bool{"ideas": true}, map[string]int{"search": 1}); ask != "" {
		t.Fatalf("nothing left to answer still asked: %q", ask)
	}
	if ask := behindSB.stopCheck(nil, func() []string { return []string{"p1"} }, 0.01)(map[string]bool{"ideas": true}, map[string]int{"search": 1}); ask != "" {
		t.Fatalf("spent budget still asked: %q", ask)
	}
}

func TestStopCheckNeverAsksAnAgentOnTarget(t *testing.T) {
	onTarget := behindSB
	onTarget.Engagements, onTarget.Followers = 12, 5
	onTarget.LastPublish = testNow.Add(-time.Hour)
	if onTarget.Behind() {
		t.Fatal("fixture is behind")
	}
	if ask := onTarget.stopCheck(nil, func() []string { return []string{"p1", "p2"} }, 5)(nil, nil); ask != "" {
		t.Fatalf("on-target agent asked: %q", ask)
	}
}

func TestBehindCountsFollowersAgainstTheWeeklyTarget(t *testing.T) {
	sb := behindSB
	sb.Engagements, sb.LastPublish = 12, testNow.Add(-time.Hour)
	for _, tc := range []struct {
		followers int64
		behind    bool
	}{{0, true}, {4, true}, {5, false}, {9, false}} {
		sb.Followers = tc.followers
		if sb.Behind() != tc.behind {
			t.Errorf("followers %d: Behind() = %v, want %v", tc.followers, sb.Behind(), tc.behind)
		}
	}
	sb.Followers = 2
	if gap := sb.gap(); !strings.HasPrefix(gap, "behind: 12 of 10 engagements in 24h, 2 of 5 followers gained in 7 days, after $0.00") {
		t.Errorf("gap %q", gap)
	}
	sb.KPI = config.KPIConfig{}
	if sb.Behind() {
		t.Error("a template without a kpi block is never behind")
	}
}
