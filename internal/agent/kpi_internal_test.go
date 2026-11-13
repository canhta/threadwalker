package agent

import (
	"strings"
	"testing"
)

func TestStopCheckShowsOpenTargetsWhileBudgetIsLeft(t *testing.T) {
	open := []string{"r3", "r8"}
	check := scoreboard{}.stopCheck(nil, func() []string { return open }, 1.20)
	if ask := check(nil, nil); !strings.Contains(ask, "2 posts you read") || !strings.Contains(ask, "r3, r8") || !strings.Contains(ask, "$1.20") {
		t.Fatalf("ask %q", ask)
	}
	open = nil
	if ask := check(nil, nil); ask != "" {
		t.Fatalf("nothing left to answer still asked: %q", ask)
	}
	if ask := (scoreboard{}).stopCheck(nil, func() []string { return []string{"r1"} }, 0.01)(nil, nil); ask != "" {
		t.Fatalf("spent budget still asked: %q", ask)
	}
}
