package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/canhta/threadwalker/internal/config"
)

func TestValidateTemplatesRequiresDistinctIdentitiesAndProfiles(t *testing.T) {
	root := t.TempDir()
	a := config.Template{ID: "a", Threads: config.ThreadsConfig{Username: "@Fixture_A", ProfileDir: filepath.Join(root, "a")}}
	b := config.Template{ID: "b", Threads: config.ThreadsConfig{Username: "fixture_b", ProfileDir: filepath.Join(root, "b")}}
	if err := config.ValidateTemplates([]config.Template{a, b}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*config.Template)
	}{
		{"id", func(b *config.Template) { b.ID = a.ID }},
		{"username", func(b *config.Template) { b.Threads.Username = "FIXTURE_A" }},
		{"missing username", func(b *config.Template) { b.Threads.Username = "" }},
		{"invalid username", func(b *config.Template) { b.Threads.Username = "https://private.invalid" }},
		{"missing profile", func(b *config.Template) { b.Threads.ProfileDir = "" }},
		{"same profile", func(b *config.Template) { b.Threads.ProfileDir = a.Threads.ProfileDir }},
		{"nested profile", func(b *config.Template) { b.Threads.ProfileDir = filepath.Join(a.Threads.ProfileDir, "child") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			other := b
			tc.change(&other)
			if config.ValidateTemplates([]config.Template{a, other}) == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
	if err := os.Mkdir(a.Threads.ProfileDir, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(a.Threads.ProfileDir, alias); err != nil {
		t.Fatal(err)
	}
	b.Threads.ProfileDir = filepath.Join(alias, "not-created")
	if config.ValidateTemplates([]config.Template{a, b}) == nil {
		t.Fatal("symlink parent alias accepted")
	}
	if err := config.ValidateTemplates([]config.Template{{ID: "legacy"}}); err != nil {
		t.Fatal(err)
	}
}

func TestPublicConfigLoadsWithoutResolvingAnySecrets(t *testing.T) {
	agents, err := config.LoadPublicDirWithCatalog(filepath.Join("..", "..", "agents"), nil)
	if err != nil || len(agents) == 0 {
		t.Fatalf("metadata: %+v %v", agents, err)
	}
	if agents[0].ID != "solo-builder" || agents[0].Limits.MaxUSDPerDay != 0.5 || agents[0].LLM.APIKey.Expose() != "" || agents[0].Telegram.BotToken.Expose() != "" {
		t.Fatalf("wrong public metadata: %+v", agents)
	}
}
