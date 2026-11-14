package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/config"
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
	if k := tmpl.KPI; k.EngagementsPerDay != 10 || k.FollowersPerWeek != 5 || k.MaxHoursWithoutPublish != 8 || k.OnTargetMinutes != 180 {
		t.Errorf("KPI = %+v", k)
	}
	if tmpl.Inbox.PollMinutes != 10 || tmpl.Inbox.MinMinutesAfterWake != 5 {
		t.Errorf("Inbox = %+v", tmpl.Inbox)
	}
	if tmpl.Threads.Username != "giaosullm" {
		t.Errorf("Threads.Username = %q, want %q", tmpl.Threads.Username, "giaosullm")
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
	for _, rule := range tmpl.Persona.StyleRules {
		if strings.Contains(rule, "không 'tao'") {
			goto avoided
		}
	}
	t.Error("StyleRules do not avoid the tao/mày self-address")
avoided:
	if len(tmpl.Persona.ResearchFocus) != 3 {
		t.Errorf("len(ResearchFocus) = %d, want 3", len(tmpl.Persona.ResearchFocus))
	}
	if len(tmpl.Persona.StyleRules) < 5 {
		t.Errorf("len(StyleRules) = %d, want at least 5", len(tmpl.Persona.StyleRules))
	}
	if len(tmpl.Persona.Topics) != 3 {
		t.Errorf("len(Topics) = %d, want 3", len(tmpl.Persona.Topics))
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
	for _, marker := range []string{"chưa phải", "Ai chịu trách nhiệm"} {
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
