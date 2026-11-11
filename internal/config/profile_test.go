package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canhta/threadwalker/internal/config"
)

func profileTemplate(t *testing.T, llm string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "agents", "solo-builder.example.yaml"))
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

func TestLoadFile_ProfileRequiresCatalog(t *testing.T) {
	path := writeProfileFile(t, profileTemplate(t, "  profile: speedy\n"))
	_, err := config.LoadFile(path, validEnv())
	if err == nil || !strings.Contains(err.Error(), "llm.profile") || !strings.Contains(err.Error(), "catalog") {
		t.Fatalf("LoadFile error = %v; want llm.profile catalog error", err)
	}
}

const resolutionCatalog = `providers:
  primary:
    protocol: openai
    base_url: https://primary.example.test/v1
    api_key_env: CHOSEN_KEY
  secondary:
    protocol: anthropic
    base_url: https://secondary.example.test
    api_key_env: UNUSED_KEY
models:
  speedy:
    provider: primary
    model: vendor/model-unlisted
    price_per_mtok_in_usd: 0.2
    price_per_mtok_out_usd: 0.4
  thoughtful:
    provider: secondary
    model: another-unlisted-model
    price_per_mtok_in_usd: 1
    price_per_mtok_out_usd: 2
`

func loadResolutionCatalog(t *testing.T) *config.Catalog {
	t.Helper()
	catalog, err := config.LoadCatalog(writeProfileFile(t, resolutionCatalog))
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestLoadFileWithCatalog_ResolvesOnlySelectedProfile(t *testing.T) {
	catalog := loadResolutionCatalog(t)
	path := writeProfileFile(t, profileTemplate(t, "  profile: speedy\n"))
	getenv := func(name string) string {
		if name == "CHOSEN_KEY" {
			return "private-selected-key"
		}
		if name == "UNUSED_KEY" || name == "LLM_API_KEY" {
			t.Fatalf("unexpected key resolution: %s", name)
		}
		return validEnv()(name)
	}
	tmpl, err := config.LoadFileWithCatalog(path, getenv, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.LLM.Protocol != "openai" || tmpl.LLM.Model != "vendor/model-unlisted" || tmpl.LLM.BaseURL != "https://primary.example.test/v1" || tmpl.LLM.APIKeyEnv != "CHOSEN_KEY" || tmpl.LLM.PricePerMTokInUSD != 0.2 || tmpl.LLM.PricePerMTokOutUSD != 0.4 || tmpl.LLM.APIKey.Expose() != "private-selected-key" {
		t.Fatalf("resolved LLM = %+v", tmpl.LLM)
	}
}

func TestLoadFileWithCatalog_ProfileErrors(t *testing.T) {
	catalog := loadResolutionCatalog(t)
	cases := []struct{ name, llm, field string }{
		{"unknown profile", "  profile: does-not-exist\n", "llm.profile"},
		{"empty profile", "  profile: \"\"\n", "llm.profile"},
		{"blank profile", "  profile:\n", "llm.profile"},
		{"empty inline field", "  profile: speedy\n  model:\n", "llm.model"},

		{"mixed model", "  profile: speedy\n  model: inline-model\n", "llm.model"},
		{"mixed URL", "  profile: speedy\n  base_url: https://inline.example.test\n", "llm.base_url"},
		{"mixed key", "  profile: speedy\n  api_key_env: INLINE_KEY\n", "llm.api_key_env"},
		{"mixed input price", "  profile: speedy\n  price_per_mtok_in_usd: 0\n", "llm.price_per_mtok_in_usd"},
		{"mixed output price", "  profile: speedy\n  price_per_mtok_out_usd: 0\n", "llm.price_per_mtok_out_usd"},
		{"mixed protocol", "  profile: speedy\n  protocol: openai\n", "llm.protocol"},
		{"mixed list", "  profile: speedy\n  model:\n    - inline-model\n", "llm.model"},
		{"missing selected key", "  profile: thoughtful\n", "UNUSED_KEY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeProfileFile(t, profileTemplate(t, tc.llm))
			_, err := config.LoadFileWithCatalog(path, validEnv(), catalog)
			field := tc.field
			if err == nil || !strings.Contains(err.Error(), field) {
				t.Fatalf("error = %v, want field %s", err, field)
			}
			for _, secret := range []string{"secret-llm-key-value", "secret-tg-token-value", "secret-owner-chat-id"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("error exposed a secret")
				}
			}
		})
	}
}

func TestLoadFileWithCatalog_InlineProtocolAndLegacy(t *testing.T) {
	catalog := loadResolutionCatalog(t)
	raw, err := os.ReadFile(filepath.Join("..", "..", "agents", "solo-builder.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, protocol := range []string{"", "openai", "anthropic", "unsupported"} {
		t.Run("protocol_"+protocol, func(t *testing.T) {
			text := string(raw)
			if protocol != "" {
				text = strings.Replace(text, "llm:\n", "llm:\n  protocol: "+protocol+"\n", 1)
			}
			tmpl, err := config.LoadFileWithCatalog(writeProfileFile(t, text), validEnv(), catalog)
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

func TestLoadDirWithCatalog_ResolvesProfilesAndInlineInFilenameOrder(t *testing.T) {
	catalog := loadResolutionCatalog(t)
	dir := t.TempDir()
	first := strings.Replace(profileTemplate(t, "  profile: speedy\n"), "id: solo-builder", "id: selected", 1)
	first = strings.Replace(first, "threads:\n", "threads:\n  username: fixture_selected\n", 1)
	first = strings.Replace(first, "data/profiles/solo-builder", "data/profiles/selected", 1)
	second, err := os.ReadFile(filepath.Join("..", "..", "agents", "solo-builder.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"a.yaml": first, "z.yaml": strings.Replace(string(second), "threads:\n", "threads:\n  username: fixture_solo\n", 1), "ignored.txt": "invalid YAML"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "nested.yaml"), 0700); err != nil {
		t.Fatal(err)
	}
	getenv := func(name string) string {
		if name == "CHOSEN_KEY" {
			return "private-selected-key"
		}
		return validEnv()(name)
	}
	templates, err := config.LoadDirWithCatalog(dir, getenv, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) != 2 || templates[0].ID != "selected" || templates[0].LLM.Model != "vendor/model-unlisted" || templates[1].LLM.Model != "deepseek-chat" {
		t.Fatalf("templates = %+v", templates)
	}
	if _, err := config.LoadDir(dir, getenv); err == nil || !strings.Contains(err.Error(), "llm.profile") {
		t.Fatalf("LoadDir error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.yaml"), []byte("id: invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadDirWithCatalog(dir, getenv, catalog); err == nil || !strings.Contains(err.Error(), "b.yaml") {
		t.Fatalf("LoadDirWithCatalog error = %v", err)
	}
}
