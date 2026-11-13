package quality

import (
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/config"
)

func TestIdeasMessagesShowFactsAndAskForNumbers(t *testing.T) {
	p := config.PersonaConfig{Name: "N", Audience: "night-shift nurses"}
	items := []Item{
		{Ref: "r1", Text: "Topic A", Facts: "[topic heat 0.82 · 2.1K posts · 3 sources · seen 2h]", Score: 0.82},
		{Ref: "r2", Text: "A post", Facts: "[45m old · 120 likes · 8 replies · target score 0.71]", Score: 0.71},
		{Ref: "r3", Text: "No numbers"},
	}
	m := IdeasMessages(p, items, nil)
	for _, want := range []string{
		`- ref "r1" [topic heat 0.82 · 2.1K posts · 3 sources · seen 2h]: "Topic A"`,
		`- ref "r2" [45m old · 120 likes · 8 replies · target score 0.71]: "A post"`,
		`- ref "r3": "No numbers"`,
	} {
		if !strings.Contains(m[1].Content, want) {
			t.Errorf("missing %s in\n%s", want, m[1].Content)
		}
	}
	if !strings.Contains(m[0].Content, "short ref such as r3") {
		t.Error("the instructions must ask for the short ref")
	}
	if !strings.Contains(m[0].Content, "cite the numbers in why") || !strings.Contains(m[0].Content, "outside window") {
		t.Error("the instructions must ask for the numbers")
	}
}
