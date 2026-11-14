package config_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	run := func(path string) string {
		out, err := exec.Command(py, script, path, viralTemplatePath()).CombinedOutput()
		if err != nil {
			t.Fatalf("patch: %v\n%s", err, out)
		}
		return string(out)
	}
	out := run(live)
	t.Log("\n" + out)
	if strings.Count(out, "rewrote:") != 3 || strings.Count(out, "replaced persona: block") != 1 || strings.Contains(out, "WARNING") {
		t.Fatalf("first run printed %q", out)
	}
	tmpl, err := loadOne(live, viralEnv())
	if err != nil {
		t.Fatalf("patched template does not load: %v", err)
	}
	want, err := loadOne(viralTemplatePath(), viralEnv())
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.DryRun {
		t.Error("dry_run was changed")
	}
	if !reflect.DeepEqual(tmpl.Persona, want.Persona) {
		t.Error("patched persona is not the template's")
	}
	patched, _ := os.ReadFile(live)
	if gone := regexp.MustCompile(`\b(read_feed|read_topic|profile_posts|target_url|target_id|target_username|trend_ref)\b`).FindString(string(patched)); gone != "" {
		t.Errorf("patched yaml still mentions %q", gone)
	}
	if !strings.Contains(string(patched), "dry_run: false") {
		t.Error("dry_run line is gone")
	}
	if bak, err := os.ReadFile(live + ".bak"); err != nil || string(bak) != string(raw) {
		t.Errorf("backup is not the original file (read err %v)", err)
	}
	// Everything outside persona: is byte-for-byte what the live file had.
	if tail := func(s string) string { return s[strings.Index(s, "\nllm:"):] }; tail(string(patched)) != tail(string(raw)) {
		t.Error("the patch changed something after the persona block")
	}
	if out := run(live); strings.TrimSpace(out) != "no changes" {
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
	if out, err := exec.Command(py, script, edited, viralTemplatePath()).CombinedOutput(); err != nil || !strings.Contains(string(out), "WARNING line") {
		t.Fatalf("hand-edited line not reported: %v %s", err, out)
	}

	// An owner-edited persona is not the known one: the script warns and keeps it.
	custom := filepath.Join(t.TempDir(), "custom.agent.yaml")
	body := strings.Replace(string(raw), "Một đứa GenZ Việt hay hóng chuyện", "Một đứa GenZ Việt hay hóng drama", 1)
	if body == string(raw) {
		t.Fatal("fixture identity text changed; update this test")
	}
	if err := os.WriteFile(custom, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out = run(custom)
	if !strings.Contains(out, "WARNING persona: block differs") || strings.Contains(out, "replaced persona") {
		t.Fatalf("edited persona: %q", out)
	}
	after, _ := os.ReadFile(custom)
	if !strings.Contains(string(after), "hay hóng drama") || !strings.Contains(string(after), "style_avoid:") {
		t.Error("the edited persona was changed")
	}
	if strings.TrimSpace(run(custom)) == "no changes" {
		t.Error("an edited persona must keep warning on every run")
	}
}
