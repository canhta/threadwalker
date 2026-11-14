package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrowserConfigurationExplicitAndFailClosed(t *testing.T) {
	raw, err := os.ReadFile("testdata/fixture.agent.yaml")
	if err != nil {
		t.Fatal(err)
	}
	base := strings.Replace(string(raw), "  backend: stub # api additionally requires base_url, executable_path and agent_browser_path.\n", "", 1)
	for _, tc := range []struct {
		name, extra string
		valid       bool
	}{
		{"default", "", true},
		{"api", "  backend: api\n  base_url: https://www.threads.com\n  executable_path: /synthetic/chrome\n  agent_browser_path: /synthetic/agent-browser\n", true},
		{"stub_keeps_api_settings", "  backend: stub\n  access_token_env: T\n  user_id_env: U\n  base_url: https://www.threads.com\n  executable_path: /x\n  agent_browser_path: relative\n", true},
		{"api_missing_cli", "  backend: api\n  base_url: https://www.threads.com\n  executable_path: /synthetic/chrome\n", false},
		{"api_relative_cli", "  backend: api\n  base_url: https://www.threads.com\n  executable_path: /synthetic/chrome\n  agent_browser_path: agent-browser\n", false},
		{"api_missing_chrome", "  backend: api\n  base_url: https://www.threads.com\n  agent_browser_path: /synthetic/agent-browser\n", false},
		{"api_threads_net", "  backend: api\n  base_url: https://threads.net\n  executable_path: /synthetic/chrome\n  agent_browser_path: /synthetic/agent-browser\n", true},
		{"api_http_rejected", "  backend: api\n  base_url: http://www.threads.com\n  executable_path: /synthetic/chrome\n  agent_browser_path: /synthetic/agent-browser\n", false},
		{"wrong_host", "  backend: api\n  base_url: https://example.com\n  executable_path: /synthetic/chrome\n  agent_browser_path: /synthetic/agent-browser\n", false},
		{"backend_browser_rejected", "  backend: browser\n  base_url: https://www.threads.com\n  executable_path: /synthetic/chrome\n", false},
		{"unknown", "  backend: auto\n", false},
		{"missing_origin", "  backend: api\n  executable_path: /synthetic/chrome\n  agent_browser_path: /synthetic/agent-browser\n", false},
		{"credentials", "  backend: api\n  base_url: https://private:secret@fixture.invalid\n  executable_path: /synthetic/chrome\n", false},
		{"invalid_origin", "  backend: api\n  base_url: file:///tmp/fixture\n  executable_path: /synthetic/chrome\n", false},
		{"missing_binary", "  backend: api\n  base_url: https://fixture.invalid\n", false},
		{"sandbox_choice", "  no_sandbox: true\n", true},
		{"invalid_sandbox_choice", "  no_sandbox: maybe\n", false},
		{"path_is_not_origin", "  backend: api\n  base_url: https://fixture.invalid/path\n  executable_path: /synthetic/chrome\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.yaml")
			input := strings.Replace(base, "threads:\n", "threads:\n"+tc.extra, 1)
			if err := os.WriteFile(path, []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			tmpl, err := loadFile(path, Options{Getenv: func(string) string { return "synthetic-secret" }})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if err == nil && tc.name == "default" && tmpl.Threads.Backend != "stub" {
				t.Fatal("default must be offline stub")
			}
			if err == nil && tc.name == "sandbox_choice" && !tmpl.Threads.NoSandbox {
				t.Fatal("explicit sandbox choice lost")
			}
			if err == nil && tc.name == "api" && (tmpl.Threads.Backend != "api" || tmpl.Threads.AgentBrowserPath != "/synthetic/agent-browser" || tmpl.Threads.ExecutablePath != "/synthetic/chrome") {
				t.Fatalf("agent-browser config lost: %+v", tmpl.Threads)
			}
			if err != nil && strings.Contains(err.Error(), "private:secret") {
				t.Fatal("origin credentials leaked")
			}
		})
	}
}

func TestAPIBackendAllowsLiveWrites(t *testing.T) {
	raw, err := os.ReadFile("testdata/fixture.agent.yaml")
	if err != nil {
		t.Fatal(err)
	}
	input := strings.Replace(string(raw), "  backend: stub # api additionally requires base_url, executable_path and agent_browser_path.\n", "", 1)
	input = strings.Replace(input, "threads:\n", "threads:\n  backend: api\n  base_url: https://www.threads.com\n  executable_path: /synthetic/chrome\n  agent_browser_path: /synthetic/agent-browser\n", 1)
	input = strings.Replace(input, "dry_run: true", "dry_run: false", 1)
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err = os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	tmpl, err := loadFile(path, Options{Getenv: func(string) string { return "synthetic-secret" }})
	if err != nil {
		t.Fatalf("measured live write profile rejected: %v", err)
	}
	if tmpl.DryRun || tmpl.Threads.Backend != "api" {
		t.Fatalf("live write config lost: %+v", tmpl.Threads)
	}
}

func TestAPIBackendReadsTokenAndUserIDFromTheEnvironment(t *testing.T) {
	raw, err := os.ReadFile("testdata/fixture.agent.yaml")
	if err != nil {
		t.Fatal(err)
	}
	input := strings.Replace(string(raw), "  backend: stub # api additionally requires base_url, executable_path and agent_browser_path.\n", "", 1)
	input = strings.Replace(input, "threads:\n", "threads:\n  backend: api\n  base_url: https://www.threads.com\n  executable_path: /synthetic/chrome\n  agent_browser_path: /synthetic/agent-browser\n", 1)
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err = os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"THREADS_ACCESS_TOKEN": "synthetic-threads-token", "THREADS_USER_ID": "1000000000000001", "LLM_API_KEY": "k", "OPENAI_API_KEY": "k", "TELEGRAM_BOT_TOKEN": "k", "TELEGRAM_OWNER_CHAT_ID": "1"}
	get := func(name string) string { return env[name] }
	tmpl, err := loadFile(path, Options{Getenv: get})
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.Threads.AccessToken.Expose() != "synthetic-threads-token" || tmpl.Threads.UserID != "1000000000000001" {
		t.Fatalf("credentials lost: %+v", tmpl.Threads)
	}
	if got := NewRedactor(tmpl).String("x synthetic-threads-token y"); got != "x [REDACTED] y" {
		t.Fatalf("token not covered by the redactor: %q", got)
	}
	missing := map[string]string{}
	for k, v := range env {
		if k != "THREADS_USER_ID" {
			missing[k] = v
		}
	}
	if _, err = loadFile(path, Options{Getenv: func(n string) string { return missing[n] }}); err == nil {
		t.Fatal("THREADS_USER_ID unset accepted")
	}
	// The token seed is optional: a sealed token stored by OAuth replaces it.
	delete(env, "THREADS_ACCESS_TOKEN")
	if _, err = loadFile(path, Options{Getenv: get}); err != nil {
		t.Fatalf("unset THREADS_ACCESS_TOKEN rejected: %v", err)
	}
}
