package agent_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/agent"
	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/llm"
	"github.com/canhta/threadwalker/internal/store"
	"github.com/canhta/threadwalker/internal/threads"
)

type wakeBrowser struct {
	err                   error
	reads, writes, closes int
	onRead                func()
}

func (b *wakeBrowser) ReadFeed(context.Context) (threads.Page, error) {
	b.reads++
	if b.onRead != nil {
		b.onRead()
	}
	return threads.Page{Kind: "feed"}, b.err
}
func (b *wakeBrowser) Search(ctx context.Context, _ string) (threads.Page, error) {
	return b.ReadFeed(ctx)
}
func (b *wakeBrowser) ReadPost(ctx context.Context, _ string) (threads.Page, error) {
	return b.ReadFeed(ctx)
}
func (b *wakeBrowser) ReadOwnMetrics(context.Context) (threads.Metrics, error) {
	return threads.Metrics{}, b.err
}
func (b *wakeBrowser) Post(context.Context, string) (string, error) {
	b.writes++
	return "https://fixture.invalid/post", b.err
}
func (b *wakeBrowser) Reply(ctx context.Context, _ string, text string) (string, error) {
	return b.Post(ctx, text)
}
func (b *wakeBrowser) Close(ctx context.Context) error {
	b.closes++
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
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
			backend := &wakeBrowser{}
			switch mode {
			case "cancelled":
				backend.onRead = cancel
				backend.err = context.Canceled
			case "logged_out":
				backend.err = threads.ErrLoggedOut
			case "captcha":
				backend.err = threads.ErrCaptcha
			case "unknown_outcome":
				tmpl.DryRun = false
				backend.err = threads.ErrUnknownOutcome
				fixture.Calls = []llm.ToolCall{agent.SyntheticCall("post", `{"text":"Synthetic first action"}`), agent.SyntheticCall("post", `{"text":"Synthetic must never run"}`)}
			}
			factories := 0
			r, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: func() time.Time { return now }, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends, ThreadsFactory: func(context.Context, config.Template) (threads.Backend, error) {
				factories++
				if mode == "factory_error" {
					return backend, errors.New("private launch detail")
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
			if factories != 1 || backend.closes != 1 {
				t.Fatalf("factory=%d close=%d", factories, backend.closes)
			}
			want := map[string]string{"success": "completed", "cancelled": "cancelled", "logged_out": "logged_out", "captcha": "captcha", "unknown_outcome": "unknown_outcome", "factory_error": "browser_error"}[mode]
			if wake.EndReason != want {
				t.Fatalf("wake=%+v err=%v", wake, err)
			}
			if mode == "unknown_outcome" && (backend.writes != 1 || !errors.Is(err, threads.ErrUnknownOutcome)) {
				t.Fatalf("writes=%d err=%v", backend.writes, err)
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

func TestConfiguredBrowserLaunchFailureClosesWakeSafely(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "launch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tmpl := agent.OfflineTemplate()
	tmpl.Threads = config.ThreadsConfig{Backend: "browser", BaseURL: "https://fixture.invalid", ExecutablePath: "/absent/synthetic-chrome", ProfileDir: filepath.Join(t.TempDir(), "profile")}
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
