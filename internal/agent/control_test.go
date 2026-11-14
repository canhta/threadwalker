package agent_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
)

func TestNextWorkNamesTheEarliestKind(t *testing.T) {
	r, st, id := transcriptRuntime(t, nil, nil)
	ctx := context.Background()
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	work, ok, err := r.NextWork(ctx, now)
	if err != nil || !ok || work.Kind != "agent" || work.Due.After(now) {
		t.Fatalf("work=%+v ok=%v err=%v", work, ok, err)
	}
	if err = st.OwnerControl(ctx, id, "pause", "k1", "paused", now); err != nil {
		t.Fatal(err)
	}
	if _, ok, err = r.NextWork(ctx, now); err != nil || ok {
		t.Fatalf("paused agent has work: ok=%v err=%v", ok, err)
	}
}

func TestWakeNowSkipsTheScheduleButNotTheStateOrBudget(t *testing.T) {
	ctx := context.Background()
	r, st, id := transcriptRuntime(t, nil, nil)
	if _, err := r.Wake(ctx); err != nil {
		t.Fatal(err)
	}
	// The scheduled wake moved the next wake to the future; a manual one still runs.
	wake, err := r.WakeNow(ctx, agent.WakeRequest{Phase: "agent"})
	if err != nil || wake.EndReason != "completed" {
		t.Fatalf("wake=%+v err=%v", wake, err)
	}
	if err = st.OwnerControl(ctx, id, "pause", "k1", "paused", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = r.WakeNow(ctx, agent.WakeRequest{Phase: "agent"}); !errors.Is(err, agent.ErrNotRunning) {
		t.Fatalf("paused agent woke: %v", err)
	}
	if _, err = r.WakeNow(ctx, agent.WakeRequest{Phase: "nap"}); err == nil {
		t.Fatal("unknown phase accepted")
	}
}

func TestWakeNowSpendCapStillStopsTheWake(t *testing.T) {
	ctx := context.Background()
	r, st, id := transcriptRuntime(t, nil, nil)
	day := "2026-11-11"
	at := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	prior, err := st.BeginWake(ctx, id, at, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err = st.RecordLLMUsage(ctx, id, prior, day, store.LLMUsage{TokensIn: 1, CostUSD: 5}); err != nil {
		t.Fatal(err)
	}
	if err = st.FinishWake(ctx, prior, at, 1, "completed"); err != nil {
		t.Fatal(err)
	}
	wake, _ := r.WakeNow(ctx, agent.WakeRequest{Phase: "agent"})
	if wake.EndReason != "budget" {
		t.Fatalf("wake=%+v", wake)
	}
}

func TestWakeNowDryRunDoesNotWrite(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "dry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	tmpl := agent.OfflineTemplate()
	tmpl.DryRun = false
	fixture := agent.NewOfflineFixture(func() time.Time { return now })
	fixture.Calls = []llm.ToolCall{agent.SyntheticCall("post", `{"text":"Synthetic live text"}`)}
	backend := failingBrowser(nil)
	r, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: func() time.Time { return now }, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends,
		ThreadsFactory: func(context.Context, config.Template) (threads.Backend, error) { return backend, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = r.WakeNow(ctx, agent.WakeRequest{Phase: "agent", DryRun: true}); err != nil {
		t.Fatal(err)
	}
	var dry int
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM actions WHERE dry_run=1`).Scan(&dry); err != nil || dry != 1 || backend.Count(threads.OpPost) != 0 {
		t.Fatalf("dry actions=%d writes=%d err=%v", dry, backend.Count(threads.OpPost), err)
	}
}

func TestControlRunsOneJobInTheSchedulerLoopAndRefusesAConcurrentOne(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, _, _ := transcriptRuntime(t, nil, nil)
	control := agent.NewControl()
	started, release := make(chan struct{}), make(chan struct{})
	sched := agent.Scheduler{Agents: []*agent.Runtime{r}, Control: control, Now: func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) },
		Wait: func(ctx context.Context, d time.Duration) error { <-ctx.Done(); return ctx.Err() }}
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx, false) }()

	first := make(chan error, 1)
	go func() {
		_, err := control.Do(ctx, func(context.Context) (any, error) { close(started); <-release; return "ok", nil })
		first <- err
	}()
	<-started
	if _, err := control.Do(ctx, func(context.Context) (any, error) { return nil, nil }); !errors.Is(err, agent.ErrBusy) {
		t.Fatalf("second trigger: %v", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	// The slot is free again after the job.
	got, err := control.Do(ctx, func(context.Context) (any, error) { return 7, nil })
	if err != nil || got != 7 {
		t.Fatalf("got=%v err=%v", got, err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("scheduler: %v", err)
	}
}

func TestContextEqualsWhatTheWakeSends(t *testing.T) {
	for _, phase := range []string{"agent", "dream"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(ctx, filepath.Join(t.TempDir(), "ctx.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
			tmpl := agent.OfflineTemplate()
			tmpl.Limits.MaxUSDPerDream, tmpl.Limits.MaxStepsPerDream = 1, 5
			fixture := agent.NewOfflineFixture(func() time.Time { return now })
			fixture.Calls = []llm.ToolCall{}
			var sent []llm.Message
			fixture.Observe = func(m []llm.Message) {
				if sent == nil {
					sent = append([]llm.Message(nil), m...)
				}
			}
			r, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: func() time.Time { return now }, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends})
			if err != nil {
				t.Fatal(err)
			}
			if err = r.Start(ctx); err != nil {
				t.Fatal(err)
			}
			want, err := r.Context(ctx, phase)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.WakeNow(ctx, agent.WakeRequest{Phase: phase}); err != nil {
				t.Fatal(err)
			}
			if len(sent) != len(want) || len(want) < 2 {
				t.Fatalf("sent %d messages, context has %d", len(sent), len(want))
			}
			for i := range want {
				if sent[i].Role != want[i].Role || sent[i].Content != want[i].Content {
					t.Fatalf("message %d differs:\nsent: %s\nctx:  %s", i, sent[i].Content, want[i].Content)
				}
			}
		})
	}
}
