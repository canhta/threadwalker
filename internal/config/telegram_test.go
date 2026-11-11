package config_test

import (
	"github.com/canhta/threadwalker/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTelegramOptionalAndExplicitlyEnabled(t *testing.T) {
	raw, err := os.ReadFile("../../agents/solo-builder.example.yaml")
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
			tmpl, err := config.LoadFile(p, func(k string) string {
				if k == "LLM_API_KEY" {
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
	_, err = config.LoadFile(p, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "llm.api_key_env") {
		t.Fatal(err)
	}
}
