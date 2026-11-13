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
	if tmpl.Schedule.Timezone != "Asia/Ho_Chi_Minh" || tmpl.Schedule.DreamAt != "" || len(tmpl.Schedule.StudySlots) != 0 {
		t.Fatalf("schedule defaults: %+v", tmpl.Schedule)
	}
	if tmpl.Limits.MaxUSDPerDream != 0.30 || tmpl.Limits.MaxStepsPerDream != 20 {
		t.Fatalf("dream limits: %+v", tmpl.Limits)
	}
	if tmpl.Study.MaxThreadsReads != 4 || tmpl.Study.MaxStudiesPerDay != 8 || tmpl.Study.MinMinutesBetweenStudies != 120 || tmpl.Study.SpikeMultiplier != 2 || tmpl.Study.SpikeMinScore != 6 {
		t.Fatalf("study defaults: %+v", tmpl.Study)
	}
	if len(tmpl.Study.YouTubeChannels) == 0 || len(config.DefaultStudySources) == 0 {
		t.Fatal("missing study source defaults")
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
  study_slots:
    - "08:00"
    - "13:15"
    - "08:00"
trends:
  include_keywords:
    - "AI"
    - "LLM"
    - "token"
  avoid_keywords:
    - "crypto"
study:
  sources:
    - hacker_news
    - vnexpress
    - github_trending
  threads_sample:
    max_reads: 3
    pause_ms: 0
    keywords:
      - "AI thay dev"
  web_search:
    enabled: true
    base_url: https://search.fixture.invalid/v1
    max_results: 4
    max_output_tokens: 256
`)
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.Schedule.DreamAt != "23:30" || len(tmpl.Schedule.StudySlots) != 2 {
		t.Fatalf("schedule: %+v", tmpl.Schedule)
	}
	if tmpl.Limits.MaxStepsPerDream != 12 {
		t.Fatalf("dream steps: %d", tmpl.Limits.MaxStepsPerDream)
	}
	if tmpl.Study.MaxThreadsReads != 3 || tmpl.Study.ReadPauseMS != 0 || len(tmpl.Study.ThreadsKeywords) != 1 {
		t.Fatalf("threads sample: %+v", tmpl.Study)
	}
	if !tmpl.Study.WebSearch || tmpl.Study.WebSearchModel != tmpl.LLM.Model || tmpl.Study.WebSearchMaxResults != 4 || tmpl.Study.WebSearchMaxOutput != 256 {
		t.Fatalf("web search: %+v", tmpl.Study)
	}
	if len(tmpl.Study.Sources) != 3 || !tmpl.StudySourceEnabled("vnexpress") || tmpl.StudySourceEnabled("reddit_openai") {
		t.Fatalf("sources: %v", tmpl.Study.Sources)
	}
	if include := tmpl.Trends.IncludeKeywords; len(include) != 3 || include[0] != "AI" {
		t.Fatalf("include keywords: %v", include)
	}
	for _, bad := range []string{`schedule:
  dream_at: "25:00"`, `schedule:
  study_slots:
    - "8am"`, `study:
  sources:
    - not_a_source`, `study:
  threads_sample:
    max_reads: 50`} {
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
