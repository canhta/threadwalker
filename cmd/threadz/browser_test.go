package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfflineBrowserConfigurationUsesOnlyInProcessStubs(t *testing.T) {
	raw, err := os.ReadFile("testdata/fixture.agent.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// An absent executable makes an accidental browser launch observable.
	input := strings.Replace(string(raw), "  backend: stub # api additionally requires base_url, executable_path and agent_browser_path.\n", "", 1)
	input = strings.Replace(input, "threads:\n", "threads:\n  backend: api\n  base_url: https://www.threads.com\n  agent_browser_path: /absent/agent-browser\n  executable_path: /absent/browser-must-never-start\n", 1)
	if err = os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"run", "-once", "-offline", "-agents", dir}, noSecretEnv(t, "offline read credentials"), &out, &stderr)
	if code != 0 {
		t.Fatalf("code=%d err=%s", code, &stderr)
	}
	if strings.Contains(out.String()+stderr.String(), "threads.com") {
		t.Fatal("offline mode opened browser")
	}
}
