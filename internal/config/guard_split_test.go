package config_test

import (
	"testing"

	"github.com/canhta/threadz/internal/config"
)

func TestGuardBlockedPhrasesAndStyleAvoidAreSeparateLists(t *testing.T) {
	runSection(t, []sectionCase{
		{name: "blocked phrases load", new: "guard:\n  blocked_phrases:\n    - kèo chắc thắng", check: func(t *testing.T, tmpl config.Template) {
			if len(tmpl.Guard.BlockedPhrases) != 1 || len(tmpl.Persona.StyleAvoid) == 0 {
				t.Fatalf("guard=%+v style_avoid=%v", tmpl.Guard, tmpl.Persona.StyleAvoid)
			}
		}},
		{name: "style phrase under guard", new: "guard:\n  blocked_phrases:\n    - TUY NHIÊN,", wantErr: "also under guard.blocked_phrases"},
		{name: "blank blocked phrase", new: "guard:\n  blocked_phrases:\n    - \"  \"", wantErr: "phrases must be nonempty"},
		{name: "explore percent", old: "  explore_percent: 30", new: "  explore_percent: 101", wantErr: "must be at most 100"},
	})
}
