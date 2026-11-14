package agent_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
)

// failingBrowser fails every read and write with err; close stays clean.
func failingBrowser(err error) *threads.Memory {
	m := threads.NewMemory()
	for _, op := range []string{threads.OpReadFeed, threads.OpSearch, threads.OpReadPost, threads.OpReadOwnMetrics, threads.OpPost, threads.OpReply} {
		m.Errors[op] = err
	}
	m.Results[threads.OpPost], m.Results[threads.OpReply] = "https://fixture.invalid/post", "https://fixture.invalid/post"
	return m
}

func TestWakeBrowserFactoryClosesEveryExitAndPreservesPartialAccounting(t *testing.T) {
	for _, mode := range []string{"success", "cancelled", "logged_out", "captcha", "unknown_outcome", "factory_error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			st, err := store.Open(ctx, filepath.Join(t.TempDir(), "browser.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
			tmpl := agent.OfflineTemplate()
			fixture := agent.NewOfflineFixture(func() time.Time { return now })
			fixture.Calls = []llm.ToolCall{agent.SyntheticCall("read_feed", `{}`)}
			backend := threads.NewMemory()
			switch mode {
			case "cancelled":
				backend = failingBrowser(context.Canceled)
				backend.Hook = func(_ context.Context, c threads.Call) error {
					if c.Op == threads.OpReadFeed {
						cancel()
					}
					return nil
				}
			case "logged_out":
				backend = failingBrowser(threads.ErrLoggedOut)
			case "captcha":
				backend = failingBrowser(threads.ErrCaptcha)
			case "unknown_outcome":
				tmpl.DryRun = false
				backend = failingBrowser(threads.ErrUnknownOutcome)
				delete(backend.Errors, threads.OpReadOwnMetrics) // an uncertain publish, not an uncertain read
				fixture.Calls = []llm.ToolCall{agent.SyntheticCall("post", `{"text":"Synthetic first action"}`), agent.SyntheticCall("post", `{"text":"Synthetic must never run"}`)}
			}
			factories := 0
			r, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: func() time.Time { return now }, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends, ThreadsFactory: func(context.Context, config.Template) (threads.Backend, error) {
				factories++
				if mode == "factory_error" {
					return nil, errors.New("private launch detail")
				}
				return backend, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			if err = r.Start(ctx); err != nil {
				t.Fatal(err)
			}
			wake, err := r.Wake(ctx)
			wantCloses := 1
			if mode == "factory_error" {
				wantCloses = 0
			}
			if factories != 1 || backend.Count(threads.OpClose) != wantCloses {
				t.Fatalf("factory=%d close=%d", factories, backend.Count(threads.OpClose))
			}
			want := map[string]string{"success": "completed", "cancelled": "cancelled", "logged_out": "logged_out", "captcha": "captcha", "unknown_outcome": "unknown_outcome", "factory_error": "browser_error"}[mode]
			if wake.EndReason != want {
				t.Fatalf("wake=%+v err=%v", wake, err)
			}
			if mode == "unknown_outcome" && (backend.Count(threads.OpPost) != 1 || !errors.Is(err, threads.ErrUnknownOutcome)) {
				t.Fatalf("writes=%d err=%v", backend.Count(threads.OpPost), err)
			}
			if mode == "logged_out" || mode == "captcha" {
				state, e := st.AgentState(context.Background(), tmpl.ID)
				if e != nil || state.Status != "paused" {
					t.Fatalf("state=%+v err=%v", state, e)
				}
				pending, e := st.PendingOwner(context.Background(), []string{tmpl.ID})
				if e != nil || len(pending) != 1 {
					t.Fatalf("pending=%+v err=%v", pending, e)
				}
			}
			if mode != "factory_error" {
				usage, e := st.LLMUsageForDay(context.Background(), tmpl.ID, "2026-11-11")
				if e != nil || usage.TokensIn < 10 || usage.TokensOut < 5 {
					t.Fatalf("partial usage=%+v err=%v", usage, e)
				}
			}
			var ended string
			if e := st.DB().QueryRow(`SELECT end_reason FROM wakes WHERE id=?`, wake.ID).Scan(&ended); e != nil || ended != want {
				t.Fatalf("persisted reason=%s err=%v", ended, e)
			}
		})
	}
}

// A wake that finished its own work stays completed when the final browser
// cleanup fails. The failure is logged without rewriting the outcome, and the
// wake returns no error that would stop the scheduler or cancel owner polling.
func TestCompletedWakeWithFailedBrowserCloseStaysCompleted(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "close.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 11, 12, 11, 55, 0, 0, time.UTC)
	tmpl := agent.OfflineTemplate()
	fixture := agent.NewOfflineFixture(func() time.Time { return now })
	fixture.Calls = []llm.ToolCall{agent.SyntheticCall("read_feed", `{}`)}
	backend := threads.NewMemory()
	backend.Errors[threads.OpClose] = errors.New("synthetic close failure")
	var logs bytes.Buffer
	logger, err := logging.New(&logs, "debug", "json")
	if err != nil {
		t.Fatal(err)
	}
	r, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: func() time.Time { return now }, Logger: logger, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends, ThreadsFactory: func(context.Context, config.Template) (threads.Backend, error) {
		return backend, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	wake, err := r.Wake(ctx)
	if err != nil || wake.EndReason != "completed" {
		t.Fatalf("wake=%+v err=%v", wake, err)
	}
	if backend.Count(threads.OpClose) != 1 {
		t.Fatalf("close calls=%d", backend.Count(threads.OpClose))
	}
	var persisted string
	if err = st.DB().QueryRow(`SELECT end_reason FROM wakes WHERE id=?`, wake.ID).Scan(&persisted); err != nil || persisted != "completed" {
		t.Fatalf("persisted reason=%q err=%v", persisted, err)
	}
	logged := logs.String()
	if !strings.Contains(logged, `"event":"wake.cleanup.failed"`) || !strings.Contains(logged, `"reason":"close_failed"`) {
		t.Fatalf("cleanup failure not logged: %s", logged)
	}
	if strings.Contains(logged, `"event":"wake.stopped"`) {
		t.Fatalf("completed wake downgraded: %s", logged)
	}
}

func TestConfiguredBrowserLaunchFailureClosesWakeSafely(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "launch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tmpl := agent.OfflineTemplate()
	tmpl.Threads = config.ThreadsConfig{Backend: "api", BaseURL: "https://www.threads.com", ExecutablePath: "/absent/synthetic-chrome", AgentBrowserPath: "/absent/agent-browser", ProfileDir: filepath.Join(t.TempDir(), "profile")}
	r, err := agent.New(agent.Options{Store: st, Template: tmpl})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	wake, err := r.Wake(ctx)
	if err == nil || wake.EndReason != "browser_error" {
		t.Fatalf("wake=%+v err=%v", wake, err)
	}
	var reason string
	if err = st.DB().QueryRow(`SELECT end_reason FROM wakes WHERE id=?`, wake.ID).Scan(&reason); err != nil || reason != "browser_error" {
		t.Fatalf("reason=%s err=%v", reason, err)
	}
}
