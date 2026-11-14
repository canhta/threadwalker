package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
)

func TestRunOnceOfflineUsesNoCredentialsOrExternalServices(t *testing.T) {
	ctx := context.Background()
	var out, stderr bytes.Buffer
	db := filepath.Join(t.TempDir(), "offline.db")
	code := run(ctx, []string{"run", "-once", "-offline", "-db", db}, noSecretEnv(t, "offline read environment"), &out, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s logs=%s", code, &stderr, &out)
	}
	st, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var wakes, actions int
	st.DB().QueryRow(`SELECT COUNT(*) FROM wakes WHERE end_reason='completed'`).Scan(&wakes)
	st.DB().QueryRow(`SELECT COUNT(*) FROM actions WHERE dry_run=1`).Scan(&actions)
	if wakes != 1 || actions != 1 {
		t.Fatalf("wakes=%d actions=%d", wakes, actions)
	}
}

func TestOfflineExplicitEnabledTemplateStillDisablesTelegram(t *testing.T) {
	raw, err := os.ReadFile("testdata/fixture.agent.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	text := strings.Replace(string(raw), "enabled: false", "enabled: true", 1)
	if err = os.WriteFile(filepath.Join(dir, "one.yaml"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"run", "-once", "-offline", "-agents", dir, "-db", filepath.Join(t.TempDir(), "offline.db")}, noSecretEnv(t, "offline read environment credentials"), &out, &stderr)
	if code != 0 {
		t.Fatalf("offline enabled template failed %d: %s", code, &stderr)
	}
	if strings.Contains(out.String(), "telegram.operation.finished") {
		t.Fatal("offline started Telegram service")
	}
}

type dialRecorder struct {
	t     *testing.T
	mu    sync.Mutex
	hosts []string
}

func (d *dialRecorder) RoundTrip(request *http.Request) (*http.Response, error) {
	d.mu.Lock()
	d.hosts = append(d.hosts, request.URL.Host+request.URL.Path)
	d.mu.Unlock()
	d.t.Errorf("offline run made a network request to %s", request.URL.Host+request.URL.Path)
	return nil, errors.New("offline run: network disabled")
}

// TestOfflineWakeInjectsEveryOutsideFetch drives a real wake with the viral
// template, which enables web search. Any request through the default transport
// fails the test.
func TestOfflineWakeInjectsEveryOutsideFetch(t *testing.T) {
	raw, err := os.ReadFile("../../templates/viral.agent.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, "viral.yaml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	recorder := &dialRecorder{t: t}
	previous := http.DefaultTransport
	http.DefaultTransport = recorder
	t.Cleanup(func() { http.DefaultTransport = previous })
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "offline.db")
	var out, stderr bytes.Buffer
	code := run(ctx, []string{"run", "-once", "-offline", "-agents", dir, "-db", db}, noSecretEnv(t, "offline read environment credentials"), &out, &stderr)
	if code != 0 {
		t.Fatalf("offline run failed %d: %s", code, &stderr)
	}
	recorder.mu.Lock()
	hosts := append([]string{}, recorder.hosts...)
	recorder.mu.Unlock()
	if len(hosts) != 0 {
		t.Fatalf("offline run made requests: %v", hosts)
	}
	st, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var kind string
	var searches, stories int
	if err = st.DB().QueryRow(`SELECT kind FROM wakes ORDER BY id DESC LIMIT 1`).Scan(&kind); err != nil || kind != store.WakeAgent {
		t.Fatalf("wake kind=%q err=%v", kind, err)
	}
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE tool='web_search' AND result_json LIKE '%"outcome":"success"%'`).Scan(&searches); err != nil || searches != 1 {
		t.Fatalf("successful web searches=%d err=%v", searches, err)
	}
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM trend_stories`).Scan(&stories); err != nil || stories == 0 {
		t.Fatalf("trend stories=%d err=%v", stories, err)
	}
}

func TestValidateTemplatesNonDryRunNeedsPublishingBackend(t *testing.T) {
	live := config.Template{ID: "a", Threads: config.ThreadsConfig{Backend: "api"}}
	stub := config.Template{ID: "b", Threads: config.ThreadsConfig{Backend: "stub"}}
	dry := config.Template{ID: "c", DryRun: true, Threads: config.ThreadsConfig{Backend: "stub"}}
	if err := validateTemplates([]config.Template{live, dry}, false); err != nil {
		t.Fatalf("api non-dry-run: %v", err)
	}
	if err := validateTemplates([]config.Template{stub}, false); err == nil {
		t.Fatal("non-dry-run stub accepted")
	}
	if err := validateTemplates([]config.Template{live}, true); err == nil {
		t.Fatal("offline must replace the backend with the stub and refuse non-dry-run")
	}
	if err := validateTemplates([]config.Template{dry, dry}, false); err == nil {
		t.Fatal("duplicate agent IDs accepted")
	}
}

func TestServeAPIRefusesNonLoopbackAndServesUntilCancelled(t *testing.T) {
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, addr := range []string{"0.0.0.0:0", "localhost:0", "nonsense"} {
		if err := serveAPI(ctx, &wg, http.NotFoundHandler(), addr); err == nil {
			t.Fatalf("%s accepted", addr)
		}
	}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()
	if err = serveAPI(ctx, &wg, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }), addr); err != nil {
		t.Fatal(err)
	}
	if err = serveAPI(ctx, &wg, http.NotFoundHandler(), addr); err == nil {
		t.Fatal("second listener on the same address accepted")
	}
	resp, err := http.Get("http://" + addr)
	if err != nil || resp.StatusCode != http.StatusTeapot {
		t.Fatalf("serve: %v", err)
	}
	resp.Body.Close()
	cancel()
	wg.Wait()
}

func TestNewAPIServerNeedsTheAPIBackendAndItsSecrets(t *testing.T) {
	if _, err := newAPIServer(nil, nil, nil, config.NewRedactor(nil), "https://x.example", func(string) string { return "" }, time.Now, nil); err == nil {
		t.Fatal("no tokens accepted")
	}
	tk, err := threads.NewTokens(threads.TokensOptions{Slot: &store.Store{}, Key: bytes.Repeat([]byte{1}, 32), Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	tmpl := config.Template{ID: "a"}
	tmpl.Threads.Backend, tmpl.Threads.UserID = "api", "9"
	env := map[string]string{"THREADS_APP_ID": "app", "THREADS_APP_SECRET": "sec", "THREADS_WEBHOOK_VERIFY_TOKEN": "ver"}
	red := config.NewRedactor(nil)
	if _, err = newAPIServer(nil, tk, []config.Template{tmpl}, red, "https://x.example", func(k string) string { return env[k] }, time.Now, nil); err != nil {
		t.Fatal(err)
	}
	if red.String("sec ver") == "sec ver" {
		t.Fatal("secrets not redacted")
	}
	if _, err = newAPIServer(nil, tk, []config.Template{tmpl}, red, "https://x.example", func(string) string { return "" }, time.Now, nil); err == nil {
		t.Fatal("missing env accepted")
	}
}
