//go:build browser_integration

package threads

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// This driver-only CDP probe is read-only. Write evidence belongs to the tagged
// integration harness, which reserves its guarded DB intent before submission.
func TestDriverLocalChromeReadAndLifecycle(t *testing.T) {
	binary := os.Getenv("CHROME_BIN")
	if binary == "" {
		t.Skip("set CHROME_BIN for local CDP evidence")
	}
	var mode atomic.Int32
	var foreignRequests atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreignRequests.Add(1); w.WriteHeader(204) }))
	defer foreign.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/favicon.ico" {
			w.WriteHeader(204)
			return
		}
		if mode.Load() == 3 {
			http.Redirect(w, r, foreign.URL+"/capture", http.StatusFound)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "feed" {
			switch mode.Load() {
			case 1:
				name = "logged-out"
			case 2:
				name = "captcha"
			case 4:
				name = "unknown"
			}
		}
		raw, err := os.ReadFile(filepath.Join("../../docs/research/testdata/browser", name+".html"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if mode.Load() == 5 {
			raw = append(raw, []byte(`<main data-tw-page="feed" data-tw-contract="synthetic-v1"></main>`)...)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	profile := t.TempDir()
	marker := filepath.Join(profile, "operator-data")
	if err := os.WriteFile(marker, []byte("retain"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	opts := Options{AgentID: "driver-test", BaseURL: server.URL, ProfileDir: profile, ExecutablePath: binary, Timeout: time.Second, StartupTimeout: 5 * time.Second, NoSandbox: true}
	b, err := New(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close(context.Background())
	for _, read := range []func() (Page, error){func() (Page, error) { return b.ReadFeed(ctx) }, func() (Page, error) { return b.Search(ctx, "synthetic") }, func() (Page, error) { return b.ReadPost(ctx, server.URL+"/post") }} {
		p, err := read()
		if err != nil || len(p.Posts) != 1 {
			t.Fatalf("read posts=%d err=%v", len(p.Posts), err)
		}
	}
	m, err := b.ReadOwnMetrics(ctx)
	if err != nil || m.Views != 123 || m.Likes != 7 || m.Replies != 3 || m.Reposts != 1 {
		t.Fatalf("metrics=%+v err=%v", m, err)
	}
	for _, tc := range []struct {
		mode int32
		want error
	}{{1, ErrLoggedOut}, {2, ErrCaptcha}, {4, ErrUnknownPage}} {
		mode.Store(tc.mode)
		if _, err = b.ReadFeed(ctx); !errors.Is(err, tc.want) {
			t.Fatalf("mode %d: %v", tc.mode, err)
		}
	}
	mode.Store(5)
	if _, err = b.ReadFeed(ctx); err == nil {
		t.Fatal("ambiguous roots accepted")
	}
	mode.Store(3)
	if _, err = b.ReadFeed(ctx); err == nil {
		t.Fatal("foreign redirect accepted")
	}
	if foreignRequests.Load() != 0 {
		t.Fatal("foreign redirect reached destination")
	}
	if _, err = New(ctx, opts); err == nil {
		t.Fatal("profile shared by active browser")
	}
	if err = b.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(marker); err != nil {
		t.Fatal("operator data removed")
	}
	mode.Store(0)
	b, err = New(ctx, opts)
	if err != nil {
		t.Fatal("reopen profile:", err)
	}
	if err = b.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	opts.AgentID = "other-agent"
	if _, err = New(ctx, opts); err == nil {
		t.Fatal("profile reused by other agent")
	}
}
