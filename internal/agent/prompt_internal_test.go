package agent

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/store"
)

// The numbers in the code-owned prompts are the constants that enforce them.
func TestPromptNumbersComeFromTheirConstants(t *testing.T) {
	for _, want := range []string{
		fmt.Sprintf("at most %d characters", store.MaxLessonRunes),
		"(window_h is 1, 6 or 24)",
		fmt.Sprintf("likes plus %d times replies", store.ReplyScoreWeight),
		fmt.Sprintf("last %d actions", store.RankPool),
	} {
		if !strings.Contains(dreamPrompt, want) {
			t.Errorf("dream prompt lacks %q", want)
		}
	}
	if want := fmt.Sprintf("your last %d texts", recentRows); !strings.Contains(wakeRules, want) {
		t.Errorf("wake rules lack %q", want)
	}
}

func viralTemplate(t *testing.T) config.Template {
	t.Helper()
	tmpls, err := config.Load("../../templates", config.Options{Getenv: func(string) string { return "dummy" }})
	if err != nil {
		t.Fatal(err)
	}
	viral := tmpls[len(tmpls)-1] // sorted by file name; viral sorts last
	if viral.ID != "viral" {
		t.Fatalf("last template = %q, want viral", viral.ID)
	}
	return viral
}

// The code-owned part is the prompt of a template with no persona text at all.
func TestCodeOwnedPromptIsShortPlainAndConsistent(t *testing.T) {
	code := systemPrompt(config.Template{}, guard.PhaseAgent)
	if n := len(code); n > 2048 {
		t.Errorf("code-owned prompt is %d bytes, cap 2048", n)
	}
	if !strings.Contains(code, "hard limits (code refuses what breaks one and the refusal names the fix) > owner notes > persona > lessons > examples") {
		t.Errorf("no precedence line:\n%s", code)
	}
	if strings.Count(code, ">") != 4 {
		t.Errorf("want exactly one precedence chain of five ranks:\n%s", code)
	}
	for _, claim := range []string{"Nothing here blocks", "only hard limits"} {
		if strings.Contains(code, claim) {
			t.Errorf("prompt makes the false claim %q", claim)
		}
	}
	for _, spec := range guard.Specs() {
		if (strings.Contains(spec.Name, "_") || spec.Name == "ideas") && strings.Contains(code, spec.Name) {
			t.Errorf("code-owned prompt names the tool %s: tool policy belongs to the schemas and refusals", spec.Name)
		}
	}
	if stamp := regexp.MustCompile(`\d{4}-\d\d|\d\d:\d\d`); stamp.MatchString(code) {
		t.Errorf("prompt carries a date or clock time:\n%s", code)
	}
}

func TestViralSystemPromptStaysUnderItsByteCap(t *testing.T) {
	prompt := systemPrompt(viralTemplate(t), guard.PhaseAgent)
	if n := len(prompt); n > 8*1024 {
		t.Errorf("viral agent system prompt is %d bytes, cap %d", n, 8*1024)
	}
	// style_rules, style_avoid, bad_examples and critic_rules are the writer's and the critic's.
	p := viralTemplate(t).Persona
	for _, owned := range append(append(p.StyleRules, p.StyleAvoid...), append(p.BadExamples, p.CriticRules...)...) {
		if strings.Contains(prompt, owned) {
			t.Errorf("agent prompt repeats a writer or critic rule: %q", owned)
		}
	}
}

func TestDreamPromptHasNoPersona(t *testing.T) {
	if systemPrompt(viralTemplate(t), guard.PhaseDream) != dreamPrompt {
		t.Error("the dream carries more than its own prompt")
	}
}
