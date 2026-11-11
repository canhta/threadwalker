package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// catalogTestCatalog is the acceptance catalog: arbitrary provider IDs, arbitrary
// profile IDs, two API protocols and independent key environment-variable names.
// Two models (fast, cheap) share the primary provider.
const catalogTestCatalog = `providers:
  primary:
    protocol: openai
    base_url: https://primary.example.test/v1
    api_key_env: FIRST_KEY
  secondary:
    protocol: anthropic
    base_url: https://secondary.example.test
    api_key_env: SECOND_KEY
models:
  fast:
    provider: primary
    model: arbitrary-fast-model
    price_per_mtok_in_usd: 0.2
    price_per_mtok_out_usd: 0.4
  backup:
    provider: secondary
    model: arbitrary-backup-model
    price_per_mtok_in_usd: 1
    price_per_mtok_out_usd: 2
  cheap:
    provider: primary
    model: arbitrary-cheap-model
    price_per_mtok_in_usd: 0.1
    price_per_mtok_out_usd: 0.2
`

const (
	catalogFakeTelegramToken = "catalog-fake-telegram-token-888"
	catalogFakeOwnerChatID   = "catalog-fake-owner-chat-777"
	catalogFakeFirstKey      = "catalog-fake-first-key-111"
	catalogFakeSecondKey     = "catalog-fake-second-key-222"
	catalogFakeLegacyKey     = "catalog-fake-legacy-key-333"
)

// catalogBaseEnv returns the non-LLM environment every check needs (Telegram).
func catalogBaseEnv() map[string]string {
	return map[string]string{
		"TELEGRAM_BOT_TOKEN":     catalogFakeTelegramToken,
		"TELEGRAM_OWNER_CHAT_ID": catalogFakeOwnerChatID,
	}
}

// catalogWriteFile writes body into a fresh temporary directory and returns the path.
func catalogWriteFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return path
}

// catalogReadExample returns the canonical agent template that holds the
// non-LLM fields tests reuse instead of copying the whole template.
func catalogReadExample(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "agents", "solo-builder.example.yaml")
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

	const oldID = "id: solo-builder"
	if !strings.Contains(out, oldID) {
		t.Fatalf("canonical example does not start with %q", oldID)
	}
	out = strings.Replace(out, oldID, "id: "+id, 1)

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

func catalogContainsAny(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

func catalogAssertNoSecrets(t *testing.T, combined string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(combined, secret) {
			t.Errorf("output leaked secret value %q: %s", secret, combined)
		}
	}
}

func TestCatalogCheck_SelectsProfileWithArbitraryKeyNames(t *testing.T) {
	catalogPath := catalogWriteFile(t, "catalog.yaml", catalogTestCatalog)
	agentsDir := t.TempDir()
	catalogAgentFile(t, agentsDir, "agent.yaml", "catalog-fast-agent", "llm:\n  profile: fast\n")

	env := catalogBaseEnv()
	env["FIRST_KEY"] = catalogFakeFirstKey

	dbPath := filepath.Join(t.TempDir(), "threadwalker.db")
	code, stdout, stderr := catalogRunCheck(t, catalogCheckArgs(agentsDir, dbPath, "-llms", catalogPath), env)
	if code != 0 {
		t.Fatalf("run(check -llms) = %d, want 0; stderr=%s stdout=%s", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "catalog-fast-agent") {
		t.Errorf("stdout does not report the selected agent id %q: %s", "catalog-fast-agent", stdout)
	}
	catalogAssertNoSecrets(t, stdout+stderr, catalogFakeFirstKey, catalogFakeTelegramToken, catalogFakeOwnerChatID)
}

func TestCatalogCheck_MultipleTemplatesSelectDistinctProfiles(t *testing.T) {
	catalogPath := catalogWriteFile(t, "catalog.yaml", catalogTestCatalog)
	agentsDir := t.TempDir()
	catalogAgentFile(t, agentsDir, "fast.yaml", "catalog-agent-fast", "llm:\n  profile: fast\n")
	catalogAgentFile(t, agentsDir, "backup.yaml", "catalog-agent-backup", "llm:\n  profile: backup\n")

	env := catalogBaseEnv()
	env["FIRST_KEY"] = catalogFakeFirstKey
	env["SECOND_KEY"] = catalogFakeSecondKey

	dbPath := filepath.Join(t.TempDir(), "threadwalker.db")
	code, stdout, stderr := catalogRunCheck(t, catalogCheckArgs(agentsDir, dbPath, "-llms", catalogPath), env)
	if code != 0 {
		t.Fatalf("run(check -llms with two profiles) = %d, want 0; stderr=%s stdout=%s", code, stderr, stdout)
	}
	for _, id := range []string{"catalog-agent-fast", "catalog-agent-backup"} {
		if !strings.Contains(stdout, id) {
			t.Errorf("stdout does not report agent id %q: %s", id, stdout)
		}
	}
}

func TestCatalogCheck_SecondModelSharesOneProvider(t *testing.T) {
	catalogPath := catalogWriteFile(t, "catalog.yaml", catalogTestCatalog)
	agentsDir := t.TempDir()
	catalogAgentFile(t, agentsDir, "agent.yaml", "catalog-cheap-agent", "llm:\n  profile: cheap\n")

	env := catalogBaseEnv()
	env["FIRST_KEY"] = catalogFakeFirstKey

	dbPath := filepath.Join(t.TempDir(), "threadwalker.db")
	code, stdout, stderr := catalogRunCheck(t, catalogCheckArgs(agentsDir, dbPath, "-llms", catalogPath), env)
	if code != 0 {
		t.Fatalf("run(check -llms with a second model on the same provider) = %d, want 0; stderr=%s stdout=%s", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "catalog-cheap-agent") {
		t.Errorf("stdout does not report agent id %q: %s", "catalog-cheap-agent", stdout)
	}
}

func TestCatalogCheck_UnusedProviderMissingKeyIsIgnored(t *testing.T) {
	catalogPath := catalogWriteFile(t, "catalog.yaml", catalogTestCatalog)
	agentsDir := t.TempDir()
	catalogAgentFile(t, agentsDir, "agent.yaml", "catalog-fast-agent", "llm:\n  profile: fast\n")

	// SECOND_KEY belongs to the unused secondary provider and is deliberately unset.
	env := catalogBaseEnv()
	env["FIRST_KEY"] = catalogFakeFirstKey

	dbPath := filepath.Join(t.TempDir(), "threadwalker.db")
	code, stdout, stderr := catalogRunCheck(t, catalogCheckArgs(agentsDir, dbPath, "-llms", catalogPath), env)
	if code != 0 {
		t.Fatalf("run(check -llms) = %d, want 0 while unused provider key is unset; stderr=%s stdout=%s", code, stderr, stdout)
	}
}

func TestCatalogCheck_SelectedProviderMissingKeyFails(t *testing.T) {
	catalogPath := catalogWriteFile(t, "catalog.yaml", catalogTestCatalog)
	agentsDir := t.TempDir()
	catalogAgentFile(t, agentsDir, "agent.yaml", "catalog-backup-agent", "llm:\n  profile: backup\n")

	// backup selects the secondary provider, whose key is unset.
	env := catalogBaseEnv()
	env["FIRST_KEY"] = catalogFakeFirstKey

	dbPath := filepath.Join(t.TempDir(), "threadwalker.db")
	code, stdout, stderr := catalogRunCheck(t, catalogCheckArgs(agentsDir, dbPath, "-llms", catalogPath), env)
	if code != 1 {
		t.Fatalf("run(check -llms with selected provider key unset) = %d, want 1; stderr=%s stdout=%s", code, stderr, stdout)
	}
	if !strings.Contains(stderr, "SECOND_KEY") {
		t.Errorf("stderr %q does not name the missing key variable %q", stderr, "SECOND_KEY")
	}
	if !catalogContainsAny(stderr, "backup", "secondary", "profile") {
		t.Errorf("stderr %q does not identify the selected profile or provider", stderr)
	}
}

func TestCatalogCheck_MissingCatalogFileFails(t *testing.T) {
	agentsDir := t.TempDir()
	catalogAgentFile(t, agentsDir, "agent.yaml", "catalog-fast-agent", "llm:\n  profile: fast\n")

	missingPath := filepath.Join(t.TempDir(), "catalog-does-not-exist.yaml")
	env := catalogBaseEnv()
	env["FIRST_KEY"] = catalogFakeFirstKey

	dbPath := filepath.Join(t.TempDir(), "threadwalker.db")
	code, stdout, stderr := catalogRunCheck(t, catalogCheckArgs(agentsDir, dbPath, "-llms", missingPath), env)
	if code != 1 {
		t.Fatalf("run(check -llms <missing file>) = %d, want 1; stderr=%s stdout=%s", code, stderr, stdout)
	}
	if !catalogContainsAny(stderr, filepath.Base(missingPath), missingPath) {
		t.Errorf("stderr %q does not identify the missing catalog path %q", stderr, missingPath)
	}
}

func TestCatalogCheck_UnknownModelProfileFails(t *testing.T) {
	catalogPath := catalogWriteFile(t, "catalog.yaml", catalogTestCatalog)
	agentsDir := t.TempDir()
	catalogAgentFile(t, agentsDir, "agent.yaml", "catalog-unknown-agent", "llm:\n  profile: catalog-missing-profile\n")

	env := catalogBaseEnv()
	env["FIRST_KEY"] = catalogFakeFirstKey

	dbPath := filepath.Join(t.TempDir(), "threadwalker.db")
	code, stdout, stderr := catalogRunCheck(t, catalogCheckArgs(agentsDir, dbPath, "-llms", catalogPath), env)
	if code != 1 {
		t.Fatalf("run(check -llms with unknown profile) = %d, want 1; stderr=%s stdout=%s", code, stderr, stdout)
	}
	if !strings.Contains(stderr, "catalog-missing-profile") {
		t.Errorf("stderr %q does not name the unknown profile %q", stderr, "catalog-missing-profile")
	}
	if !catalogContainsAny(stderr, "profile", "model", "catalog") {
		t.Errorf("stderr %q does not describe the profile lookup failure", stderr)
	}
}

func TestCatalogCheck_ProfileWithoutCatalogFails(t *testing.T) {
	agentsDir := t.TempDir()
	catalogAgentFile(t, agentsDir, "agent.yaml", "catalog-fast-agent", "llm:\n  profile: fast\n")

	env := catalogBaseEnv()
	env["FIRST_KEY"] = catalogFakeFirstKey

	dbPath := filepath.Join(t.TempDir(), "threadwalker.db")
	code, stdout, stderr := catalogRunCheck(t, catalogCheckArgs(agentsDir, dbPath), env)
	if code != 1 {
		t.Fatalf("run(check with llm.profile but no -llms) = %d, want 1; stderr=%s stdout=%s", code, stderr, stdout)
	}
	if !strings.Contains(stderr, "profile") {
		t.Errorf("stderr %q does not name llm.profile", stderr)
	}
	if !catalogContainsAny(stderr, "catalog", "llms") {
		t.Errorf("stderr %q does not explain that a catalog is required", stderr)
	}
}

func TestCatalogCheck_ProfileMixedWithInlineSettingsFails(t *testing.T) {
	catalogPath := catalogWriteFile(t, "catalog.yaml", catalogTestCatalog)
	agentsDir := t.TempDir()
	mixed := "llm:\n  profile: fast\n  model: catalog-inline-model\n"
	catalogAgentFile(t, agentsDir, "agent.yaml", "catalog-mixed-agent", mixed)

	env := catalogBaseEnv()
	env["FIRST_KEY"] = catalogFakeFirstKey

	dbPath := filepath.Join(t.TempDir(), "threadwalker.db")
	code, stdout, stderr := catalogRunCheck(t, catalogCheckArgs(agentsDir, dbPath, "-llms", catalogPath), env)
	if code != 1 {
		t.Fatalf("run(check -llms with profile plus inline model) = %d, want 1; stderr=%s stdout=%s", code, stderr, stdout)
	}
	lowered := strings.ToLower(stderr)
	if !strings.Contains(lowered, "profile") {
		t.Errorf("stderr %q does not name the profile setting", stderr)
	}
	if !strings.Contains(lowered, "model") {
		t.Errorf("stderr %q does not name the conflicting inline model field", stderr)
	}
}

func TestCatalogCheck_LegacyInlineConfigStillWorksWithoutFlag(t *testing.T) {
	agentsDir := t.TempDir()
	legacyLLM := "llm:\n" +
		"  base_url: https://legacy.example.test/v1\n" +
		"  model: legacy-inline-model\n" +
		"  api_key_env: LEGACY_LLM_KEY\n" +
		"  price_per_mtok_in_usd: 0.5\n" +
		"  price_per_mtok_out_usd: 1.5\n"
	catalogAgentFile(t, agentsDir, "agent.yaml", "catalog-legacy-agent", legacyLLM)

	env := catalogBaseEnv()
	env["LEGACY_LLM_KEY"] = catalogFakeLegacyKey

	dbPath := filepath.Join(t.TempDir(), "threadwalker.db")
	code, stdout, stderr := catalogRunCheck(t, catalogCheckArgs(agentsDir, dbPath), env)
	if code != 0 {
		t.Fatalf("run(check with legacy inline llm and no -llms) = %d, want 0; stderr=%s stdout=%s", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "catalog-legacy-agent") {
		t.Errorf("stdout does not report agent id %q: %s", "catalog-legacy-agent", stdout)
	}
}

func TestCatalogCheck_OutputNeverContainsFakeKeyValues(t *testing.T) {
	catalogPath := catalogWriteFile(t, "catalog.yaml", catalogTestCatalog)
	agentsDir := t.TempDir()
	catalogAgentFile(t, agentsDir, "fast.yaml", "catalog-fast-agent", "llm:\n  profile: fast\n")
	catalogAgentFile(t, agentsDir, "backup.yaml", "catalog-backup-agent", "llm:\n  profile: backup\n")

	env := catalogBaseEnv()
	env["FIRST_KEY"] = catalogFakeFirstKey
	env["SECOND_KEY"] = catalogFakeSecondKey

	dbPath := filepath.Join(t.TempDir(), "threadwalker.db")
	code, stdout, stderr := catalogRunCheck(t, catalogCheckArgs(agentsDir, dbPath, "-llms", catalogPath), env)
	if code != 0 {
		t.Fatalf("run(check -llms with both providers selected) = %d, want 0; stderr=%s stdout=%s", code, stderr, stdout)
	}
	catalogAssertNoSecrets(t, stdout+stderr,
		catalogFakeFirstKey,
		catalogFakeSecondKey,
		catalogFakeTelegramToken,
		catalogFakeOwnerChatID,
	)
}
