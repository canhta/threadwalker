package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
)

// viralEnv supplies only the inline LLM key the live template names. The test
// never reads a real credential.
func viralEnv() func(string) string {
	return func(k string) string {
		if k == "OPENAI_API_KEY" {
			return "dummy-viral-key"
		}
		return ""
	}
}

func viralTemplatePath() string {
	return filepath.Join("..", "..", "templates", "viral.agent.yaml")
}

func TestLoadFile_ViralTemplate(t *testing.T) {
	tmpl, err := loadOne(viralTemplatePath(), viralEnv())
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if tmpl.ID != "viral" {
		t.Errorf("ID = %q, want %q", tmpl.ID, "viral")
	}
	if k := tmpl.KPI; k.EngagementsPerDay != 10 || k.FollowersPerWeek != 5 || k.MaxHoursWithoutPublish != 8 || k.OnTargetMinutes != 60 {
		t.Errorf("KPI = %+v", k)
	}
	if tmpl.Inbox.PollMinutes != 10 || tmpl.Inbox.MinMinutesAfterWake != 5 {
		t.Errorf("Inbox = %+v", tmpl.Inbox)
	}
	if tmpl.Threads.Username != "your_threads_username" {
		t.Errorf("Threads.Username = %q, want %q", tmpl.Threads.Username, "your_threads_username")
	}
	if tmpl.Persona.Name != "Hóng Hớt" {
		t.Errorf("Persona.Name = %q, want %q", tmpl.Persona.Name, "Hóng Hớt")
	}
	if tmpl.Persona.Language != "vi" {
		t.Errorf("Persona.Language = %q, want %q", tmpl.Persona.Language, "vi")
	}
	if tmpl.Persona.BioMustInclude != "AI tự chạy" {
		t.Errorf("Persona.BioMustInclude = %q, want %q", tmpl.Persona.BioMustInclude, "AI tự chạy")
	}
	if !tmpl.DryRun {
		t.Error("DryRun = false, want the live account template to start in dry run")
	}
	if !strings.Contains(tmpl.Goal, "người lướt") {
		t.Errorf("Goal = %q, want the goal aimed at the Threads reader", tmpl.Goal)
	}
	if !strings.Contains(tmpl.Persona.Voice, "mình") {
		t.Errorf("Persona.Voice = %q, want the mình register", tmpl.Persona.Voice)
	}
	if !strings.Contains(tmpl.Persona.Voice, `không "tao"`) {
		t.Error("Voice does not avoid the tao/mày self-address")
	}
	if len(tmpl.Persona.ResearchFocus) != 2 {
		t.Errorf("len(ResearchFocus) = %d, want 2", len(tmpl.Persona.ResearchFocus))
	}
	if len(tmpl.Persona.StyleRules) < 5 {
		t.Errorf("len(StyleRules) = %d, want at least 5", len(tmpl.Persona.StyleRules))
	}
	if len(tmpl.Persona.Topics) != 2 {
		t.Errorf("len(Topics) = %d, want 2", len(tmpl.Persona.Topics))
	}
	if len(tmpl.Persona.AvoidTopics) != 4 {
		t.Errorf("len(AvoidTopics) = %d, want 4", len(tmpl.Persona.AvoidTopics))
	}
	if len(tmpl.Persona.GoodPosts) < 2 || len(tmpl.Persona.GoodReplies) < 2 {
		t.Errorf("GoodPosts/GoodReplies = %d/%d, want at least 2 each", len(tmpl.Persona.GoodPosts), len(tmpl.Persona.GoodReplies))
	}
	if len(tmpl.Persona.BadExamples) < 3 {
		t.Errorf("len(BadExamples) = %d, want at least 3", len(tmpl.Persona.BadExamples))
	}
	if len(tmpl.Persona.StyleAvoid) < 10 {
		t.Errorf("len(StyleAvoid) = %d, want at least 10", len(tmpl.Persona.StyleAvoid))
	}
	if len(tmpl.Persona.GoodReplies) == 0 {
		t.Fatal("GoodReplies is empty")
	}
	for _, reply := range tmpl.Persona.GoodReplies {
		if n := len([]rune(reply)); n > 120 {
			t.Errorf("GoodReplies entry is %d chars, want <= 120: %q", n, reply)
		}
	}
	for _, post := range tmpl.Persona.GoodPosts {
		if n := len([]rune(post)); n > 500 {
			t.Errorf("GoodPosts entry is %d chars, want <= 500: %q", n, post)
		}
		if lines := strings.Count(post, "\n") + 1; lines > 3 {
			t.Errorf("GoodPosts entry has %d lines, want 1-3: %q", lines, post)
		}
	}
	preachy := strings.Join(tmpl.Persona.BadExamples, "\n")
	for _, marker := range []string{"khởi tố chưa đồng nghĩa có tội", "mình chốt"} {
		if !strings.Contains(preachy, marker) {
			t.Errorf("BadExamples do not keep the rejected preachy draft marker %q", marker)
		}
	}
	forbidden := strings.Join(tmpl.Persona.StyleAvoid, "\n")
	for _, marker := range []string{"chưa phải", "ai chịu trách nhiệm", "chỉ là"} {
		if !strings.Contains(forbidden, marker) {
			t.Errorf("StyleAvoid does not list the lecture marker %q", marker)
		}
	}
	if !tmpl.Guard.TrendAnchor {
		t.Error("Guard.TrendAnchor = false, want every viral post to rest on a trend read this wake")
	}
	raw, err := os.ReadFile("../../templates/viral.agent.yaml")
	if err != nil || strings.Contains(string(raw), "con vợ") {
		t.Errorf("template keeps the con vợ catchphrase (read err %v)", err)
	}
	if tmpl.Limits.PostsPerDay != 6 || tmpl.Limits.RepliesPerDay != 80 || tmpl.Limits.RepliesPerUser7d != 2 {
		t.Errorf("Limits = %+v, want the fixture starting values", tmpl.Limits)
	}
	if tmpl.Limits.MaxUSDPerDay != 1.00 {
		t.Errorf("Limits.MaxUSDPerDay = %v, want the owner's 1.00 cap", tmpl.Limits.MaxUSDPerDay)
	}
	if tmpl.LLM.Model != "gpt-6-luna" || tmpl.LLM.Protocol != "openai" || tmpl.LLM.APIKeyEnv != "OPENAI_API_KEY" {
		t.Errorf("LLM = %+v, want the owner's inline gpt-6-luna block", tmpl.LLM)
	}
	if tmpl.LLM.ReasoningEffort != "none" || tmpl.LLM.PricePerMTokInUSD != 0.10 || tmpl.LLM.PricePerMTokOutUSD != 0.50 {
		t.Errorf("LLM pricing/effort = %+v, want none and OpenAI Standard rates", tmpl.LLM)
	}
	if got := tmpl.Threads.ProfileDir; got != "data/profiles/viral" {
		t.Errorf("Threads.ProfileDir = %q", got)
	}
}

func TestLoadDirPicksUpAgentYAMLTemplates(t *testing.T) {
	raw, err := os.ReadFile(viralTemplatePath())
	if err != nil {
		t.Fatalf("reading canonical template: %v", err)
	}
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, "viral.agent.yaml"), raw, 0600); err != nil {
		t.Fatalf("writing copy into temp dir: %v", err)
	}
	tmpls, err := config.Load(dir, config.Options{Getenv: viralEnv()})
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if len(tmpls) != 1 || tmpls[0].ID != "viral" {
		t.Fatalf("Load returned %d templates, want the one *.agent.yaml template", len(tmpls))
	}
}

func TestViralTemplateHasWriterCriticAndNoStartupPersona(t *testing.T) {
	tmpl, err := loadOne(viralTemplatePath(), viralEnv())
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.Writer.Model == "" || tmpl.Writer.ReasoningEffort != "high" || tmpl.Critic.Model == "" || tmpl.Critic.APIKey.Expose() == "" || tmpl.Writer.APIKey.Expose() == "" {
		t.Errorf("writer %+v critic %+v", tmpl.Writer, tmpl.Critic)
	}
	raw, _ := os.ReadFile(viralTemplatePath())
	persona := string(raw)[strings.Index(string(raw), "persona:"):strings.Index(string(raw), "\nllm:")]
	// The rejected live drafts stay in bad_examples; nothing else may push the startup punchline.
	for _, line := range strings.Split(persona, "\n") {
		if strings.Contains(strings.ToLower(line), "startup") && !strings.HasPrefix(strings.TrimSpace(line), "- \"") {
			t.Errorf("persona line pushes the startup persona: %s", line)
		}
	}
	for _, rule := range append(tmpl.Persona.StyleRules, tmpl.Persona.Voice, tmpl.Persona.Identity) {
		for _, banned := range []string{"cú đấm", "startup tôi"} {
			if strings.Contains(rule, banned) {
				t.Errorf("persona keeps %q: %s", banned, rule)
			}
		}
	}
}

// personaSizes is every persona text field's bytes as the prompts carry them.
func personaSizes(p config.PersonaConfig) (total int, bySection map[string]int) {
	sections := map[string][]string{
		"identity": {p.Identity}, "voice": {p.Voice}, "audience": {p.Audience},
		"critic_rules": p.CriticRules, "research_focus": p.ResearchFocus, "style_rules": p.StyleRules,
		"topics": p.Topics, "avoid_topics": p.AvoidTopics, "good_posts": p.GoodPosts, "good_replies": p.GoodReplies,
		"bad_examples": p.BadExamples, "style_avoid": p.StyleAvoid,
	}
	bySection = map[string]int{}
	for name, items := range sections {
		for _, s := range items {
			bySection[name] += len(strings.TrimSpace(s))
		}
		total += bySection[name]
	}
	return total, bySection
}

// The persona is the one place for who the account is and how it writes; a
// section that grows past its cap holds a rule that belongs somewhere else.
func TestViralPersonaStaysUnderItsByteCaps(t *testing.T) {
	tmpl, err := loadOne(viralTemplatePath(), viralEnv())
	if err != nil {
		t.Fatal(err)
	}
	total, by := personaSizes(tmpl.Persona)
	caps := map[string]int{
		"identity": 560, "voice": 760, "audience": 280, "critic_rules": 820, "research_focus": 300, "style_rules": 2200,
		"topics": 450, "avoid_topics": 560, "good_posts": 200, "good_replies": 150, "bad_examples": 450, "style_avoid": 560,
	}
	for name, limit := range caps {
		if by[name] > limit {
			t.Errorf("persona.%s is %d bytes, cap %d", name, by[name], limit)
		}
	}
	if total > 6*1024 {
		t.Errorf("persona is %d bytes, cap %d: %v", total, 6*1024, by)
	}
	if n := len(tmpl.Persona.CriticRules); n > 6 {
		t.Errorf("%d critic_rules, at most 6", n)
	}
	if n := len(tmpl.Persona.BadExamples); n > 6 {
		t.Errorf("%d bad_examples, at most 6", n)
	}
}

// words lowercases text and keeps its letters and digits as words.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

// A rule lives in one persona section: no two rule texts share a run of
// duplicateRun words. Examples and the literal-phrase list are not rules.
func TestViralPersonaStatesEachRuleOnce(t *testing.T) {
	const duplicateRun = 6
	tmpl, err := loadOne(viralTemplatePath(), viralEnv())
	if err != nil {
		t.Fatal(err)
	}
	p := tmpl.Persona
	rules := map[string][]string{"identity": {p.Identity}, "voice": {p.Voice}, "audience": {p.Audience}, "topics": p.Topics, "avoid_topics": p.AvoidTopics,
		"style_rules": p.StyleRules, "critic_rules": p.CriticRules, "research_focus": p.ResearchFocus}
	seen := map[string]string{}
	for _, section := range []string{"identity", "voice", "audience", "topics", "avoid_topics", "style_rules", "critic_rules", "research_focus"} {
		for i, text := range rules[section] {
			where := fmt.Sprintf("%s[%d]", section, i)
			w := words(text)
			here := map[string]bool{}
			for j := 0; j+duplicateRun <= len(w); j++ {
				run := strings.Join(w[j:j+duplicateRun], " ")
				if first, ok := seen[run]; ok && first != where {
					t.Errorf("%q appears in %s and %s", run, first, where)
				}
				here[run] = true
			}
			for run := range here {
				seen[run] = where
			}
		}
	}
}

// Tool policy lives in the tool specs and the guard's refusals, not the persona.
func TestViralPersonaNamesNoTool(t *testing.T) {
	tmpl, err := loadOne(viralTemplatePath(), viralEnv())
	if err != nil {
		t.Fatal(err)
	}
	p := tmpl.Persona
	text := strings.Join(append(append(append([]string{p.Identity, p.Voice, p.Audience}, p.Topics...), p.ResearchFocus...), append(p.StyleRules, p.CriticRules...)...), "\n")
	for _, spec := range guard.Specs() {
		if strings.Contains(spec.Name, "_") && strings.Contains(text, spec.Name) {
			t.Errorf("persona names the tool %s", spec.Name)
		}
	}
}
