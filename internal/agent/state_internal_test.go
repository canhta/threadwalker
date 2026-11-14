package agent

import (
	"strings"
	"testing"
)

func TestStopCheckNeverNudgesTowardAHiddenTool(t *testing.T) {
	open := func() []string { return []string{"p1", "p2"} }
	called := map[string]bool{"ideas": true}
	if ask := behindSB.stopCheck([]string{"reply"}, open, 5)(called, nil); ask != "" {
		t.Fatalf("hidden reply still asked for: %q", ask)
	}
	if ask := behindSB.stopCheck([]string{"ideas"}, open, 5)(nil, nil); strings.Contains(ask, "call ideas") || !strings.Contains(ask, "p1, p2") {
		t.Fatalf("ask %q", ask)
	}
}

func TestNothingToDoWhenNoWriteToolIsUsable(t *testing.T) {
	if !(wakeState{}).nothingToDo() {
		t.Fatal("post and reply both closed, inbox or not: nothing to do")
	}
	if (wakeState{PostsLeft: 1}).nothingToDo() || (wakeState{RepliesLeft: 1}).nothingToDo() {
		t.Fatal("an open write tool leaves something to do")
	}
}
