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
			if tmpl.Limits.MaxUSDPerDream != 0.30 || tmpl.Limits.MaxStepsPerDream != 20 {
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
		{name: "digest hour out of range", old: "digest_hour: 18", new: "digest_hour: 24", wantErr: "telegram.digest_hour"},
		{name: "enabled not boolean", old: "enabled: false", new: "enabled: maybe", wantErr: "telegram.enabled"},
		{name: "enabled needs env", old: "enabled: false", new: "enabled: true", check: func(t *testing.T, tmpl config.Template) {
			if tmpl.Telegram.BotToken.Expose() != "secret-tg-token-value" {
				t.Fatalf("token not resolved")
			}
		}},
		{name: "bad timezone", old: "digest_hour: 18", new: "timezone: Mars/Base\n  digest_hour: 18", wantErr: "telegram.timezone"},
		{name: "unknown message", old: "digest_hour: 18", new: "messages:\n    bogus: hi\n  digest_hour: 18", wantErr: "unknown owner message name"},
		{name: "broken message template", old: "digest_hour: 18", new: "messages:\n    help: \"{{\"\n  digest_hour: 18", wantErr: "invalid message template"},
		{name: "message override", old: "digest_hour: 18", new: "messages:\n    help: hello\n  digest_hour: 18", check: func(t *testing.T, tmpl config.Template) {
			if tmpl.Telegram.Messages["help"] != "hello" {
				t.Fatalf("messages = %v", tmpl.Telegram.Messages)
			}
		}},
	})
}

func TestSectionStudyScheduleTrendsGuard(t *testing.T) {
	runSection(t, []sectionCase{
		{name: "study defaults", check: func(t *testing.T, tmpl config.Template) {
			s := tmpl.Study
			if s.MaxThreadsReads != 4 || s.ReadPauseMS != 1500 || s.MaxStudiesPerDay != 8 || s.SpikeMultiplier != 2 || s.WebSearchBaseURL != "https://api.openai.com/v1" {
				t.Fatalf("study = %+v", s)
			}
			if tmpl.Schedule.Timezone != "Asia/Ho_Chi_Minh" {
				t.Fatalf("timezone = %q", tmpl.Schedule.Timezone)
			}
		}},
		{name: "unknown source", new: "study:\n  sources:\n    - nowhere", wantErr: "unknown source nowhere"},
		{name: "sources deduplicated", new: "study:\n  sources:\n    - hacker_news\n    - hacker_news", check: func(t *testing.T, tmpl config.Template) {
			if len(tmpl.Study.Sources) != 1 {
				t.Fatalf("sources = %v", tmpl.Study.Sources)
			}
		}},
		{name: "bad youtube channel", new: "study:\n  youtube_channels:\n    - bad id", wantErr: "YouTube channel ID"},
		{name: "reads too many", new: "study:\n  threads_sample:\n    max_reads: 21", wantErr: "at most 20"},
		{name: "pause out of range", new: "study:\n  threads_sample:\n    pause_ms: 60001", wantErr: "0..60000"},
		{name: "spike multiplier out of range", new: "study:\n  spike_multiplier: 21", wantErr: "1..20"},
		{name: "web search bad url", new: "study:\n  web_search:\n    enabled: false\n    base_url: ftp://x", wantErr: "HTTP(S) endpoint"},
		{name: "web search enabled not bool", new: "study:\n  web_search:\n    enabled: perhaps", wantErr: "study.web_search.enabled"},
		{name: "web search max results", new: "study:\n  web_search:\n    max_results: 11", wantErr: "at most 10"},
		{name: "web search model defaults to llm", new: "study:\n  web_search:\n    enabled: true", check: func(t *testing.T, tmpl config.Template) {
			if tmpl.Study.WebSearchModel != "deepseek-chat" {
				t.Fatalf("model = %q", tmpl.Study.WebSearchModel)
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
		{name: "study slots deduplicated", new: "schedule:\n  study_slots:\n    - \"09:00\"\n    - \"09:00\"\n    - \"13:30\"", check: func(t *testing.T, tmpl config.Template) {
			if got := strings.Join(tmpl.Schedule.StudySlots, ","); got != "09:00,13:30" {
				t.Fatalf("slots = %s", got)
			}
		}},
		{name: "blank keyword", new: "trends:\n  include_keywords:\n    - \" \"", wantErr: "keywords must be nonempty"},
		{name: "blank stop phrase", new: "guard:\n  stop_phrases:\n    - \" \"", wantErr: "phrases must be nonempty"},
	})
}
