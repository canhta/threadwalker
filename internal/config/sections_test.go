package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/config"
)

// loadVariant applies one textual edit to the example template and loads it.
// An empty old appends new as extra top-level YAML.
func loadVariant(t *testing.T, old, new string) (config.Template, error) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fixture.agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if old == "" {
		text += "\n" + new + "\n"
	} else {
		if !strings.Contains(text, old) {
			t.Fatalf("example template lacks %q", old)
		}
		text = strings.Replace(text, old, new, 1)
	}
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return loadOne(path, validEnv())
}

type sectionCase struct {
	name, old, new string
	wantErr        string // empty means the load must succeed
	check          func(t *testing.T, tmpl config.Template)
}

func runSection(t *testing.T, cases []sectionCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpl, err := loadVariant(t, tc.old, tc.new)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.check != nil {
				tc.check(t, tmpl)
			}
		})
	}
}

func TestSectionPersona(t *testing.T) {
	runSection(t, []sectionCase{
		{name: "name missing", old: "  name: Kaito\n", new: "", wantErr: "persona.name"},
		{name: "language blank", old: "  language: vi\n", new: "  language:\n", wantErr: "persona.language"},
		{name: "ok", old: "  name: Kaito\n", new: "  name: Other\n", check: func(t *testing.T, tmpl config.Template) {
			if tmpl.Persona.Name != "Other" {
				t.Fatalf("name = %q", tmpl.Persona.Name)
			}
		}},
	})
}

func TestSectionLLM(t *testing.T) {
	runSection(t, []sectionCase{
		{name: "default protocol", check: func(t *testing.T, tmpl config.Template) {
			if tmpl.LLM.Protocol != "openai" || tmpl.LLM.APIKey.Expose() != "secret-llm-key-value" {
				t.Fatalf("llm = %+v", tmpl.LLM)
			}
		}},
		{name: "unknown protocol", old: "llm:\n", new: "llm:\n  protocol: gemini\n", wantErr: "llm.protocol"},
		{name: "negative price", old: "price_per_mtok_in_usd: 0 ", new: "price_per_mtok_in_usd: -1 ", wantErr: "price_per_mtok_in_usd"},
		{name: "key env unset", old: "api_key_env: LLM_API_KEY", new: "api_key_env: NOPE", wantErr: "NOPE is unset"},
		{name: "catalog profile", old: "llm:\n", new: "llm:\n  profile: p\n", wantErr: "declare the llm block inline"},
	})
}

func TestSectionLimits(t *testing.T) {
	runSection(t, []sectionCase{
		{name: "non-integer", old: "  posts_per_day: 3", new: "  posts_per_day: many", wantErr: "limits.posts_per_day"},
		{name: "zero", old: "  posts_per_day: 3", new: "  posts_per_day: 0", wantErr: "must be positive"},
		{name: "zero spend cap", old: "max_usd_per_day: 0.50", new: "max_usd_per_day: 0", wantErr: "limits.max_usd_per_day"},
		{name: "wake gap too long", old: "min_minutes_between_wakes: 20", new: "min_minutes_between_wakes: 1441", wantErr: "at most 1440"},
		{name: "dream defaults", check: func(t *testing.T, tmpl config.Template) {
			if tmpl.Limits.MaxUSDPerDream != 0.30 || tmpl.Limits.MaxStepsPerDream != 8 {
				t.Fatalf("limits = %+v", tmpl.Limits)
			}
		}},
		{name: "dream override", old: "  min_minutes_between_wakes: 20", new: "  min_minutes_between_wakes: 20\n  max_usd_per_dream: 0.1\n  max_steps_per_dream: 5", check: func(t *testing.T, tmpl config.Template) {
			if tmpl.Limits.MaxUSDPerDream != 0.1 || tmpl.Limits.MaxStepsPerDream != 5 {
				t.Fatalf("limits = %+v", tmpl.Limits)
			}
		}},
		{name: "bad dream step cap", old: "  min_minutes_between_wakes: 20", new: "  min_minutes_between_wakes: 20\n  max_steps_per_dream: 0", wantErr: "limits.max_steps_per_dream"},
	})
}

func TestSectionThreads(t *testing.T) {
	runSection(t, []sectionCase{
		{name: "profile dir missing", old: "  profile_dir: data/profiles/fixture\n", new: "", wantErr: "threads.profile_dir"},
		{name: "bad username", old: "username: synthetic_builder", new: "username: no spaces", wantErr: "threads.username"},
		{name: "username normalized", old: "username: synthetic_builder", new: "username: \"@Synthetic_Builder\"", check: func(t *testing.T, tmpl config.Template) {
			if tmpl.Threads.Username != "synthetic_builder" {
				t.Fatalf("username = %q", tmpl.Threads.Username)
			}
		}},
		{name: "backend browser rejected", old: "  backend: stub", new: "  backend: browser", wantErr: "threads.backend"},
	})
}

func TestSectionTelegram(t *testing.T) {
	runSection(t, []sectionCase{
		{name: "report hour out of range", old: "report_hour: 21", new: "report_hour: 24", wantErr: "telegram.report_hour"},
		{name: "enabled not boolean", old: "enabled: false", new: "enabled: maybe", wantErr: "telegram.enabled"},
		{name: "enabled needs env", old: "enabled: false", new: "enabled: true", check: func(t *testing.T, tmpl config.Template) {
			if tmpl.Telegram.BotToken.Expose() != "secret-tg-token-value" {
				t.Fatalf("token not resolved")
			}
		}},
		{name: "bad timezone", old: "report_hour: 21", new: "timezone: Mars/Base\n  report_hour: 21", wantErr: "telegram.timezone"},
		// Unknown keys are ignored.
		{name: "unknown keys ignored", old: "report_hour: 21", new: "digest_hour: 18\n  messages:\n    help: hello\n  report_hour: 21", check: func(t *testing.T, tmpl config.Template) {
			if tmpl.Telegram.ReportHour != 21 {
				t.Fatalf("report hour %d", tmpl.Telegram.ReportHour)
			}
		}},
	})
}

func TestSectionWebSearchScheduleTrendsGuard(t *testing.T) {
	runSection(t, []sectionCase{
		{name: "web search defaults", check: func(t *testing.T, tmpl config.Template) {
			if s := tmpl.WebSearch; s.Enabled || s.BaseURL != "https://api.openai.com/v1" || s.MaxResults != 5 || s.MaxOutput != 512 {
				t.Fatalf("web search = %+v", s)
			}
			if tmpl.Schedule.Timezone != "Asia/Ho_Chi_Minh" {
				t.Fatalf("timezone = %q", tmpl.Schedule.Timezone)
			}
			if tmpl.Limits.Timezone != tmpl.Schedule.Timezone {
				t.Fatalf("limits timezone = %q", tmpl.Limits.Timezone)
			}
		}},
		{name: "unknown source", new: "trends:\n  sources:\n    - nowhere", wantErr: "unknown source nowhere"},
		{name: "sources deduplicated", new: "trends:\n  sources:\n    - hacker_news\n    - hacker_news", check: func(t *testing.T, tmpl config.Template) {
			if len(tmpl.Trends.Sources) != 1 {
				t.Fatalf("sources = %v", tmpl.Trends.Sources)
			}
		}},
		{name: "removed source", new: "trends:\n  sources:\n    - reddit_openai", wantErr: "unknown source reddit_openai"},
		{name: "web search bad url", new: "web_search:\n    enabled: false\n    base_url: ftp://x", wantErr: "HTTP(S) endpoint"},
		{name: "web search enabled not bool", new: "web_search:\n    enabled: perhaps", wantErr: "web_search.enabled"},
		{name: "web search max results", new: "web_search:\n    max_results: 11", wantErr: "at most 10"},
		{name: "web search model defaults to llm", new: "web_search:\n    enabled: true", check: func(t *testing.T, tmpl config.Template) {
			if tmpl.WebSearch.Model != "deepseek-chat" {
				t.Fatalf("model = %q", tmpl.WebSearch.Model)
			}
		}},
		{name: "kpi block absent is off", check: func(t *testing.T, tmpl config.Template) {
			if tmpl.KPI.Enabled() {
				t.Fatalf("kpi = %+v", tmpl.KPI)
			}
		}},
		{name: "kpi block", new: "kpi:\n  engagements_per_day: 40\n  followers_per_week: 5\n  max_hours_without_publish: 8\n  on_target_minutes_between_wakes: 180", check: func(t *testing.T, tmpl config.Template) {
			if tmpl.KPI.EngagementsPerDay != 40 || tmpl.KPI.OnTargetMinutes != 180 || tmpl.KPI.MaxHoursWithoutPublish != 8 || tmpl.KPI.FollowersPerWeek != 5 || !tmpl.KPI.Enabled() {
				t.Fatalf("kpi = %+v", tmpl.KPI)
			}
		}},
		{name: "kpi block missing a key", new: "kpi:\n  engagements_per_day: 40", wantErr: "kpi.followers_per_week"},
		{name: "kpi on-target wait below the minimum", new: "kpi:\n  engagements_per_day: 40\n  followers_per_week: 5\n  max_hours_without_publish: 8\n  on_target_minutes_between_wakes: 1", wantErr: "at least limits.min_minutes_between_wakes"},
		{name: "bad schedule zone", new: "schedule:\n  timezone: Mars/Base", wantErr: "schedule.timezone"},
		{name: "bad dream time", new: "schedule:\n  dream_at: \"25:00\"", wantErr: "HH:MM"},
		{name: "blank keyword", new: "trends:\n  include_keywords:\n    - \" \"", wantErr: "keywords must be nonempty"},
		{name: "blank stop phrase", new: "guard:\n  stop_phrases:\n    - \" \"", wantErr: "phrases must be nonempty"},
	})
}
