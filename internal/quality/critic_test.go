package quality

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/config"
)

func TestParseCritic(t *testing.T) {
	ok := `{"pass":false,"problems":["x"],"scores":{"hook":1,"natural_vietnamese":2,"specificity":3,"forced_self_reference":4,"target_fit":5,"novelty":1,"clarity":5}}`
	v, err := ParseCritic(ok)
	if err != nil || v.Pass || v.Problems[0] != "x" || v.Scores.TargetFit != 5 {
		t.Fatalf("%+v %v", v, err)
	}
	for _, bad := range []string{`nope`, `{"pass":true,"extra":1}`, `{"pass":true,"problems":[],"scores":{"hook":6,"natural_vietnamese":1,"specificity":1,"forced_self_reference":1,"target_fit":1,"novelty":1,"clarity":1}}`, `{"pass":true}`} {
		if _, err := ParseCritic(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestUnclearDraftFailsEvenWhenTheCriticPassesIt(t *testing.T) {
	riddle := `{"pass":true,"problems":[],"scores":{"hook":4,"natural_vietnamese":4,"specificity":3,"forced_self_reference":5,"target_fit":4,"novelty":3,"clarity":3}}`
	v, err := ParseCritic(riddle)
	if err != nil || v.Pass || len(v.Problems) != 1 {
		t.Fatalf("%+v %v", v, err)
	}
}

func TestReviseStopsOnPassAndGivesProblemsBack(t *testing.T) {
	n := 0
	write := func(_ context.Context, in WriterInput) ([]string, error) {
		n++
		if n == 2 && in.Problems[0] != "cứng" {
			t.Errorf("revision got %v", in.Problems)
		}
		return []string{"v" + string(rune('0'+n))}, nil
	}
	judge := func(_ context.Context, in CriticInput) (CriticVerdict, error) {
		return CriticVerdict{Pass: in.Texts[0] == "v2", Problems: []string{"cứng"}}, nil
	}
	tries, ok, err := Revise(context.Background(), write, judge, nil, WriterInput{}, 2)
	if err != nil || !ok || len(tries) != 2 {
		t.Fatalf("%v %v %v", tries, ok, err)
	}
}

func TestReviseGivesUpAfterTheRevisions(t *testing.T) {
	write := func(context.Context, WriterInput) ([]string, error) { return []string{"x"}, nil }
	judge := func(context.Context, CriticInput) (CriticVerdict, error) {
		return CriticVerdict{Problems: []string{"p"}}, nil
	}
	tries, ok, _ := Revise(context.Background(), write, judge, nil, WriterInput{}, 2)
	if ok || len(tries) != 3 {
		t.Fatalf("%v %v", tries, ok)
	}
	// No writer: the agent's own draft is judged once.
	tries, ok, _ = Revise(context.Background(), nil, judge, nil, WriterInput{Draft: []string{"a"}}, 2)
	if ok || len(tries) != 1 {
		t.Fatalf("%v %v", tries, ok)
	}
	_, _, err := Revise(context.Background(), write, func(context.Context, CriticInput) (CriticVerdict, error) {
		return CriticVerdict{}, errors.New("down")
	}, nil, WriterInput{}, 2)
	if err == nil {
		t.Fatal("critic error swallowed")
	}
}

func TestParseWriter(t *testing.T) {
	if got, err := ParseWriter(`{"texts":["a","b"]}`, 5); err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{`{"texts":[]}`, `{"texts":["a"," "]}`, `{"texts":["a","b"]}`, `x`} {
		if _, err := ParseWriter(bad, 1); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestParseIdeas(t *testing.T) {
	ideas, err := ParseIdeas(`{"ideas":[{"kind":"post","ref":"u1","moment":"m","why":"w"},{"kind":"reply","ref":"u2","moment":"m","why":"w"},{"kind":"post","ref":"u3","moment":"m","why":"w"},{"kind":"post","ref":"u4","moment":"m","why":"w"}]}`)
	if err != nil || len(ideas) != MaxIdeas {
		t.Fatalf("ideas = %v, %v", ideas, err)
	}
	if ideas, err = ParseIdeas(`{"ideas":[]}`); err != nil || len(ideas) != 0 {
		t.Fatalf("empty = %v, %v", ideas, err)
	}
	if ideas, err = ParseIdeas(`{"ideas":[{"kind":"post","ref":"","moment":"m","why":"w"}]}`); err != nil || len(ideas) != 1 {
		t.Fatalf("a post with no ref is an everyday moment: %v, %v", ideas, err)
	}
	if ideas, err = ParseIdeas(`{"ideas":[{"kind":"post","ref":"","moment":"m","why":"w"}]}`); err != nil || len(ideas) != 1 {
		t.Fatalf("a post with no ref is an everyday moment: %v, %v", ideas, err)
	}
	for _, bad := range []string{`nope`, `{"ideas":[{"kind":"dm","ref":"u","moment":"m","why":"w"}]}`, `{"ideas":[{"kind":"reply","ref":"","moment":"m","why":"w"}]}`} {
		if _, err := ParseIdeas(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestPromptsCarryTheOffLimitsSubjectsAndTheirRule(t *testing.T) {
	r, err := DefaultRubric()
	if err != nil {
		t.Fatal(err)
	}
	p := config.PersonaConfig{Name: "N", AvoidTopics: []string{"pizza wars", "ski trips"}}
	c := CriticMessages(r, p, CriticInput{Kind: Post, Texts: []string{"x"}})
	w := WriterMessages(r, p, WriterInput{Kind: Post})
	i := IdeasMessages(p, nil, nil)
	for name, m := range map[string][2]string{
		"critic": {c[0].Content, c[1].Content},
		"writer": {w[0].Content, w[1].Content},
		"ideas":  {i[0].Content, i[1].Content},
	} {
		if !strings.Contains(m[1], "pizza wars") || !strings.Contains(m[1], "ski trips") {
			t.Errorf("%s prompt lacks the off-limits subjects: %q", name, m[1])
		}
		if !strings.Contains(m[0], "off limits") {
			t.Errorf("%s instructions do not say to respect off-limits subjects", name)
		}
	}
	if !strings.Contains(r.CriticInstructions, "name that subject") {
		t.Error("critic must name the off-limits subject in its problem")
	}
	if strings.Contains(WriterMessages(r, config.PersonaConfig{Name: "N"}, WriterInput{Kind: Post})[1].Content, "off limits") {
		t.Error("an empty list must add nothing")
	}
}

func TestPromptsCarryTheTemplatesAudience(t *testing.T) {
	r, err := DefaultRubric()
	if err != nil {
		t.Fatal(err)
	}
	p := config.PersonaConfig{Name: "N", Audience: "night-shift nurses", CriticRules: []string{"mentions pizza"}}
	c := CriticMessages(r, p, CriticInput{Kind: Post, Texts: []string{"x"}})
	w := WriterMessages(r, p, WriterInput{Kind: Post})
	i := IdeasMessages(p, nil, []string{"outlook went down again"})
	if !strings.Contains(i[1].Content, `"outlook went down again"`) {
		t.Error("ideas must see the account's recent posts so it does not repeat them")
	}
	for _, s := range []string{c[1].Content, w[1].Content, i[1].Content} {
		if !strings.Contains(s, "night-shift nurses") {
			t.Errorf("no audience in %q", s)
		}
	}
	if !strings.Contains(c[1].Content, "(1) mentions pizza") {
		t.Error("critic rule missing")
	}
	for _, s := range []string{r.CriticInstructions, r.WriterInstructions, ideasInstructions} {
		if strings.Contains(s, "GenZ") || strings.Contains(s, "Vietnamese") {
			t.Errorf("audience text in code: %q", s)
		}
	}
}

// The bar for a safe, obvious or outsider draft lives in the critic prompt and
// the persona's rules, for posts and replies alike; code adds no score gate.
func TestTheCriticPromptFailsObviousDraftsForBothKinds(t *testing.T) {
	r, err := DefaultRubric()
	if err != nil {
		t.Fatal(err)
	}
	c := r.CriticInstructions
	for _, want := range []string{"obvious or safe", "outsider", "explains its own joke", "for posts and replies alike"} {
		if !strings.Contains(c, want) {
			t.Errorf("critic instructions miss %q", want)
		}
	}
	if strings.Contains(c, "Never fail a draft for being obvious") {
		t.Error("the critic prompt still forgives obvious drafts")
	}
}
