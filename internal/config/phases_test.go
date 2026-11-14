package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/config"
)

func baseTemplate(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/fixture.agent.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// Rename the copy so several fixtures never collide.
	return strings.Replace(string(raw), "id: fixture", "id: phase-fixture", 1)
}

func loadTemplate(t *testing.T, extra string) (config.Template, error) {
	t.Helper()
	raw := strings.Replace(baseTemplate(t), "max_usd_per_day: 0.50", "max_usd_per_day: 0.50\n  max_usd_per_dream: 0.30\n  max_steps_per_dream: 12", 1)
	path := filepath.Join(t.TempDir(), "phase.agent.yaml")
	if err := os.WriteFile(path, []byte(raw+"\n"+extra+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return loadOne(path, func(name string) string { return "synthetic-value" })
}

func TestTemplatePhaseDefaults(t *testing.T) {
	tmpl, err := loadPlainTemplate(t)
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.Schedule.Timezone != "Asia/Ho_Chi_Minh" || tmpl.Schedule.DreamAt != "" {
		t.Fatalf("schedule defaults: %+v", tmpl.Schedule)
	}
	if tmpl.Limits.MaxUSDPerDream != 0.30 || tmpl.Limits.MaxStepsPerDream != 8 {
		t.Fatalf("dream limits: %+v", tmpl.Limits)
	}
	include, avoid := tmpl.TrendKeywords()
	if len(include) == 0 || len(avoid) == 0 {
		t.Fatalf("derived keywords: include=%v avoid=%v", include, avoid)
	}
}

func loadPlainTemplate(t *testing.T) (config.Template, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "phase-defaults.agent.yaml")
	if err := os.WriteFile(path, []byte(baseTemplate(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	return loadOne(path, func(string) string { return "synthetic-value" })
}

func TestTemplatePhaseFieldsParseAndValidate(t *testing.T) {
	tmpl, err := loadTemplate(t, `
schedule:
  timezone: Asia/Ho_Chi_Minh
  dream_at: "23:30"
trends:
  sources:
    - hacker_news
    - vnexpress
    - github_trending
  include_keywords:
    - "AI"
    - "LLM"
    - "token"
  avoid_keywords:
    - "crypto"
web_search:
  enabled: true
  base_url: https://search.fixture.invalid/v1
  max_results: 4
  max_output_tokens: 256
`)
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.Schedule.DreamAt != "23:30" {
		t.Fatalf("schedule: %+v", tmpl.Schedule)
	}
	if tmpl.Limits.MaxStepsPerDream != 12 {
		t.Fatalf("dream steps: %d", tmpl.Limits.MaxStepsPerDream)
	}
	if w := tmpl.WebSearch; !w.Enabled || w.Model != tmpl.LLM.Model || w.MaxResults != 4 || w.MaxOutput != 256 {
		t.Fatalf("web search: %+v", w)
	}
	if len(tmpl.Trends.Sources) != 3 {
		t.Fatalf("sources: %v", tmpl.Trends.Sources)
	}
	if include := tmpl.Trends.IncludeKeywords; len(include) != 3 || include[0] != "AI" {
		t.Fatalf("include keywords: %v", include)
	}
	for _, bad := range []string{`schedule:
  dream_at: "25:00"`, `trends:
  sources:
    - not_a_source`, `web_search:
  max_results: 50`} {
		if _, err := loadTemplate(t, bad); err == nil {
			t.Fatalf("invalid %q accepted", bad)
		}
	}
}

func TestTemplateTrendKeywordsPreferExplicitLists(t *testing.T) {
	tmpl, err := loadTemplate(t, `
trends:
  include_keywords:
    - "benchmark"
    - "latency"
  avoid_keywords:
    - "crypto"
    - "airdrop"
`)
	if err != nil {
		t.Fatal(err)
	}
	include, avoid := tmpl.TrendKeywords()
	if len(include) != 2 || include[0] != "benchmark" || len(avoid) != 2 || avoid[1] != "airdrop" {
		t.Fatalf("explicit keywords: include=%v avoid=%v", include, avoid)
	}
}

func TestTemplateTrendSources(t *testing.T) {
	tmpl, err := loadTemplate(t, "trends:\n  sources:\n    - google_trends\n    - genk\n    - genk")
	if err != nil || len(tmpl.Trends.Sources) != 2 || tmpl.Trends.Sources[1] != "genk" {
		t.Fatalf("sources: %v %v", tmpl.Trends.Sources, err)
	}
	if _, err := loadTemplate(t, "trends:\n  sources:\n    - nope"); err == nil {
		t.Fatal("unknown trend source accepted")
	}
}
