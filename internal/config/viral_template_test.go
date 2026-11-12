package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canhta/threadwalker/internal/config"
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
	tmpl, err := config.LoadFile(viralTemplatePath(), viralEnv())
	if err != nil {
		t.Fatalf("LoadFile returned unexpected error: %v", err)
	}

	if tmpl.ID != "viral" {
		t.Errorf("ID = %q, want %q", tmpl.ID, "viral")
	}
	if tmpl.Threads.Username != "giaosullm" {
		t.Errorf("Threads.Username = %q, want %q", tmpl.Threads.Username, "giaosullm")
	}
	if tmpl.Persona.Name != "Giáo sư LLM" {
		t.Errorf("Persona.Name = %q, want %q", tmpl.Persona.Name, "Giáo sư LLM")
	}
	if tmpl.Persona.Language != "vi" {
		t.Errorf("Persona.Language = %q, want %q", tmpl.Persona.Language, "vi")
	}
	if tmpl.Persona.BioMustInclude != "giáo sư AI tự chạy" {
		t.Errorf("Persona.BioMustInclude = %q, want %q", tmpl.Persona.BioMustInclude, "giáo sư AI tự chạy")
	}
	if !tmpl.DryRun {
		t.Error("DryRun = false, want the live account template to start in dry run")
	}
	if !strings.Contains(tmpl.Goal, "cà khịa") {
		t.Errorf("Goal = %q, want the owner's viral/cà khịa goal", tmpl.Goal)
	}
	if !strings.Contains(tmpl.Persona.Voice, "tôi") || !strings.Contains(tmpl.Persona.Voice, "các con vợ") {
		t.Errorf("Persona.Voice = %q, want the tôi/các con vợ register", tmpl.Persona.Voice)
	}
	for _, rule := range tmpl.Persona.StyleRules {
		if strings.Contains(rule, "Không xưng 'tao'") {
			goto avoided
		}
	}
	t.Error("StyleRules do not avoid the tao/mày self-address")
avoided:
	if len(tmpl.Persona.ResearchFocus) != 4 {
		t.Errorf("len(ResearchFocus) = %d, want 4", len(tmpl.Persona.ResearchFocus))
	}
	if len(tmpl.Persona.StyleRules) < 5 {
		t.Errorf("len(StyleRules) = %d, want at least 5", len(tmpl.Persona.StyleRules))
	}
	if len(tmpl.Persona.Topics) != 4 {
		t.Errorf("len(Topics) = %d, want 4", len(tmpl.Persona.Topics))
	}
	if len(tmpl.Persona.AvoidTopics) != 4 {
		t.Errorf("len(AvoidTopics) = %d, want 4", len(tmpl.Persona.AvoidTopics))
	}
	if len(tmpl.Persona.GoodPosts) < 6 || len(tmpl.Persona.GoodReplies) < 6 {
		t.Errorf("GoodPosts/GoodReplies = %d/%d, want at least 6 each", len(tmpl.Persona.GoodPosts), len(tmpl.Persona.GoodReplies))
	}
	if len(tmpl.Persona.BadExamples) < 3 {
		t.Errorf("len(BadExamples) = %d, want at least 3", len(tmpl.Persona.BadExamples))
	}
	if len(tmpl.Persona.ForbiddenPhrases) < 10 {
		t.Errorf("len(ForbiddenPhrases) = %d, want at least 10", len(tmpl.Persona.ForbiddenPhrases))
	}
	if len(tmpl.SeedPlaybook) != 3 {
		t.Errorf("len(SeedPlaybook) = %d, want 3", len(tmpl.SeedPlaybook))
	}
	if tmpl.Limits.PostsPerDay != 3 || tmpl.Limits.RepliesPerDay != 15 || tmpl.Limits.RepliesPerUser7d != 2 {
		t.Errorf("Limits = %+v, want the solo-builder starting values", tmpl.Limits)
	}
	if tmpl.Limits.MaxUSDPerDay != 2.00 {
		t.Errorf("Limits.MaxUSDPerDay = %v, want the owner's 2.00 cap", tmpl.Limits.MaxUSDPerDay)
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
	tmpls, err := config.LoadDir(dir, viralEnv())
	if err != nil {
		t.Fatalf("LoadDir returned unexpected error: %v", err)
	}
	if len(tmpls) != 1 || tmpls[0].ID != "viral" {
		t.Fatalf("LoadDir returned %d templates, want the one *.agent.yaml template", len(tmpls))
	}
}
