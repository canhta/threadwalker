//go:build browser_integration

package metrics_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/metrics"
	"github.com/canhta/threadwalker/internal/store"
	"github.com/canhta/threadwalker/internal/threads"
)

func TestMetricProcessorWithActualLocalCDPAndBoundSyntheticDOM(t *testing.T) {
	binary := os.Getenv("CHROME_BIN")
	if binary == "" {
		t.Skip("set CHROME_BIN for explicit local CDP evidence")
	}
	raw, err := os.ReadFile("../../docs/research/testdata/metrics/action-metrics.html")
	if err != nil {
		t.Fatal(err)
	}
	var writes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writes.Add(1)
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "cdp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	tmpl := config.Template{ID: "synthetic-cdp-metrics", DryRun: true, Limits: config.LimitsConfig{MinMinutesBetweenWakes: 15}}
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	for i, path := range []string{"/own", "/other"} {
		if _, err = st.DB().ExecContext(ctx, `INSERT INTO actions(id,agent_id,kind,text,dry_run,posted_at,thread_url) VALUES(?,?,'post','',0,'2026-01-01T00:00:00Z',?)`, i+1, tmpl.ID, server.URL+path); err != nil {
			t.Fatal(err)
		}
	}
	var logs bytes.Buffer
	logger, _ := logging.New(&logs, "debug", "json")
	profile := t.TempDir()
	launches := 0
	browserOptions := threads.Options{AgentID: tmpl.ID, BaseURL: server.URL, ProfileDir: profile, ExecutablePath: binary, Timeout: 5 * time.Second, StartupTimeout: 10 * time.Second, NoSandbox: true, Now: clock, Logger: logger}
	p, err := metrics.New(metrics.Options{Store: st, Template: tmpl, Now: clock, Logger: logger, ThreadsFactory: func(ctx context.Context, _ config.Template) (threads.Backend, error) {
		launches++
		b, e := threads.New(ctx, browserOptions)
		if b == nil {
			return nil, e
		}
		return b, e
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.RunDue(ctx)
	if err != nil || result.Samples != 1 || result.DeferredJobs != 1 || launches != 1 {
		t.Fatalf("result=%+v launches=%d err=%v", result, launches, err)
	}
	samples, err := st.RecentMetricSamples(ctx, tmpl.ID, 10)
	if err != nil || len(samples) != 1 || samples[0].ActionID != 1 || samples[0].Views != 123 {
		t.Fatalf("samples=%+v err=%v", samples, err)
	}
	if writes.Load() != 0 || strings.Contains(logs.String(), server.URL) || !strings.Contains(logs.String(), `"reason":"malformed_page"`) {
		t.Fatal("write, log leak or missing mismatch outcome")
	}
	// A new browser can acquire the profile after processor cleanup.
	b, err := threads.New(ctx, browserOptions)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Close(ctx); err != nil {
		t.Fatal(err)
	}
	t.Log("actual local CDP: exact action binding measured, mismatched action deferred, zero writes, metadata-only logs, profile released")
}
