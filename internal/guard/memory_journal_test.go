package guard_test

import (
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/guard"
)

func TestNoToolTouchesTheMemoryJournal(t *testing.T) {
	if _, ok := guard.Spec("memory_journal"); ok {
		t.Fatal("a memory_journal tool is offered")
	}
	for _, s := range guard.Specs() {
		text := s.Name + " " + s.Description
		for _, a := range s.Args {
			text += " " + a.Name
		}
		if strings.Contains(strings.ToLower(text), "journal") {
			t.Errorf("tool %s mentions the journal", s.Name)
		}
	}
	if s, _ := guard.Spec("memory_supersede"); s.AllowedIn(guard.PhaseAgent) || s.AllowedIn(guard.PhaseStudy) || !s.AllowedIn(guard.PhaseDream) {
		t.Error("memory_supersede must run in the dream phase only")
	}
}
