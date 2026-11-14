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
