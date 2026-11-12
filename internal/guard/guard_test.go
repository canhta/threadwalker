package guard_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/guard"
	"github.com/canhta/threadwalker/internal/store"
)

func setup(t *testing.T) (*store.Store, guard.Options) {
	st, _, opts := setupPath(t)
	return st, opts
}

// setupPath also returns the database path so concurrency tests can give each
// goroutine an independent connection instead of contending on one pool.
func setupPath(t *testing.T) (*store.Store, string, guard.Options) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "guard.db")
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	_, err = st.DB().Exec(`INSERT INTO agents(id,status) VALUES ('agent','running'); INSERT INTO wakes(id,agent_id,started_at) VALUES (1,'agent','2026-11-11T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	return st, path, guard.Options{Store: st, Template: config.Template{ID: "agent", DryRun: true}, WakeID: 1, Now: func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }}
}

// concurrentStore opens an independent handle on the same temporary database.
// One connection plus a bounded busy timeout makes write contention wait rather
// than fail immediately with SQLITE_BUSY; the test still runs real concurrent
// guards over separate connections.
func concurrentStore(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	st.DB().SetMaxOpenConns(1)
	if _, err = st.DB().Exec(`PRAGMA busy_timeout=5000`); err != nil {
		t.Fatal(err)
	}
	return st
}
func TestGuardAuditsBeforeEffectAndKeepsPayloadsOut(t *testing.T) {
	st, opts := setup(t)
	g, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	output, err := g.Execute(context.Background(), guard.Attempt{Tool: "read_feed", Arguments: `{"secret":"private-note"}`}, func() (string, error) {
		var count int
		if err := st.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE result_json = ''`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("pre-effect audit: %d %v", count, err)
		}
		return "private-external-text", nil
	})
	if err != nil || output != "private-external-text" {
		t.Fatalf("result %q %v", output, err)
	}
	var args, result, at string
	st.DB().QueryRow(`SELECT args_json,result_json,at FROM tool_calls`).Scan(&args, &result, &at)
	if args == "" || result == "" || at != "2026-11-11T00:00:00Z" {
		t.Fatalf("audit metadata %q %q %q", args, result, at)
	}
	if args == `{"secret":"private-note"}` || result == "private-external-text" {
		t.Fatal("raw payload in audit")
	}
}
func TestGuardRejectsBeforeEffects(t *testing.T) {
	for _, tc := range []struct{ name, sql, reason string }{
		{"paused", `UPDATE agents SET status='paused'`, "agent_not_running"},
		{"killed", `UPDATE agents SET status='killed'`, "agent_not_running"},
		{"missing-agent", `DELETE FROM agents`, "agent_not_running"},
		{"closed-wake", `UPDATE wakes SET ended_at='2026-11-11T00:00:00Z'`, "wake_not_open"},
		{"foreign-wake", `UPDATE wakes SET agent_id='other'`, "wake_not_open"},
		{"missing-wake", `DELETE FROM wakes`, "wake_not_open"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, opts := setup(t)
			g, _ := guard.New(opts)
			st.DB().Exec(tc.sql)
			called := false
			_, err := g.Execute(context.Background(), guard.Attempt{Tool: "note_write", Arguments: "{}"}, func() (string, error) { called = true; return "", nil })
			if err == nil || called {
				t.Fatalf("called %v err %v", called, err)
			}
			var reason string
			st.DB().QueryRow(`SELECT blocked_by FROM tool_calls`).Scan(&reason)
			if reason != tc.reason {
				t.Fatalf("reason %q", reason)
			}
		})
	}
}
func TestGuardAuditFailureAndRejectedAttempts(t *testing.T) {
	for _, phase := range []string{"insert", "update", "state"} {
		t.Run(phase, func(t *testing.T) {
			st, opts := setup(t)
			g, _ := guard.New(opts)
			switch phase {
			case "insert":
				st.DB().Exec(`CREATE TRIGGER fail_audit BEFORE INSERT ON tool_calls BEGIN SELECT RAISE(FAIL,'private-storage-error'); END`)
			case "update":
				st.DB().Exec(`CREATE TRIGGER fail_audit BEFORE UPDATE ON tool_calls BEGIN SELECT RAISE(FAIL,'private-storage-error'); END`)
			case "state":
				st.DB().Exec(`DROP TABLE agents`)
			}
			called := false
			_, err := g.Execute(context.Background(), guard.Attempt{Tool: "read_feed", Arguments: "{}"}, func() (string, error) { called = true; return "ok", nil })
			if !errors.Is(err, guard.ErrAudit) || called != (phase == "update") {
				t.Fatalf("called %v err %v", called, err)
			}
			if err.Error() == "private-storage-error" {
				t.Fatal("unsafe error")
			}
		})
	}
	st, opts := setup(t)
	g, _ := guard.New(opts)
	called := false
	_, err := g.Execute(context.Background(), guard.Attempt{Tool: "unknown", Arguments: "{", BlockedBy: "unknown_tool"}, func() (string, error) { called = true; return "", nil })
	if err == nil || called {
		t.Fatal("invalid call executed")
	}
	var reason string
	st.DB().QueryRow(`SELECT blocked_by FROM tool_calls`).Scan(&reason)
	if reason != "unknown_tool" {
		t.Fatal(reason)
	}
}
func TestGuardEffectFailureAndOptions(t *testing.T) {
	st, opts := setup(t)
	g, _ := guard.New(opts)
	sentinel := errors.New("effect failure")
	_, err := g.Execute(context.Background(), guard.Attempt{Tool: "note_write", Arguments: "{}"}, func() (string, error) { return "", sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	var result string
	st.DB().QueryRow(`SELECT result_json FROM tool_calls`).Scan(&result)
	if result != `{"outcome":"error","bytes":0}` {
		t.Fatal(result)
	}
	for _, change := range []func(*guard.Options){func(o *guard.Options) { o.Store = nil }, func(o *guard.Options) { o.Template.ID = "" }, func(o *guard.Options) { o.Template.DryRun = false }, func(o *guard.Options) { o.WakeID = 0 }} {
		bad := opts
		change(&bad)
		if _, err := guard.New(bad); err == nil {
			t.Fatal("invalid options accepted")
		}
	}
	opts.Now = nil
	g, err = guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !g.Matches(st, "agent", 1) || g.Matches(st, "other", 1) {
		t.Fatal("scope match")
	}
	_, err = g.Execute(context.Background(), guard.Attempt{}, nil)
	if err == nil {
		t.Fatal("nil effect")
	}
}

func TestUnknownGuardToolCannotExecute(t *testing.T) {
	st, opts := setup(t)
	g, _ := guard.New(opts)
	called := false
	_, err := g.Execute(context.Background(), guard.Attempt{Tool: "untrusted-name-canary", Arguments: "{}"}, func() (string, error) { called = true; return "", nil })
	if err == nil || called {
		t.Fatal("unknown tool bypassed guard")
	}
	var tool, blocked string
	if err := st.DB().QueryRow(`SELECT tool,blocked_by FROM tool_calls`).Scan(&tool, &blocked); err != nil || tool != "unknown" || blocked != "unknown_tool" {
		t.Fatalf("audit tool %q blocked %q err %v", tool, blocked, err)
	}
}
