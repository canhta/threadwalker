package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun_CheckPassesWithValidEnvAndLeaksNoSecrets(t *testing.T) {
	secrets := map[string]string{
		"LLM_API_KEY":            "super-secret-deepseek-key-999",
		"TELEGRAM_BOT_TOKEN":     "super-secret-telegram-token-888",
		"TELEGRAM_OWNER_CHAT_ID": "super-secret-owner-chat-777",
	}
	getenv := func(k string) string {
		return secrets[k]
	}

	agentsDir := filepath.Join("..", "..", "agents")
	dbPath := filepath.Join(t.TempDir(), "threadwalker.db")

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"check", "-agents", agentsDir, "-db", dbPath}, getenv, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run(check) = %d, want 0; stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}

	combined := stdout.String() + "\n" + stderr.String()
	for name, secretVal := range secrets {
		if strings.Contains(combined, secretVal) {
			t.Errorf("output leaked secret %s (%q): %s", name, secretVal, combined)
		}
	}
}

func TestRun_CheckFailsAndPrintsEachErrorWhenInvalid(t *testing.T) {
	getenv := func(string) string { return "" }
	agentsDir := filepath.Join("..", "..", "agents")
	dbPath := filepath.Join(t.TempDir(), "threadwalker.db")

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"check", "-agents", agentsDir, "-db", dbPath}, getenv, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("run(check) with unset secrets = %d, want 1", code)
	}

	errOut := stderr.String()
	for _, want := range []string{"llm.api_key_env", "telegram.bot_token_env", "telegram.owner_chat_id_env"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr %q does not contain expected error for %q", errOut, want)
		}
	}
}

func TestRun_UnknownCommandExitsOne(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"unknown"}, func(string) string { return "" }, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("run(unknown) = %d, want 1", code)
	}
}

func TestRun_NoArgsAndBadFlagsAndBadDBPathExitOne(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), nil, func(string) string { return "" }, &stdout, &stderr); code != 1 {
		t.Fatalf("run(nil) = %d, want 1", code)
	}
	if code := run(context.Background(), []string{"check", "-unknown-flag"}, func(string) string { return "" }, &stdout, &stderr); code != 1 {
		t.Fatalf("run(check -unknown-flag) = %d, want 1", code)
	}
	// Passing a directory path as the DB file path causes store.Open to fail.
	badDBPath := t.TempDir()
	agentsDir := filepath.Join("..", "..", "agents")
	if code := run(context.Background(), []string{"check", "-agents", agentsDir, "-db", badDBPath}, func(string) string { return "val" }, &stdout, &stderr); code != 1 {
		t.Fatalf("run(check with directory as db) = %d, want 1", code)
	}
}
