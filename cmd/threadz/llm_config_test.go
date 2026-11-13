package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	catalogFakeTelegramToken = "catalog-fake-telegram-token-888"
	catalogFakeOwnerChatID   = "catalog-fake-owner-chat-777"
	catalogFakeFirstKey      = "catalog-fake-first-key-111"
	catalogFakeInlineKey     = "catalog-fake-inline-key-333"
)

// catalogBaseEnv returns the non-LLM environment every check needs (Telegram).
func catalogBaseEnv() map[string]string {
	return map[string]string{
		"OPENAI_API_KEY":         catalogFakeFirstKey,
		"TELEGRAM_BOT_TOKEN":     catalogFakeTelegramToken,
		"TELEGRAM_OWNER_CHAT_ID": catalogFakeOwnerChatID,
	}
}

// catalogReadExample returns the canonical agent template that holds the
// non-LLM fields tests reuse instead of copying the whole template.
func catalogReadExample(t *testing.T) string {
	t.Helper()
	path := filepath.Join("testdata", "fixture.agent.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading canonical example template: %v", err)
	}
	return string(raw)
}

// catalogAgentFile builds an agent template in dir from the canonical example,
// replacing only the llm block (between "llm:" and "limits:") and its id.
func catalogAgentFile(t *testing.T, dir, filename, id, llmBlock string) string {
	t.Helper()
	base := catalogReadExample(t)

	const llmPrefix = "\nllm:\n"
	start := strings.Index(base, llmPrefix)
	if start < 0 {
		t.Fatalf("canonical example has no %q block", strings.TrimSpace(llmPrefix))
	}
	rest := base[start+len(llmPrefix):]
	relEnd := strings.Index(rest, "\nlimits:")
	if relEnd < 0 {
		t.Fatalf("canonical example has no llm block end before limits:")
	}
	end := start + len(llmPrefix) + relEnd

	out := base[:start] + "\n" + strings.TrimRight(llmBlock, "\n") + "\n" + base[end:]

	const oldID = "id: fixture"
	if !strings.Contains(out, oldID) {
		t.Fatalf("canonical example does not start with %q", oldID)
	}
	out = strings.Replace(out, oldID, "id: "+id, 1)
	out = strings.Replace(out, "username: synthetic_builder", "username: "+strings.ReplaceAll(id, "-", "_"), 1)
	out = strings.Replace(out, "data/profiles/fixture", "data/profiles/"+id, 1)

	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		t.Fatalf("writing agent template %s: %v", filename, err)
	}
	return path
}

func catalogCheckArgs(agentsDir, dbPath string, extra ...string) []string {
	args := []string{"check", "-agents", agentsDir, "-db", dbPath}
	return append(args, extra...)
}

func catalogRunCheck(t *testing.T, args []string, env map[string]string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(context.Background(), args, func(k string) string { return env[k] }, &out, &errOut)
	return code, out.String(), errOut.String()
}

func catalogAssertNoSecrets(t *testing.T, combined string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(combined, secret) {
			t.Errorf("output leaked secret value %q: %s", secret, combined)
		}
	}
}

func TestCheckRejectsCatalogProfile(t *testing.T) {
	agentsDir := t.TempDir()
	catalogAgentFile(t, agentsDir, "agent.yaml", "catalog-fast-agent", "llm:\n  profile: fast\n")
	env := catalogBaseEnv()
	dbPath := filepath.Join(t.TempDir(), "threadz.db")
	code, stdout, stderr := catalogRunCheck(t, catalogCheckArgs(agentsDir, dbPath), env)
	if code != 1 || !strings.Contains(stderr, "llm.profile") {
		t.Fatalf("check with llm.profile = %d, want 1 naming llm.profile; stderr=%s stdout=%s", code, stderr, stdout)
	}
}

func TestRunAndCheckHaveNoLLMsFlag(t *testing.T) {
	agentsDir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "threadz.db")
	for _, command := range []string{"check", "run"} {
		code, _, _ := catalogRunCheck(t, []string{command, "-agents", agentsDir, "-db", dbPath, "-llms", "x.yaml"}, catalogBaseEnv())
		if code != exitUsage {
			t.Errorf("%s -llms = %d, want %d", command, code, exitUsage)
		}
	}
}

func TestCheckInlineLLMNeverPrintsKey(t *testing.T) {
	agentsDir := t.TempDir()
	inline := "llm:\n" +
		"  base_url: https://inline.example.test/v1\n" +
		"  model: inline-model\n" +
		"  api_key_env: INLINE_LLM_KEY\n" +
		"  price_per_mtok_in_usd: 0.5\n" +
		"  price_per_mtok_out_usd: 1.5\n"
	catalogAgentFile(t, agentsDir, "agent.yaml", "catalog-inline-agent", inline)
	env := catalogBaseEnv()
	env["INLINE_LLM_KEY"] = catalogFakeInlineKey
	dbPath := filepath.Join(t.TempDir(), "threadz.db")
	code, stdout, stderr := catalogRunCheck(t, catalogCheckArgs(agentsDir, dbPath), env)
	if code != 0 || !strings.Contains(stdout, "catalog-inline-agent") {
		t.Fatalf("check with inline llm = %d; stderr=%s stdout=%s", code, stderr, stdout)
	}
	catalogAssertNoSecrets(t, stdout+stderr, catalogFakeInlineKey, catalogFakeTelegramToken, catalogFakeOwnerChatID)
}
