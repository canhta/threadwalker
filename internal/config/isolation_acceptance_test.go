package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canhta/threadwalker/internal/config"
)

// These tests exercise the public configuration seam used by the multi-agent
// runtime. They are deliberately independent of the loader implementation: a
// two-template set is only accepted when operator-declared identities and
// browser profiles are distinct after normalization.

func isolationTemplateYAML(id, username, profileDir string) string {
	var b strings.Builder
	b.WriteString("id: " + id + "\n")
	b.WriteString("goal: synthetic isolation acceptance goal\n")
	b.WriteString("persona:\n")
	b.WriteString("  name: Synthetic Persona\n")
	b.WriteString("  language: vi\n")
	b.WriteString("  bio_must_include: \"synthetic AI label\"\n")
	b.WriteString("  identity: synthetic identity\n")
	b.WriteString("  voice: synthetic voice\n")
	b.WriteString("  research_focus:\n    - \"synthetic focus\"\n")
	b.WriteString("  style_rules:\n    - \"synthetic style\"\n")
	b.WriteString("  topics:\n    - \"synthetic topic\"\n")
	b.WriteString("  avoid_topics:\n    - \"synthetic avoided topic\"\n")
	b.WriteString("  good_posts:\n    - \"synthetic post\"\n")
	b.WriteString("  good_replies:\n    - \"synthetic reply\"\n")
	b.WriteString("  bad_examples:\n    - \"synthetic bad example\"\n")
	b.WriteString("  forbidden_phrases:\n    - \"synthetic forbidden phrase\"\n")
	b.WriteString("seed_playbook:\n  - \"synthetic hypothesis\"\n")
	b.WriteString("llm:\n")
	b.WriteString("  base_url: https://api.example.invalid\n")
	b.WriteString("  model: synthetic-model\n")
	b.WriteString("  api_key_env: LLM_API_KEY\n")
	b.WriteString("  price_per_mtok_in_usd: 0\n")
	b.WriteString("  price_per_mtok_out_usd: 0\n")
	b.WriteString("limits:\n")
	b.WriteString("  posts_per_day: 1\n")
	b.WriteString("  replies_per_day: 2\n")
	b.WriteString("  replies_per_user_7d: 1\n")
	b.WriteString("  min_seconds_between_actions: 60\n")
	b.WriteString("  max_steps_per_wake: 5\n")
	b.WriteString("  max_usd_per_day: 0.10\n")
	b.WriteString("  min_minutes_between_wakes: 30\n")
	b.WriteString("threads:\n")
	if username != "" {
		b.WriteString("  username: " + username + "\n")
	}
	b.WriteString("  profile_dir: " + profileDir + "\n")
	b.WriteString("telegram:\n  enabled: false\n")
	b.WriteString("dry_run: true\n")
	return b.String()
}

func isolationWriteTemplate(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
		t.Fatalf("writing template fixture: %v", err)
	}
}

func isolationProfileDirs(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	one := filepath.Join(root, "agent-one")
	two := filepath.Join(root, "agent-two")
	for _, dir := range []string{one, two} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("creating profile fixture: %v", err)
		}
	}
	return one, two
}

func isolationGetenv(name string) string {
	if name == "LLM_API_KEY" {
		return "synthetic-isolation-test-key"
	}
	return ""
}

func TestIsolationAcceptanceLoadsDistinctNormalizedIdentities(t *testing.T) {
	dir := t.TempDir()
	profileOne, profileTwo := isolationProfileDirs(t)
	isolationWriteTemplate(t, dir, "one.yaml", isolationTemplateYAML("agent-one", "@Synthetic_One", profileOne))
	isolationWriteTemplate(t, dir, "two.yaml", isolationTemplateYAML("agent-two", " synthetic.two ", profileTwo))

	templates, err := config.LoadDirWithCatalog(dir, isolationGetenv, nil)
	if err != nil {
		t.Fatalf("LoadDirWithCatalog rejected distinct identities: %v", err)
	}
	if len(templates) != 2 {
		t.Fatalf("loaded %d templates, want 2", len(templates))
	}
	got := map[string]string{}
	for _, tmpl := range templates {
		got[tmpl.ID] = tmpl.Threads.Username
	}
	if got["agent-one"] != "synthetic_one" {
		t.Errorf("agent-one username = %q, want normalized synthetic_one", got["agent-one"])
	}
	if got["agent-two"] != "synthetic.two" {
		t.Errorf("agent-two username = %q, want normalized synthetic.two", got["agent-two"])
	}
}

func TestIsolationAcceptanceMultiTemplateIdentityFailsClosed(t *testing.T) {
	profileOne, profileTwo := isolationProfileDirs(t)
	cases := []struct {
		name    string
		oneUser string
		twoUser string
	}{
		{name: "missing username on one template", oneUser: "synthetic_one", twoUser: ""},
		{name: "duplicate after case and at-sign normalization", oneUser: "@Synthetic_One", twoUser: "synthetic_one"},
		{name: "invalid username", oneUser: "synthetic_one", twoUser: "bad/username"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			isolationWriteTemplate(t, dir, "one.yaml", isolationTemplateYAML("agent-one", tc.oneUser, profileOne))
			isolationWriteTemplate(t, dir, "two.yaml", isolationTemplateYAML("agent-two", tc.twoUser, profileTwo))
			if _, err := config.LoadDirWithCatalog(dir, isolationGetenv, nil); err == nil {
				t.Fatal("LoadDirWithCatalog accepted multi-template set without distinct valid usernames")
			}
		})
	}
}

func TestIsolationAcceptanceMultiTemplateIDsAndProfilesFailClosed(t *testing.T) {
	cases := []struct {
		name       string
		idOne      string
		idTwo      string
		profileOne func(root string) string
		profileTwo func(root string) string
		symlink    bool
	}{
		{
			name:       "duplicate agent id",
			idOne:      "agent-one",
			idTwo:      "agent-one",
			profileOne: func(root string) string { return filepath.Join(root, "one") },
			profileTwo: func(root string) string { return filepath.Join(root, "two") },
		},
		{
			name:       "same profile path",
			idOne:      "agent-one",
			idTwo:      "agent-two",
			profileOne: func(root string) string { return filepath.Join(root, "shared") },
			profileTwo: func(root string) string { return filepath.Join(root, "shared") },
		},
		{
			name:       "nested profile path",
			idOne:      "agent-one",
			idTwo:      "agent-two",
			profileOne: func(root string) string { return filepath.Join(root, "shared") },
			profileTwo: func(root string) string { return filepath.Join(root, "shared", "nested") },
		},
		{
			name:       "symlink alias",
			idOne:      "agent-one",
			idTwo:      "agent-two",
			profileOne: func(root string) string { return filepath.Join(root, "one") },
			profileTwo: func(root string) string { return filepath.Join(root, "alias") },
			symlink:    true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			profileOne := tc.profileOne(root)
			profileTwo := tc.profileTwo(root)
			for _, dir := range []string{profileOne, profileTwo, filepath.Join(root, "one"), filepath.Join(root, "shared")} {
				if tc.symlink && dir == profileTwo {
					continue
				}
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("creating profile fixture: %v", err)
				}
			}
			if tc.symlink {
				if err := os.Symlink(filepath.Join(root, "one"), profileTwo); err != nil {
					t.Fatalf("creating profile symlink: %v", err)
				}
			}
			dir := t.TempDir()
			isolationWriteTemplate(t, dir, "one.yaml", isolationTemplateYAML(tc.idOne, "synthetic_one", profileOne))
			isolationWriteTemplate(t, dir, "two.yaml", isolationTemplateYAML(tc.idTwo, "synthetic_two", profileTwo))
			if _, err := config.LoadDirWithCatalog(dir, isolationGetenv, nil); err == nil {
				t.Fatal("LoadDirWithCatalog accepted aliased agent IDs or profile paths")
			}
		})
	}
}

func TestIsolationAcceptanceProgrammaticSetValidatesIndependently(t *testing.T) {
	oneDir, twoDir := isolationProfileDirs(t)
	base := config.Template{
		ID:      "agent-one",
		DryRun:  true,
		Threads: config.ThreadsConfig{Username: "synthetic_one", ProfileDir: oneDir},
	}
	other := base
	other.ID = "agent-two"
	other.Threads = config.ThreadsConfig{Username: "synthetic_two", ProfileDir: twoDir}

	if err := config.ValidateTemplates([]config.Template{base, other}); err != nil {
		t.Fatalf("ValidateTemplates rejected distinct programmatic identities: %v", err)
	}

	duplicate := other
	duplicate.Threads.Username = " @SYNTHETIC_ONE "
	if err := config.ValidateTemplates([]config.Template{base, duplicate}); err == nil {
		t.Fatal("ValidateTemplates accepted duplicate normalized usernames")
	}
	missing := other
	missing.Threads.Username = ""
	if err := config.ValidateTemplates([]config.Template{base, missing}); err == nil {
		t.Fatal("ValidateTemplates accepted a missing multi-agent username")
	}
	overlap := other
	overlap.Threads.ProfileDir = filepath.Join(oneDir, "nested")
	if err := config.ValidateTemplates([]config.Template{base, overlap}); err == nil {
		t.Fatal("ValidateTemplates accepted overlapping browser profiles")
	}
}

func TestIsolationAcceptanceLegacySingleTemplateMayOmitUsername(t *testing.T) {
	dir := t.TempDir()
	profileDir, _ := isolationProfileDirs(t)
	isolationWriteTemplate(t, dir, "one.yaml", isolationTemplateYAML("agent-one", "", profileDir))
	templates, err := config.LoadDirWithCatalog(dir, isolationGetenv, nil)
	if err != nil {
		t.Fatalf("legacy single template without username was rejected: %v", err)
	}
	if len(templates) != 1 || templates[0].Threads.Username != "" {
		t.Fatalf("legacy template username = %q, want empty", templates[0].Threads.Username)
	}
}

func TestIsolationAcceptanceIdentityIsNeverInferred(t *testing.T) {
	// IDs and persona names that look like usernames are not identities. A
	// multi-agent set must fail rather than guess an account from either field.
	profileOne, profileTwo := isolationProfileDirs(t)
	oneDir := t.TempDir()
	isolationWriteTemplate(t, oneDir, "one.yaml", isolationTemplateYAML("synthetic_one", "synthetic_one", profileOne))
	isolationWriteTemplate(t, oneDir, "two.yaml", isolationTemplateYAML("synthetic_two", "", profileTwo))
	if _, err := config.LoadDirWithCatalog(oneDir, isolationGetenv, nil); err == nil {
		t.Fatal("loader inferred a missing username from the agent ID")
	}

	personaDir := t.TempDir()
	yaml := strings.Replace(isolationTemplateYAML("agent-two", "", profileTwo), "  name: Synthetic Persona", "  name: synthetic_two", 1)
	isolationWriteTemplate(t, personaDir, "one.yaml", isolationTemplateYAML("agent-one", "synthetic_one", profileOne))
	isolationWriteTemplate(t, personaDir, "two.yaml", yaml)
	if _, err := config.LoadDirWithCatalog(personaDir, isolationGetenv, nil); err == nil {
		t.Fatal("loader inferred a missing username from the persona name")
	}
}

func TestIsolationAcceptancePublicLoaderReadsNoSecrets(t *testing.T) {
	dir := t.TempDir()
	profileOne, profileTwo := isolationProfileDirs(t)
	isolationWriteTemplate(t, dir, "one.yaml", isolationTemplateYAML("agent-one", "synthetic_one", profileOne))
	isolationWriteTemplate(t, dir, "two.yaml", isolationTemplateYAML("agent-two", "synthetic_two", profileTwo))

	templates, err := config.LoadPublicDirWithCatalog(dir, nil)
	if err != nil {
		t.Fatalf("LoadPublicDirWithCatalog failed: %v", err)
	}
	for _, tmpl := range templates {
		if tmpl.LLM.APIKey.Expose() != "" {
			t.Fatalf("public loader exposed an LLM key for %s", tmpl.ID)
		}
		if tmpl.Telegram.BotToken.Expose() != "" || tmpl.Telegram.OwnerChatID.Expose() != "" {
			t.Fatalf("public loader exposed Telegram secrets for %s", tmpl.ID)
		}
	}
}

func TestIsolationAcceptanceTeasingTemplateIsDistinctAndNonInteracting(t *testing.T) {
	path := filepath.Join("..", "..", "agents", "teasing.example.yaml")
	tmpl, err := config.LoadFileWithCatalog(path, isolationGetenv, nil)
	if err != nil {
		t.Fatalf("loading canonical teasing template: %v", err)
	}
	if tmpl.ID != "teasing" || tmpl.Threads.Username != "synthetic_teasing" || tmpl.Threads.ProfileDir != "data/profiles/teasing" {
		t.Fatalf("teasing identity = %q/%q/%q", tmpl.ID, tmpl.Threads.Username, tmpl.Threads.ProfileDir)
	}
	if !tmpl.DryRun || tmpl.Telegram.Enabled {
		t.Fatalf("teasing template must be dry-run with Telegram disabled: dry_run=%v telegram=%v", tmpl.DryRun, tmpl.Telegram.Enabled)
	}
	if tmpl.LLM.APIKeyEnv != "LLM_API_KEY" || tmpl.LLM.APIKey.Expose() != "synthetic-isolation-test-key" {
		t.Fatalf("teasing template must use the canonical inline key environment")
	}
	if tmpl.Persona.Name == "" || strings.EqualFold(tmpl.Persona.Name, "Kaito") {
		t.Fatalf("teasing persona must be distinct from the builder template: %q", tmpl.Persona.Name)
	}
	joined := strings.Join(append(append(append([]string{tmpl.Goal, tmpl.Persona.Identity, tmpl.Persona.Voice}, tmpl.Persona.Topics...), tmpl.Persona.StyleRules...), tmpl.Persona.AvoidTopics...), "\n")
	joined += "\n" + strings.Join(tmpl.Persona.GoodPosts, "\n") + "\n" + strings.Join(tmpl.Persona.GoodReplies, "\n") + "\n" + strings.Join(tmpl.SeedPlaybook, "\n")
	if strings.Contains(strings.ToLower(joined), "kaito") || strings.Contains(strings.ToLower(joined), "solo-builder") {
		t.Fatal("teasing template references the other configured account")
	}
	if strings.Contains(joined, "http://") || strings.Contains(joined, "https://") || strings.Contains(joined, "www.") {
		t.Fatal("teasing template embeds a promotion link")
	}
	if !strings.Contains(strings.ToLower(joined), "tài khoản thí nghiệm còn lại") {
		t.Fatal("teasing template lacks an explicit no-interaction rule")
	}
}
