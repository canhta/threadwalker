package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrowserConfigurationExplicitAndFailClosed(t *testing.T) {
	raw, err := os.ReadFile("../../agents/solo-builder.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	base := strings.Replace(string(raw), "  backend: stub # Browser mode additionally requires base_url and executable_path.\n", "", 1)
	for _, tc := range []struct {
		name, extra string
		valid       bool
	}{
		{"default", "", true},
		{"browser", "  backend: browser\n  base_url: http://127.0.0.1:1234\n  executable_path: /synthetic/chrome\n", true},
		{"agent_browser", "  backend: agent_browser\n  base_url: http://127.0.0.1:1234\n  executable_path: /synthetic/chrome\n  agent_browser_path: /synthetic/agent-browser\n", true},
		{"agent_browser_missing_cli", "  backend: agent_browser\n  base_url: http://127.0.0.1:1234\n  executable_path: /synthetic/chrome\n", false},
		{"agent_browser_relative_cli", "  backend: agent_browser\n  base_url: http://127.0.0.1:1234\n  executable_path: /synthetic/chrome\n  agent_browser_path: agent-browser\n", false},
		{"agent_browser_missing_chrome", "  backend: agent_browser\n  base_url: http://127.0.0.1:1234\n  agent_browser_path: /synthetic/agent-browser\n", false},
		{"threads_live", "  backend: agent_browser\n  base_url: https://www.threads.com\n  executable_path: /synthetic/chrome\n  agent_browser_path: /synthetic/agent-browser\n  page_profile: threads\n", true},
		{"threads_live_wrong_host", "  backend: agent_browser\n  base_url: https://example.com\n  executable_path: /synthetic/chrome\n  agent_browser_path: /synthetic/agent-browser\n  page_profile: threads\n", false},
		{"threads_live_wrong_backend", "  backend: browser\n  base_url: https://www.threads.com\n  executable_path: /synthetic/chrome\n  page_profile: threads\n", false},
		{"threads_unknown_profile", "  backend: agent_browser\n  base_url: https://www.threads.com\n  executable_path: /synthetic/chrome\n  agent_browser_path: /synthetic/agent-browser\n  page_profile: invented\n", false},
		{"unknown", "  backend: auto\n", false},
		{"missing_origin", "  backend: browser\n  executable_path: /synthetic/chrome\n", false},
		{"credentials", "  backend: browser\n  base_url: https://private:secret@fixture.invalid\n  executable_path: /synthetic/chrome\n", false},
		{"invalid_origin", "  backend: browser\n  base_url: file:///tmp/fixture\n  executable_path: /synthetic/chrome\n", false},
		{"missing_binary", "  backend: browser\n  base_url: https://fixture.invalid\n", false},
		{"sandbox_choice", "  no_sandbox: true\n", true},
		{"invalid_sandbox_choice", "  no_sandbox: maybe\n", false},
		{"path_is_not_origin", "  backend: browser\n  base_url: https://fixture.invalid/path\n  executable_path: /synthetic/chrome\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.yaml")
			input := strings.Replace(base, "threads:\n", "threads:\n"+tc.extra, 1)
			if err := os.WriteFile(path, []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			tmpl, err := LoadFile(path, func(string) string { return "synthetic-secret" })
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if err == nil && tc.name == "default" && tmpl.Threads.Backend != "stub" {
				t.Fatal("default must be offline stub")
			}
			if err == nil && tc.name == "sandbox_choice" && !tmpl.Threads.NoSandbox {
				t.Fatal("explicit sandbox choice lost")
			}
			if err == nil && tc.name == "browser" && (tmpl.Threads.Backend != "browser" || tmpl.Threads.BaseURL != "http://127.0.0.1:1234" || tmpl.Threads.ExecutablePath != "/synthetic/chrome") {
				t.Fatalf("config lost: %+v", tmpl.Threads)
			}
			if err == nil && tc.name == "agent_browser" && (tmpl.Threads.Backend != "agent_browser" || tmpl.Threads.AgentBrowserPath != "/synthetic/agent-browser" || tmpl.Threads.ExecutablePath != "/synthetic/chrome") {
				t.Fatalf("agent-browser config lost: %+v", tmpl.Threads)
			}
			if err == nil && tc.name == "threads_live" && (tmpl.Threads.PageProfile != "threads" || tmpl.Threads.Backend != "agent_browser") {
				t.Fatalf("live profile lost: %+v", tmpl.Threads)
			}
			if err != nil && strings.Contains(err.Error(), "private:secret") {
				t.Fatal("origin credentials leaked")
			}
		})
	}
}

func TestThreadsLiveProfileRequiresDryRun(t *testing.T) {
	raw, err := os.ReadFile("../../agents/solo-builder.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	input := strings.Replace(string(raw), "  backend: stub # Browser mode additionally requires base_url and executable_path.\n", "", 1)
	input = strings.Replace(input, "threads:\n", "threads:\n  backend: agent_browser\n  base_url: https://www.threads.com\n  executable_path: /synthetic/chrome\n  agent_browser_path: /synthetic/agent-browser\n  page_profile: threads\n", 1)
	input = strings.Replace(input, "dry_run: true", "dry_run: false", 1)
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err = os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadFile(path, func(string) string { return "synthetic-secret" }); err == nil || !strings.Contains(err.Error(), "dry_run true") {
		t.Fatalf("non-dry-run live profile accepted: %v", err)
	}
}
