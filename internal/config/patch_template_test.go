package config_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The v0.3.0 deploy patches the live v0.2.1 yaml with deploy/patch-template-v13.py.
// testdata/v0.2.1 is the live template as the v0.2.0 deploy left it, with dry_run off.
func TestPatchTemplateV13(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "v0.2.1", "viral.agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(t.TempDir(), "viral.agent.yaml")
	if err := os.WriteFile(live, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join("..", "..", "deploy", "patch-template-v13.py")
	run := func() string {
		out, err := exec.Command(py, script, live).CombinedOutput()
		if err != nil {
			t.Fatalf("patch: %v\n%s", err, out)
		}
		return string(out)
	}
	out := run()
	t.Log("\n" + out)
	if strings.Count(out, "rewrote:") != 3 || strings.Contains(out, "WARNING") {
		t.Fatalf("first run printed %q", out)
	}
	tmpl, err := loadOne(live, viralEnv())
	if err != nil {
		t.Fatalf("patched template does not load: %v", err)
	}
	if tmpl.DryRun {
		t.Error("dry_run was changed")
	}
	patched, _ := os.ReadFile(live)
	if gone := regexp.MustCompile(`\b(read_feed|read_topic|profile_posts|target_url|target_id|target_username|trend_ref)\b`).FindString(string(patched)); gone != "" {
		t.Errorf("patched yaml still mentions %q", gone)
	}
	if !strings.Contains(string(patched), "dry_run: false") {
		t.Error("dry_run line is gone")
	}
	if out := run(); strings.TrimSpace(out) != "no changes" {
		t.Errorf("second run is not a no-op: %q", out)
	}
	if again, _ := os.ReadFile(live); string(again) != string(patched) {
		t.Error("second run changed the file")
	}

	// A hand-edited line the script cannot rewrite is reported, not left silent.
	edited := filepath.Join(t.TempDir(), "edited.agent.yaml")
	if err := os.WriteFile(edited, []byte(strings.Replace(string(raw), "Đọc read_feed, rồi read_topic", "Xem read_feed trước, rồi read_topic", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(py, script, edited).CombinedOutput(); err != nil || !strings.Contains(string(out), "WARNING line") {
		t.Fatalf("hand-edited line not reported: %v %s", err, out)
	}
}
