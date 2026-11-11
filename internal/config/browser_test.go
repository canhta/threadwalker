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
		{"unknown", "  backend: auto\n", false},
		{"missing_origin", "  backend: browser\n  executable_path: /synthetic/chrome\n", false},
		{"credentials", "  backend: browser\n  base_url: https://private:secret@fixture.invalid\n  executable_path: /synthetic/chrome\n", false},
		{"invalid_origin", "  backend: browser\n  base_url: file:///tmp/fixture\n  executable_path: /synthetic/chrome\n", false},
		{"missing_binary", "  backend: browser\n  base_url: https://fixture.invalid\n", false},
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
			if err == nil && tc.name == "browser" && (tmpl.Threads.Backend != "browser" || tmpl.Threads.BaseURL != "http://127.0.0.1:1234" || tmpl.Threads.ExecutablePath != "/synthetic/chrome") {
				t.Fatalf("config lost: %+v", tmpl.Threads)
			}
			if err != nil && strings.Contains(err.Error(), "private:secret") {
				t.Fatal("origin credentials leaked")
			}
		})
	}
}
