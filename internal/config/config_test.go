package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canhta/threadwalker/internal/config"
)

func validEnv() func(string) string {
	env := map[string]string{
		"LLM_API_KEY":            "secret-llm-key-value",
		"TELEGRAM_BOT_TOKEN":     "secret-tg-token-value",
		"TELEGRAM_OWNER_CHAT_ID": "secret-owner-chat-id",
	}
	return func(k string) string {
		return env[k]
	}
}

func TestLoadFile_ValidExampleTemplate(t *testing.T) {
	path := filepath.Join("..", "..", "agents", "solo-builder.example.yaml")
	tmpl, err := config.LoadFile(path, validEnv())
	if err != nil {
		t.Fatalf("LoadFile returned unexpected error: %v", err)
	}

	if tmpl.ID != "solo-builder" {
		t.Errorf("ID = %q, want %q", tmpl.ID, "solo-builder")
	}
	if !strings.Contains(tmpl.Goal, "Nuôi một acc Threads VN") {
		t.Errorf("Goal = %q", tmpl.Goal)
	}
	if tmpl.Persona.Name != "Kaito" {
		t.Errorf("Persona.Name = %q, want %q", tmpl.Persona.Name, "Kaito")
	}
	if tmpl.Persona.Language != "vi" {
		t.Errorf("Persona.Language = %q, want %q", tmpl.Persona.Language, "vi")
	}
	if tmpl.Persona.BioMustInclude != "thí nghiệm AI tự vận hành" {
		t.Errorf("Persona.BioMustInclude = %q, want %q", tmpl.Persona.BioMustInclude, "thí nghiệm AI tự vận hành")
	}
	if !strings.Contains(tmpl.Persona.Identity, "VPS Ubuntu 2 vCPU") {
		t.Errorf("Persona.Identity = %q", tmpl.Persona.Identity)
	}
	if !strings.Contains(tmpl.Persona.Voice, "Giọng GenZ Threads VN") {
		t.Errorf("Persona.Voice = %q", tmpl.Persona.Voice)
	}
	if len(tmpl.Persona.ResearchFocus) != 4 {
		t.Errorf("len(Persona.ResearchFocus) = %d, want 4", len(tmpl.Persona.ResearchFocus))
	}
	if len(tmpl.Persona.StyleRules) != 5 {
		t.Errorf("len(Persona.StyleRules) = %d, want 5", len(tmpl.Persona.StyleRules))
	}
	if len(tmpl.Persona.Topics) != 4 {
		t.Errorf("len(Persona.Topics) = %d, want 4", len(tmpl.Persona.Topics))
	}
	if len(tmpl.Persona.AvoidTopics) != 4 {
		t.Errorf("len(Persona.AvoidTopics) = %d, want 4", len(tmpl.Persona.AvoidTopics))
	}
	if len(tmpl.Persona.GoodPosts) != 3 {
		t.Errorf("len(Persona.GoodPosts) = %d, want 3", len(tmpl.Persona.GoodPosts))
	}
	if len(tmpl.Persona.GoodReplies) != 3 {
		t.Errorf("len(Persona.GoodReplies) = %d, want 3", len(tmpl.Persona.GoodReplies))
	}
	if len(tmpl.Persona.BadExamples) != 3 {
		t.Errorf("len(Persona.BadExamples) = %d, want 3", len(tmpl.Persona.BadExamples))
	}
	if len(tmpl.Persona.ForbiddenPhrases) != 17 || tmpl.Persona.ForbiddenPhrases[0] != "Tuy nhiên," {
		t.Errorf("Persona.ForbiddenPhrases = %v", tmpl.Persona.ForbiddenPhrases)
	}
	if len(tmpl.SeedPlaybook) != 3 {
		t.Errorf("len(SeedPlaybook) = %d, want 3", len(tmpl.SeedPlaybook))
	}
	if tmpl.LLM.BaseURL != "https://api.deepseek.com" {
		t.Errorf("LLM.BaseURL = %q", tmpl.LLM.BaseURL)
	}
	if tmpl.LLM.Model != "deepseek-chat" {
		t.Errorf("LLM.Model = %q", tmpl.LLM.Model)
	}
	if tmpl.LLM.APIKeyEnv != "LLM_API_KEY" {
		t.Errorf("LLM.APIKeyEnv = %q", tmpl.LLM.APIKeyEnv)
	}
	if tmpl.LLM.APIKey.Expose() != "secret-llm-key-value" {
		t.Errorf("LLM.APIKey.Expose() = %q", tmpl.LLM.APIKey.Expose())
	}
	if tmpl.Limits.PostsPerDay != 3 ||
		tmpl.Limits.RepliesPerDay != 15 ||
		tmpl.Limits.RepliesPerUser7d != 2 ||
		tmpl.Limits.MinSecondsBetweenActions != 300 ||
		tmpl.Limits.MaxStepsPerWake != 30 ||
		tmpl.Limits.MaxUSDPerDay != 0.50 ||
		tmpl.Limits.MinMinutesBetweenWakes != 20 {
		t.Errorf("Limits = %+v", tmpl.Limits)
	}
	if tmpl.Threads.ProfileDir != "data/profiles/solo-builder" {
		t.Errorf("Threads.ProfileDir = %q", tmpl.Threads.ProfileDir)
	}
	if tmpl.Telegram.Enabled || tmpl.Telegram.BotToken.Expose() != "" {
		t.Errorf("Telegram.BotToken.Expose() = %q", tmpl.Telegram.BotToken.Expose())
	}
	if tmpl.Telegram.OwnerChatID.Expose() != "" {
		t.Errorf("Telegram.OwnerChatID.Expose() = %q", tmpl.Telegram.OwnerChatID.Expose())
	}
	if !tmpl.DryRun {
		t.Errorf("DryRun = false, want true")
	}

	formatted := fmt.Sprintf("%v %+v %#v", tmpl, tmpl, tmpl)
	for _, secret := range []string{"secret-llm-key-value", "secret-tg-token-value", "secret-owner-chat-id"} {
		if strings.Contains(formatted, secret) {
			t.Errorf("formatted Template leaked secret %q: %s", secret, formatted)
		}
	}
}

func TestLoadFile_MissingRequiredFields(t *testing.T) {
	requiredFields := []struct {
		field     string
		removeKey string
	}{
		{field: "id", removeKey: "id:"},
		{field: "goal", removeKey: "goal:"},
		{field: "persona.name", removeKey: "  name:"},
		{field: "persona.language", removeKey: "  language:"},
		{field: "persona.bio_must_include", removeKey: "  bio_must_include:"},
		{field: "persona.identity", removeKey: "  identity:"},
		{field: "persona.voice", removeKey: "  voice:"},
		{field: "persona.research_focus", removeKey: "  research_focus:"},
		{field: "persona.style_rules", removeKey: "  style_rules:"},
		{field: "persona.topics", removeKey: "  topics:"},
		{field: "persona.avoid_topics", removeKey: "  avoid_topics:"},
		{field: "persona.good_posts", removeKey: "  good_posts:"},
		{field: "persona.good_replies", removeKey: "  good_replies:"},
		{field: "persona.bad_examples", removeKey: "  bad_examples:"},
		{field: "persona.forbidden_phrases", removeKey: "  forbidden_phrases:"},
		{field: "seed_playbook", removeKey: "seed_playbook:"},
		{field: "llm.base_url", removeKey: "  base_url:"},
		{field: "llm.model", removeKey: "  model:"},
		{field: "llm.api_key_env", removeKey: "  api_key_env:"},
		{field: "llm.price_per_mtok_in_usd", removeKey: "  price_per_mtok_in_usd:"},
		{field: "llm.price_per_mtok_out_usd", removeKey: "  price_per_mtok_out_usd:"},
		{field: "limits.posts_per_day", removeKey: "  posts_per_day:"},
		{field: "limits.replies_per_day", removeKey: "  replies_per_day:"},
		{field: "limits.replies_per_user_7d", removeKey: "  replies_per_user_7d:"},
		{field: "limits.min_seconds_between_actions", removeKey: "  min_seconds_between_actions:"},
		{field: "limits.max_steps_per_wake", removeKey: "  max_steps_per_wake:"},
		{field: "limits.max_usd_per_day", removeKey: "  max_usd_per_day:"},
		{field: "limits.min_minutes_between_wakes", removeKey: "  min_minutes_between_wakes:"},
		{field: "threads.profile_dir", removeKey: "  profile_dir:"},
		{field: "telegram.bot_token_env", removeKey: "  bot_token_env:"},
		{field: "telegram.owner_chat_id_env", removeKey: "  owner_chat_id_env:"},
		{field: "dry_run", removeKey: "dry_run:"},
	}

	examplePath := filepath.Join("..", "..", "agents", "solo-builder.example.yaml")
	rawBytes, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatalf("reading example template: %v", err)
	}
	baseYAML := strings.Replace(string(rawBytes), "enabled: false", "enabled: true", 1)

	for _, tc := range requiredFields {
		t.Run(tc.field, func(t *testing.T) {
			modified := removeYAMLKeyBlock(baseYAML, tc.removeKey)
			tmp := filepath.Join(t.TempDir(), "agent.yaml")
			if err := os.WriteFile(tmp, []byte(modified), 0o600); err != nil {
				t.Fatalf("writing temp template: %v", err)
			}
			_, err := config.LoadFile(tmp, validEnv())
			if err == nil {
				t.Fatalf("expected error when %s is missing, got nil", tc.field)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("error %q does not name missing field %q", err.Error(), tc.field)
			}
		})
	}
}

func removeYAMLKeyBlock(yamlText, prefix string) string {
	lines := strings.Split(yamlText, "\n")
	var out []string
	skipping := false
	keyIndent := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if skipping {
			if trimmed != "" && indent <= keyIndent {
				skipping = false
			} else {
				continue
			}
		}
		if strings.HasPrefix(line, prefix) {
			skipping = true
			keyIndent = indent
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func TestLoadFile_NonPositiveLimits(t *testing.T) {
	limitCases := []struct {
		field      string
		targetLine string
		zeroLine   string
		negLine    string
	}{
		{
			field:      "limits.posts_per_day",
			targetLine: "  posts_per_day: 3",
			zeroLine:   "  posts_per_day: 0",
			negLine:    "  posts_per_day: -1",
		},
		{
			field:      "limits.replies_per_day",
			targetLine: "  replies_per_day: 15",
			zeroLine:   "  replies_per_day: 0",
			negLine:    "  replies_per_day: -5",
		},
		{
			field:      "limits.replies_per_user_7d",
			targetLine: "  replies_per_user_7d: 2",
			zeroLine:   "  replies_per_user_7d: 0",
			negLine:    "  replies_per_user_7d: -2",
		},
		{
			field:      "limits.min_seconds_between_actions",
			targetLine: "  min_seconds_between_actions: 300",
			zeroLine:   "  min_seconds_between_actions: 0",
			negLine:    "  min_seconds_between_actions: -10",
		},
		{
			field:      "limits.max_steps_per_wake",
			targetLine: "  max_steps_per_wake: 30",
			zeroLine:   "  max_steps_per_wake: 0",
			negLine:    "  max_steps_per_wake: -1",
		},
		{
			field:      "limits.max_usd_per_day",
			targetLine: "  max_usd_per_day: 0.50",
			zeroLine:   "  max_usd_per_day: 0",
			negLine:    "  max_usd_per_day: -0.10",
		},
		{
			field:      "limits.min_minutes_between_wakes",
			targetLine: "  min_minutes_between_wakes: 20",
			zeroLine:   "  min_minutes_between_wakes: 0",
			negLine:    "  min_minutes_between_wakes: -5",
		},
	}

	examplePath := filepath.Join("..", "..", "agents", "solo-builder.example.yaml")
	rawBytes, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatalf("reading example template: %v", err)
	}
	baseYAML := string(rawBytes)

	for _, tc := range limitCases {
		for _, replacement := range []string{tc.zeroLine, tc.negLine} {
			t.Run(tc.field+"_"+strings.TrimSpace(replacement), func(t *testing.T) {
				modified := strings.Replace(baseYAML, tc.targetLine, replacement, 1)
				tmp := filepath.Join(t.TempDir(), "agent.yaml")
				if err := os.WriteFile(tmp, []byte(modified), 0o600); err != nil {
					t.Fatalf("writing temp template: %v", err)
				}
				_, err := config.LoadFile(tmp, validEnv())
				if err == nil {
					t.Fatalf("expected error for non-positive %s (%s), got nil", tc.field, replacement)
				}
				if !strings.Contains(err.Error(), tc.field) {
					t.Errorf("error %q does not name limit field %q", err.Error(), tc.field)
				}
			})
		}
	}
}

func TestLoadFile_UnsetSecretVariables(t *testing.T) {
	cases := []struct {
		field  string
		envVar string
	}{
		{field: "llm.api_key_env", envVar: "LLM_API_KEY"},
		{field: "telegram.bot_token_env", envVar: "TELEGRAM_BOT_TOKEN"},
		{field: "telegram.owner_chat_id_env", envVar: "TELEGRAM_OWNER_CHAT_ID"},
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", "agents", "solo-builder.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	examplePath := filepath.Join(t.TempDir(), "enabled.yaml")
	if err = os.WriteFile(examplePath, []byte(strings.Replace(string(raw), "enabled: false", "enabled: true", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			base := validEnv()
			env := func(k string) string {
				if k == tc.envVar {
					return ""
				}
				return base(k)
			}
			_, err := config.LoadFile(examplePath, env)
			if err == nil {
				t.Fatalf("expected error when %s (%s) is unset, got nil", tc.field, tc.envVar)
			}
			if !strings.Contains(err.Error(), tc.field) || !strings.Contains(err.Error(), tc.envVar) {
				t.Errorf("error %q must name field %q and env var %q", err.Error(), tc.field, tc.envVar)
			}
		})
	}
}

func TestLoadDir(t *testing.T) {
	dir := filepath.Join("..", "..", "agents")
	tmpls, err := config.LoadDir(dir, validEnv())
	if err != nil {
		t.Fatalf("LoadDir returned unexpected error: %v", err)
	}
	if len(tmpls) != 1 || tmpls[0].ID != "solo-builder" {
		t.Fatalf("LoadDir returned %+v, want 1 template with ID solo-builder", tmpls)
	}
}

func TestLoadFile_BlockScalarPreservesHashAndErrorsOnInvalidSyntax(t *testing.T) {
	examplePath := filepath.Join("..", "..", "agents", "solo-builder.example.yaml")
	rawBytes, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatalf("reading example template: %v", err)
	}
	baseYAML := string(rawBytes)

	// Block scalar should preserve literal # characters (e.g. #threads)
	withHash := strings.Replace(baseYAML, "sống sót qua ngày", "sống sót #threads qua ngày", 1)
	tmpHash := filepath.Join(t.TempDir(), "hash.yaml")
	if err := os.WriteFile(tmpHash, []byte(withHash), 0o600); err != nil {
		t.Fatalf("writing temp template: %v", err)
	}
	tmpl, err := config.LoadFile(tmpHash, validEnv())
	if err != nil {
		t.Fatalf("LoadFile failed on block scalar with #: %v", err)
	}
	if !strings.Contains(tmpl.Goal, "#threads") {
		t.Errorf("Goal %q did not preserve #threads inside block scalar", tmpl.Goal)
	}

	// Negative price_per_mtok_in_usd and invalid number/bool syntax
	negPrice := strings.Replace(baseYAML, "  price_per_mtok_in_usd: 0", "  price_per_mtok_in_usd: -1", 1)
	negPrice = strings.Replace(negPrice, "  posts_per_day: 3", "  posts_per_day: abc", 1)
	negPrice = strings.Replace(negPrice, "  max_usd_per_day: 0.50", "  max_usd_per_day: xyz", 1)
	negPrice = strings.Replace(negPrice, "dry_run: true", "dry_run: maybe", 1)
	tmpNeg := filepath.Join(t.TempDir(), "neg.yaml")
	if err := os.WriteFile(tmpNeg, []byte(negPrice), 0o600); err != nil {
		t.Fatalf("writing temp template: %v", err)
	}
	_, err = config.LoadFile(tmpNeg, validEnv())
	if err == nil {
		t.Fatal("expected error for negative price and invalid types, got nil")
	}
	for _, want := range []string{"llm.price_per_mtok_in_usd", "limits.posts_per_day", "limits.max_usd_per_day", "dry_run"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing field %q", err.Error(), want)
		}
	}

	// YAML syntax errors: unexpected list item, missing colon, unexpected indentation
	syntaxCases := []struct {
		name string
		yaml string
		want string
	}{
		{name: "unexpected_list_item", yaml: "- orphan\n", want: "unexpected list item"},
		{name: "missing_colon", yaml: "invalid_line_without_colon\n", want: "expected key: value"},
		{name: "unexpected_indent", yaml: "  indented_first: value\n", want: "unexpected indentation"},
	}
	for _, sc := range syntaxCases {
		t.Run(sc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), sc.name+".yaml")
			if err := os.WriteFile(p, []byte(sc.yaml), 0o600); err != nil {
				t.Fatalf("writing file: %v", err)
			}
			_, err := config.LoadFile(p, validEnv())
			if err == nil || !strings.Contains(err.Error(), sc.want) {
				t.Errorf("LoadFile(%s) error = %v, want substring %q", sc.name, err, sc.want)
			}
		})
	}

	// Empty directory and non-existent directory in LoadDir
	emptyDir := t.TempDir()
	if _, err := config.LoadDir(emptyDir, validEnv()); err == nil {
		t.Errorf("LoadDir on empty dir: expected error, got nil")
	}
	if _, err := config.LoadDir(filepath.Join(emptyDir, "missing"), validEnv()); err == nil {
		t.Errorf("LoadDir on missing dir: expected error, got nil")
	}
}
