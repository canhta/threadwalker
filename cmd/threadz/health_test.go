package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

// doctorFixture is a healthy deployment: one agent template, an initialised
// database and a Telegram transport that accepts the token.
type doctorFixture struct {
	t        *testing.T
	agents   string
	db       string
	env      map[string]string
	telegram func(*http.Request) (*http.Response, error)
	free     uint64
	freeErr  error
}

func newDoctorFixture(t *testing.T) *doctorFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/fixture.agent.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f := &doctorFixture{t: t, agents: t.TempDir(), db: filepath.Join(t.TempDir(), "tw.db"), free: 10 << 30,
		env: map[string]string{"LLM_API_KEY": "sk-fixture", "OPENAI_API_KEY": "sk-embed-fixture", "TELEGRAM_BOT_TOKEN": "123456:FAKE-SYNTHETIC-TOKEN", "TELEGRAM_OWNER_CHAT_ID": "9001"},
		telegram: func(r *http.Request) (*http.Response, error) {
			return jsonResponse(200, `{"ok":true,"result":{"id":1}}`), nil
		}}
	text := strings.Replace(string(raw), "enabled: false", "enabled: true", 1)
	f.writeTemplate(text)
	st, err := store.Open(context.Background(), f.db)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	return f
}

func (f *doctorFixture) writeTemplate(text string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.agents, "one.yaml"), []byte(text), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *doctorFixture) run(args ...string) (int, []healthResult, string) {
	f.t.Helper()
	var out, errOut bytes.Buffer
	deps := ctlDeps{httpClient: &http.Client{Transport: roundTripFunc(f.telegram)}, freeBytes: func(string) (uint64, error) { return f.free, f.freeErr }, now: func() time.Time { return time.Now() }}
	code := runCtl(context.Background(), append([]string{"doctor", "--json", "--agents", f.agents, "--db", f.db}, args...), func(k string) string { return f.env[k] }, &out, &errOut, deps)
	var results []healthResult
	if out.Len() > 0 {
		if err := json.Unmarshal(out.Bytes(), &results); err != nil {
			f.t.Fatalf("doctor output %q: %v", out.String(), err)
		}
	}
	return code, results, errOut.String()
}

func failed(results []healthResult) []string {
	var names []string
	for _, r := range results {
		if !r.OK {
			names = append(names, r.Name)
		}
	}
	return names
}

func TestDoctorPassesOnAHealthyDeployment(t *testing.T) {
	code, results, stderr := newDoctorFixture(t).run()
	if code != 0 || len(failed(results)) != 0 || len(results) < 6 {
		t.Fatalf("code=%d failed=%v results=%+v stderr=%s", code, failed(results), results, stderr)
	}
}

func TestDoctorFailsOnEachBrokenDependency(t *testing.T) {
	cases := map[string]struct {
		break_ func(*doctorFixture)
		want   string
	}{
		"missing env key":  {func(f *doctorFixture) { delete(f.env, "LLM_API_KEY") }, "env_keys"},
		"invalid template": {func(f *doctorFixture) { f.writeTemplate("id: [") }, "config"},
		"old schema": {func(f *doctorFixture) {
			st, _ := store.Open(context.Background(), f.db)
			defer st.Close()
			st.DB().Exec(`PRAGMA user_version=0`)
		}, "db_schema"},
		"telegram rejects token": {func(f *doctorFixture) {
			f.telegram = func(*http.Request) (*http.Response, error) {
				return jsonResponse(200, `{"ok":false,"error_code":401}`), nil
			}
		}, "telegram:fixture"},
		"telegram unreachable": {func(f *doctorFixture) {
			f.telegram = func(*http.Request) (*http.Response, error) { return nil, errors.New("dial") }
		}, "telegram:fixture"},
		"disk almost full": {func(f *doctorFixture) { f.free = 1 << 20 }, "disk"},
		"retention overdue": {func(f *doctorFixture) {
			st, _ := store.Open(context.Background(), f.db)
			defer st.Close()
			st.AppendTranscript(context.Background(), "fixture", 1, store.TranscriptContext, "x", time.Now().Add(-30*24*time.Hour))
		}, "retention"},
		"browser profile missing": {func(f *doctorFixture) {
			raw, _ := os.ReadFile(filepath.Join(f.agents, "one.yaml"))
			text := strings.Replace(string(raw), "backend: stub", "backend: api\n  base_url: https://www.threads.com\n  executable_path: /absent/chrome\n  agent_browser_path: /absent/agent-browser", 1)
			text = strings.Replace(text, "dry_run: false", "dry_run: true", 1)
			f.writeTemplate(text)
		}, "browser:fixture"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newDoctorFixture(t)
			c.break_(f)
			code, results, stderr := f.run()
			got := failed(results)
			if code == 0 || len(got) == 0 {
				t.Fatalf("doctor passed: code=%d results=%+v stderr=%s", code, results, stderr)
			}
			found := false
			for _, n := range got {
				found = found || n == c.want
			}
			if !found {
				t.Fatalf("want %s among failures %v", c.want, got)
			}
		})
	}
}

func TestDoctorNeverPrintsASecretValue(t *testing.T) {
	f := newDoctorFixture(t)
	delete(f.env, "TELEGRAM_OWNER_CHAT_ID")
	var out bytes.Buffer
	deps := ctlDeps{httpClient: &http.Client{Transport: roundTripFunc(f.telegram)}, freeBytes: func(string) (uint64, error) { return f.free, nil }, now: time.Now}
	runCtl(context.Background(), []string{"doctor", "--agents", f.agents, "--db", f.db}, func(k string) string { return f.env[k] }, &out, &out, deps)
	for _, secret := range []string{"sk-fixture", "123456:FAKE-SYNTHETIC-TOKEN"} {
		if strings.Contains(out.String(), secret) {
			t.Fatalf("secret in output: %s", out.String())
		}
	}
	if !strings.Contains(out.String(), "TELEGRAM_OWNER_CHAT_ID") || !strings.Contains(out.String(), "FAIL") {
		t.Fatalf("missing key name or failure marker: %s", out.String())
	}
}

func browserTemplate(f *doctorFixture, exe, profile string) {
	raw, _ := os.ReadFile("testdata/fixture.agent.yaml")
	text := strings.Replace(string(raw), "enabled: false", "enabled: true", 1)
	text = strings.Replace(text, "backend: stub", "backend: api\n  base_url: https://www.threads.com\n  agent_browser_path: /absent/agent-browser\n  executable_path: "+exe, 1)
	text = strings.Replace(text, "profile_dir: data/profiles/fixture", "profile_dir: "+profile, 1)
	f.writeTemplate(text)
}

func detail(results []healthResult, name string) (healthResult, bool) {
	for _, r := range results {
		if r.Name == name {
			return r, true
		}
	}
	return healthResult{}, false
}

func TestDoctorNamesTheBrokenPart(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "chrome")
	os.WriteFile(exe, nil, 0o700)
	cases := map[string]struct {
		setup  func(*doctorFixture) []string
		name   string
		ok     bool
		detail string
	}{
		"browser profile directory missing": {func(f *doctorFixture) []string { browserTemplate(f, exe, filepath.Join(dir, "absent")); return nil }, "browser:fixture", false, "profile directory"},
		"browser present": {func(f *doctorFixture) []string {
			profile := filepath.Join(dir, "profile")
			os.MkdirAll(profile, 0o700)
			browserTemplate(f, exe, profile)
			return nil
		}, "browser:fixture", true, "profile present"},
		"database missing": {func(f *doctorFixture) []string { f.db = filepath.Join(dir, "none.db"); return nil }, "db_schema", false, "database is unavailable"},
		"disk unreadable":  {func(f *doctorFixture) []string { f.freeErr = errors.New("statfs"); return nil }, "disk", false, "reading free disk space"},
		"bot token unusable": {func(f *doctorFixture) []string {
			f.env["TELEGRAM_BOT_TOKEN"] = ""
			return nil
		}, "telegram:fixture", false, "bot token is not valid"},
		"telegram disabled": {func(f *doctorFixture) []string {
			raw, _ := os.ReadFile("testdata/fixture.agent.yaml")
			f.writeTemplate(string(raw))
			return nil
		}, "telegram:fixture", true, "disabled"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newDoctorFixture(t)
			extra := c.setup(f)
			_, results, stderr := f.run(extra...)
			got, found := detail(results, c.name)
			if !found || got.OK != c.ok || !strings.Contains(got.Detail, c.detail) {
				t.Fatalf("%s = %+v (found %v) stderr=%s all=%+v", c.name, got, found, stderr, results)
			}
		})
	}
}
