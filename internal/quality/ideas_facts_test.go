package quality

import (
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/config"
)

func TestIdeasMessagesShowFactsAndAskForNumbers(t *testing.T) {
	p := config.PersonaConfig{Name: "N", Audience: "night-shift nurses"}
	items := []Item{
		{Ref: "t1", Text: "Topic A", Facts: "[topic heat 0.82 · 2.1K posts · 3 sources · seen 2h]", Score: 0.82},
		{Ref: "p2", Text: "A post", Facts: "[45m old · 120 likes · 8 replies · target score 0.71]", Score: 0.71},
		{Ref: "p3", Text: "No numbers"},
	}
	m := IdeasMessages(p, items, nil)
	for _, want := range []string{
		`- ref "t1" [topic heat 0.82 · 2.1K posts · 3 sources · seen 2h]: "Topic A"`,
		`- ref "p2" [45m old · 120 likes · 8 replies · target score 0.71]: "A post"`,
		`- ref "p3": "No numbers"`,
	} {
		if !strings.Contains(m[1].Content, want) {
			t.Errorf("missing %s in\n%s", want, m[1].Content)
		}
	}
	if !strings.Contains(m[0].Content, "ref such as p12 or t3") {
		t.Error("the instructions must ask for the ref")
	}
	if !strings.Contains(m[0].Content, "cite the numbers in why") || !strings.Contains(m[0].Content, "outside window") {
		t.Error("the instructions must ask for the numbers")
	}
}
