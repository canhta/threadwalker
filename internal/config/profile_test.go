package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func profileTemplate(t *testing.T, llm string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fixture.agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "\nllm:\n")
	end := strings.Index(text[start:], "\nlimits:") + start
	return text[:start] + "\nllm:\n" + llm + text[end:]
}

func writeProfileFile(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadFile_RejectsCatalogProfile(t *testing.T) {
	path := writeProfileFile(t, profileTemplate(t, "  profile: speedy\n"))
	_, err := loadOne(path, validEnv())
	if err == nil || !strings.Contains(err.Error(), "llm.profile") {
		t.Fatalf("Load error = %v; want llm.profile rejected", err)
	}
}

func TestLoadFile_InlineProtocol(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "fixture.agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, protocol := range []string{"", "openai", "anthropic", "unsupported"} {
		t.Run("protocol_"+protocol, func(t *testing.T) {
			text := string(raw)
			if protocol != "" {
				text = strings.Replace(text, "llm:\n", "llm:\n  protocol: "+protocol+"\n", 1)
			}
			tmpl, err := loadOne(writeProfileFile(t, text), validEnv())
			if protocol == "unsupported" {
				if err == nil || !strings.Contains(err.Error(), "llm.protocol") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := protocol
			if want == "" {
				want = "openai"
			}
			if tmpl.LLM.Protocol != want || tmpl.LLM.Model != "deepseek-chat" || tmpl.LLM.APIKey.Expose() != "secret-llm-key-value" {
				t.Fatalf("LLM = %+v", tmpl.LLM)
			}
		})
	}
}
