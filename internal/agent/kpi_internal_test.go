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
	open := []string{"r3", "r8"}
	check := behindSB.stopCheck(nil, func() []string { return open }, 1.20)
	if ask := check(nil, nil); !strings.Contains(ask, "2 posts you read") || !strings.Contains(ask, "r3, r8") {
		t.Fatalf("ask %q", ask)
	}
	if ask := check(nil, nil); strings.Contains(ask, "note_write") {
		t.Fatalf("stop check pushes a note: %q", ask)
	}
	open = nil
	if ask := check(map[string]bool{"ideas": true}, map[string]int{"note_write": 1}); ask != "" {
		t.Fatalf("nothing left to answer still asked: %q", ask)
	}
	if ask := behindSB.stopCheck(nil, func() []string { return []string{"r1"} }, 0.01)(map[string]bool{"ideas": true}, map[string]int{"note_write": 1}); ask != "" {
		t.Fatalf("spent budget still asked: %q", ask)
	}
}

func TestStopCheckNeverAsksAnAgentOnTarget(t *testing.T) {
	onTarget := behindSB
	onTarget.Engagements = 12
	onTarget.LastPublish = testNow.Add(-time.Hour)
	if onTarget.Behind() {
		t.Fatal("fixture is behind")
	}
	if ask := onTarget.stopCheck(nil, func() []string { return []string{"r1", "r2"} }, 5)(nil, nil); ask != "" {
		t.Fatalf("on-target agent asked: %q", ask)
	}
}
