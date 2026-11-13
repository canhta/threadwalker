package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTelegramOptionalAndExplicitlyEnabled(t *testing.T) {
	raw, err := os.ReadFile("testdata/fixture.agent.yaml")
	if err != nil {
		t.Fatal(err)
	}
	base := removeYAMLKeyBlock(string(raw), "telegram:")
	for _, tc := range []struct {
		name, block string
		wantErr     bool
		reads       int
	}{
		{"omitted", "", false, 0}, {"disabled", "telegram:\n  enabled: false\n", false, 0},
		{"enabled_missing", "telegram:\n  enabled: true\n", true, 0},
		{"invalid", "telegram:\n  enabled: maybe\n", true, 0},
		{"empty", "telegram:\n  enabled:\n", true, 0},
		{"enabled", "telegram:\n  enabled: true\n  bot_token_env: TG\n  owner_chat_id_env: CHAT\n", false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "agent.yaml")
			if err := os.WriteFile(p, []byte(base+"\n"+tc.block), 0600); err != nil {
				t.Fatal(err)
			}
			reads := 0
			tmpl, err := loadOne(p, func(k string) string {
				if k == "LLM_API_KEY" || k == "OPENAI_API_KEY" {
					return "dummy-llm"
				}
				reads++
				return "dummy-telegram"
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v", err)
			}
			if reads != tc.reads {
				t.Fatalf("telegram reads=%d", reads)
			}
			if !tc.wantErr && tc.name != "enabled" && tmpl.Telegram.BotToken.Expose() != "" {
				t.Fatal("resolved disabled secret")
			}
		})
	}
	// Enabling requires environment values, disabling does not weaken LLM validation.
	p := filepath.Join(t.TempDir(), "agent.yaml")
	os.WriteFile(p, []byte(base), 0600)
	_, err = loadOne(p, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "llm.api_key_env") {
		t.Fatal(err)
	}
}

func TestTelegramReportHour(t *testing.T) {
	raw, err := os.ReadFile("testdata/fixture.agent.yaml")
	if err != nil {
		t.Fatal(err)
	}
	base := removeYAMLKeyBlock(string(raw), "telegram:")
	for _, tc := range []struct {
		hour    string
		want    int
		invalid bool
	}{{"", 21, false}, {"0", 0, false}, {"23", 23, false}, {"24", 0, true}, {"-1", 0, true}, {"noon", 0, true}, {"null", 0, true}} {
		t.Run(tc.hour, func(t *testing.T) {
			text := base + "\ntelegram:\n  enabled: false\n"
			if tc.hour != "" {
				text += "  report_hour: " + tc.hour + "\n"
			}
			path := filepath.Join(t.TempDir(), "agent.yaml")
			if err = os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			tmpl, err := loadOne(path, func(string) string { return "synthetic-key" })
			if (err != nil) != tc.invalid {
				t.Fatalf("hour %q error %v", tc.hour, err)
			}
			if err == nil && tmpl.Telegram.ReportHour != tc.want {
				t.Fatalf("hour %d want %d", tmpl.Telegram.ReportHour, tc.want)
			}
		})
	}
}

func TestTelegramOwnerTimezone(t *testing.T) {
	raw, err := os.ReadFile("testdata/fixture.agent.yaml")
	if err != nil {
		t.Fatal(err)
	}
	base := removeYAMLKeyBlock(string(raw), "telegram:")
	for _, tc := range []struct {
		name    string
		extra   string
		zone    string
		invalid bool
	}{
		{name: "defaults", zone: "Asia/Ho_Chi_Minh"},
		{name: "configured zone", extra: "  timezone: UTC\n", zone: "UTC"},
		{name: "invalid zone", extra: "  timezone: Mars/Base\n", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := base + "\ntelegram:\n  enabled: false\n" + tc.extra
			path := filepath.Join(t.TempDir(), "agent.yaml")
			if err = os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			tmpl, err := loadOne(path, func(string) string { return "synthetic-key" })
			if (err != nil) != tc.invalid {
				t.Fatalf("error %v", err)
			}
			if tc.invalid {
				return
			}
			if tmpl.Telegram.Timezone != tc.zone {
				t.Fatalf("zone %q want %q", tmpl.Telegram.Timezone, tc.zone)
			}
		})
	}
}
