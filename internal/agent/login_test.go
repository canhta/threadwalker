package agent_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
)

type loginRig struct {
	st    *store.Store
	r     *agent.Runtime
	tmpl  config.Template
	now   time.Time
	opts  agent.Options
	close *threads.Memory
}

func newLoginRig(t *testing.T, backend *threads.Memory, mutate func(*agent.Options)) *loginRig {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "login.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	lr := &loginRig{st: st, now: time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC), tmpl: agent.OfflineTemplate(), close: backend}
	lr.tmpl.Threads.Backend = "api"
	lr.opts = agent.Options{Store: st, Template: lr.tmpl, Now: func() time.Time { return lr.now }, ThreadsFactory: func(context.Context, config.Template) (threads.Backend, error) {
		if backend == nil {
			return nil, fmt.Errorf("opening threads api: %w", threads.ErrNoToken)
		}
		return backend, nil
	}}
	if mutate != nil {
		mutate(&lr.opts)
	}
	lr.r, err = agent.New(lr.opts)
	if err != nil {
		t.Fatal(err)
	}
	if err = lr.r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return lr
}

func TestMissingTokenSkipsTheWakeAndKeepsTheSchedulerRunning(t *testing.T) {
	ctx := context.Background()
	var told []string
	lr := newLoginRig(t, nil, func(o *agent.Options) {
		o.TokenMissing = func(_ context.Context, id string) { told = append(told, id) }
	})
	sched := agent.Scheduler{Agents: []*agent.Runtime{lr.r}, Now: func() time.Time { return lr.now }}
	if err := sched.Run(ctx, true); err != nil {
		t.Fatalf("scheduler stopped: %v", err)
	}
	if len(told) != 1 || told[0] != lr.tmpl.ID {
		t.Fatalf("owner told %v", told)
	}
	var reason string
	if err := lr.st.DB().QueryRow(`SELECT end_reason FROM wakes ORDER BY id DESC LIMIT 1`).Scan(&reason); err != nil || reason != "no_token" {
		t.Fatalf("end reason %q %v", reason, err)
	}
	state, err := lr.st.AgentState(ctx, lr.tmpl.ID)
	if err != nil || state.Status != "running" || !state.NextWake.After(lr.now) {
		t.Fatalf("state %+v %v", state, err)
	}
}

func TestNoWakeRunsWhileACookieImportHoldsTheProfile(t *testing.T) {
	ctx := context.Background()
	held := true
	lr := newLoginRig(t, threads.NewMemory(), func(o *agent.Options) {
		o.ProfileHeld = func(string) bool { return held }
	})
	if _, err := lr.r.Wake(ctx); !errors.Is(err, agent.ErrProfileHeld) {
		t.Fatalf("wake: %v", err)
	}
	if _, err := lr.r.WakeNow(ctx, agent.WakeRequest{Phase: "agent"}); !errors.Is(err, agent.ErrProfileHeld) {
		t.Fatalf("manual wake: %v", err)
	}
	if _, err := lr.r.RunPhase(ctx, "dream", "k"); !errors.Is(err, agent.ErrProfileHeld) {
		t.Fatalf("phase: %v", err)
	}
	if _, ok, err := lr.r.NextWork(ctx, lr.now); ok || err != nil {
		t.Fatalf("work offered during a session: %v %v", ok, err)
	}
	var wakes int
	if err := lr.st.DB().QueryRow(`SELECT COUNT(*) FROM wakes`).Scan(&wakes); err != nil || wakes != 0 {
		t.Fatalf("wake rows %d %v", wakes, err)
	}
	held = false
	if _, ok, err := lr.r.NextWork(ctx, lr.now); !ok || err != nil {
		t.Fatalf("work after the session: %v %v", ok, err)
	}
}

func TestSessionCheckPausesALoggedOutProfileAndSchedulesTheNext(t *testing.T) {
	ctx := context.Background()
	out := failingBrowser(threads.ErrLoggedOut)
	lr := newLoginRig(t, out, nil)
	lr.now = lr.now.Add(25 * time.Hour) // the daily check is due
	if err := lr.st.SetNextWake(ctx, lr.tmpl.ID, lr.now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	work, ok, err := lr.r.NextWork(ctx, lr.now)
	if err != nil || !ok || work.Kind != "session" {
		t.Fatalf("work %+v %v %v", work, ok, err)
	}
	if err = lr.r.CheckSession(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := lr.st.AgentState(ctx, lr.tmpl.ID)
	pending, perr := lr.st.PendingOwner(ctx, []string{lr.tmpl.ID})
	if err != nil || perr != nil || state.Status != "paused" || len(pending) != 1 || out.Count(threads.OpClose) != 1 {
		t.Fatalf("state %+v pending %d closes %d", state, len(pending), out.Count(threads.OpClose))
	}
}

func TestSessionCheckLeavesALoggedInProfileRunning(t *testing.T) {
	ctx := context.Background()
	ok := threads.NewMemory()
	lr := newLoginRig(t, ok, nil)
	if err := lr.r.CheckSession(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := lr.st.AgentState(ctx, lr.tmpl.ID)
	if err != nil || state.Status != "running" || ok.Count(threads.OpReadFeed) != 1 || ok.Count(threads.OpClose) != 1 {
		t.Fatalf("state %+v feed reads %d", state, ok.Count(threads.OpReadFeed))
	}
	if work, found, _ := lr.r.NextWork(ctx, lr.now); !found || work.Kind == "session" {
		t.Fatalf("check not rescheduled: %+v", work)
	}
	lr.now = lr.now.Add(25 * time.Hour)
	if err := lr.st.SetNextWake(ctx, lr.tmpl.ID, lr.now.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if work, found, _ := lr.r.NextWork(ctx, lr.now); !found || work.Kind != "session" {
		t.Fatalf("daily check missing: %+v", work)
	}
}

func TestDailyCheckTellsTheOwnerOfALogoutOncePerDay(t *testing.T) {
	ctx := context.Background()
	lr := newLoginRig(t, failingBrowser(threads.ErrLoggedOut), nil)
	for i := 0; i < 3; i++ {
		lr.now = lr.now.Add(time.Hour)
		if err := lr.r.CheckSession(ctx); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := lr.st.PendingOwner(ctx, []string{lr.tmpl.ID})
	if err != nil || len(pending) != 1 {
		t.Fatalf("three checks on one day queued %d notices: %v", len(pending), err)
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("provider down")
}

func TestProviderFailureBacksTheAgentOffInsteadOfStoppingTheScheduler(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lr := newLoginRig(t, threads.NewMemory(), func(o *agent.Options) {
		o.HTTPClient = &http.Client{Transport: failingTransport{}}
		o.Wait = func(context.Context, time.Duration) error { return nil }
	})
	idles := 0
	sched := agent.Scheduler{Agents: []*agent.Runtime{lr.r}, Now: func() time.Time { return lr.now }, Wait: func(context.Context, time.Duration) error {
		idles++
		if idles > 2 {
			cancel()
		}
		return ctx.Err()
	}}
	if err := sched.Run(ctx, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("scheduler stopped on a provider failure: %v", err)
	}
	var wakes int
	if err := lr.st.DB().QueryRow(`SELECT COUNT(*) FROM wakes WHERE kind='agent'`).Scan(&wakes); err != nil || wakes != 1 {
		t.Fatalf("a backing-off agent woke %d times: %v", wakes, err)
	}
	pending, err := lr.st.PendingOwner(context.Background(), []string{lr.tmpl.ID})
	if err != nil || len(pending) != 1 {
		t.Fatalf("owner notices %d: %v", len(pending), err)
	}
	if work, ok, _ := lr.r.NextWork(context.Background(), lr.now.Add(16*time.Minute)); !ok || work.Kind == "" {
		t.Fatalf("no retry after the backoff: %+v %v", work, ok)
	}
}
