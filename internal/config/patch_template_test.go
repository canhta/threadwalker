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

// fixturePersonaSHA256 is the hash of the fixture's persona block after the script's
// sentence rewrites. The fixture is a trimmed copy of the v0.2.1 live template with
// placeholder names, so its hash is not the one the script knows for the live persona;
// the test runs a temporary copy of the script with this one. When the fixture's persona
// changes, the script's warning "block differs" means this value needs the new hash.
const fixturePersonaSHA256 = "95016ad2f4689b2af926884cbc39d1bd946f53a8bceb3e69e1a3684c129b391f"

var scriptPath = filepath.Join("..", "..", "deploy", "patch-template-v13.py")

var personaHashLine = regexp.MustCompile(`(?m)^OLD_PERSONA_SHA256 = "[0-9a-f]{64}"$`)

// fixtureScript writes a copy of the script that knows the fixture's persona hash and returns its path.
func fixtureScript(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !personaHashLine.Match(raw) {
		t.Fatal("the script has no OLD_PERSONA_SHA256 line")
	}
	copyPath := filepath.Join(t.TempDir(), "patch-template-v13.py")
	body := personaHashLine.ReplaceAll(raw, []byte(`OLD_PERSONA_SHA256 = "`+fixturePersonaSHA256+`"`))
	if err = os.WriteFile(copyPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return copyPath
}

// hashProgram prints the sha256 of a template's persona block after the script's
// rewrites, then the script's own constant.
const hashProgram = `
import hashlib, importlib.util, sys
spec = importlib.util.spec_from_file_location("patch", sys.argv[1])
patch = importlib.util.module_from_spec(spec)
spec.loader.exec_module(patch)
text = open(sys.argv[2], encoding="utf-8", newline="").read()
for old, new in patch.REWRITES:
    text = text.replace(old, new)
lines = text.splitlines(keepends=True)
start, end = patch.persona_span(lines)
print(hashlib.sha256("".join(lines[start:end]).encode("utf-8")).hexdigest(), patch.OLD_PERSONA_SHA256)
`

// The hash in the script is the persona the v0.2.1 release shipped, with the script's rewrites applied.
func TestPatchScriptKnowsTheV021Persona(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	vcs, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	shipped, err := exec.Command(vcs, "show", "v0.2.1:templates/viral.agent.yaml").Output()
	if err != nil {
		t.Skip("tag v0.2.1 is not available")
	}
	source := filepath.Join(t.TempDir(), "v0.2.1.agent.yaml")
	if err = os.WriteFile(source, shipped, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(py, "-I", "-c", hashProgram, scriptPath, source).Output()
	if err != nil {
		t.Fatalf("hashing the v0.2.1 persona: %v", err)
	}
	if got := strings.Fields(string(out)); len(got) != 2 || got[0] != got[1] {
		t.Fatalf("v0.2.1 persona hash and the script's constant differ: %s", out)
	}
}

// patchCommand runs a script on a live yaml and the repo template.
func patchCommand(py, script string, args ...string) *exec.Cmd {
	return exec.Command(py, append([]string{script}, args...)...)
}

// The v0.3.0 deploy patches the live v0.2.1 yaml with deploy/patch-template-v13.py.
// testdata/v0.2.1 holds the parts of that live template the patch touches, with dry_run off.
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
	script := fixtureScript(t)
	run := func(path string) string {
		out, err := patchCommand(py, script, path, viralTemplatePath()).CombinedOutput()
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
	if out, err := patchCommand(py, script, edited, viralTemplatePath()).CombinedOutput(); err != nil || !strings.Contains(string(out), "WARNING line") {
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
