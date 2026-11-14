package config_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The v0.2.0 deploy patches the live v0.1.16 yaml with deploy/patch-template-v12.py.
func TestPatchTemplateV12(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "v0.1.16", "viral.agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(t.TempDir(), "viral.agent.yaml")
	src := strings.Replace(string(raw), "dry_run: true", "dry_run: false", 1)
	if err := os.WriteFile(live, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join("..", "..", "deploy", "patch-template-v12.py")
	run := func() string {
		out, err := exec.Command(py, script, live).CombinedOutput()
		if err != nil {
			t.Fatalf("patch: %v\n%s", err, out)
		}
		return string(out)
	}
	if out := run(); !strings.Contains(out, "moved study.web_search") || !strings.Contains(out, "max_steps_per_dream from 20 to 8") {
		t.Fatalf("first run printed %q", out)
	}
	tmpl, err := loadOne(live, viralEnv())
	if err != nil {
		t.Fatalf("patched template does not load: %v", err)
	}
	if tmpl.Limits.MaxStepsPerDream != 8 {
		t.Errorf("max_steps_per_dream=%d, want 8", tmpl.Limits.MaxStepsPerDream)
	}
	if tmpl.DryRun || !tmpl.WebSearch.Enabled || tmpl.WebSearch.Model != "gpt-6-luna" {
		t.Errorf("dry_run=%v web_search=%+v", tmpl.DryRun, tmpl.WebSearch)
	}
	patched, _ := os.ReadFile(live)
	for _, gone := range []string{"study", "seed_playbook", "explore_percent", "read_own_metrics", "note_write"} {
		if strings.Contains(string(patched), gone) {
			t.Errorf("patched yaml still mentions %q", gone)
		}
	}
	if out := run(); strings.TrimSpace(out) != "no changes" {
		t.Errorf("second run is not a no-op: %q", out)
	}
	if again, _ := os.ReadFile(live); string(again) != string(patched) {
		t.Error("second run changed the file")
	}
}
