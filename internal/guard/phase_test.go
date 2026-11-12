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

func openPhaseGuard(t *testing.T, phase string) (*store.Store, *guard.Guard, int64) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "phase.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tmpl := config.Template{ID: "phase-agent", DryRun: true, Limits: config.LimitsConfig{PostsPerDay: 3, RepliesPerDay: 5, RepliesPerUser7d: 1, MinSecondsBetweenActions: 1, MaxStepsPerWake: 10, MaxUSDPerDay: 1, MinMinutesBetweenWakes: 1}}
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	kind := store.WakeDream
	if phase == guard.PhaseStudy {
		kind = store.WakeStudy
	}
	wakeID, err := st.BeginPhaseWake(ctx, tmpl.ID, kind, "2026-01-01", time.Date(2026, 1, 1, 23, 40, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	g, err := guard.New(guard.Options{Store: st, Template: tmpl, WakeID: wakeID, Phase: phase, Now: func() time.Time { return time.Date(2026, 1, 1, 23, 40, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	return st, g, wakeID
}

func TestGuardBlocksThreadsAndOwnerWritesInDreamAndStudy(t *testing.T) {
	for _, phase := range []string{guard.PhaseDream, guard.PhaseStudy} {
		t.Run(phase, func(t *testing.T) {
			st, g, wakeID := openPhaseGuard(t, phase)
			writeCalls := map[string]string{
				"post":                `{"text":"synthetic take","hypothesis_ids":[]}`,
				"reply":               `{"text":"synthetic reply","target_url":"https://threads.net/@someone/post/1","target_username":"someone","hypothesis_ids":[]}`,
				"notify_owner":        `{"text":"synthetic"}`,
				"ask_owner":           `{"text":"synthetic"}`,
				"mark_do_not_contact": `{"username":"someone"}`,
				"schedule_next_wake":  `{"at":"2026-01-02T01:00:00Z"}`,
			}
			for tool, args := range writeCalls {
				var ran bool
				_, err := g.Execute(context.Background(), guard.Attempt{Tool: tool, Arguments: args}, func() (string, error) {
					ran = true
					return "effect", nil
				})
				if !errors.Is(err, guard.ErrBlocked) || ran {
					t.Fatalf("%s in %s: err=%v ran=%v", tool, phase, err, ran)
				}
			}
			var blocked string
			if err := st.DB().QueryRow(`SELECT blocked_by FROM tool_calls WHERE wake_id=? ORDER BY id DESC LIMIT 1`, wakeID).Scan(&blocked); err != nil || blocked != "phase_read_only" {
				t.Fatalf("blocked_by=%q err=%v", blocked, err)
			}
			for _, tool := range []string{"note_write", "note_search", "playbook_read", "playbook_propose", "fetch_trends", "read_feed", "search", "read_post", "read_own_metrics"} {
				var ran bool
				_, err := g.Execute(context.Background(), guard.Attempt{Tool: tool, Arguments: allowedArgs(tool)}, func() (string, error) {
					ran = true
					return "ok", nil
				})
				if !ran {
					t.Fatalf("%s should run in %s: err=%v", tool, phase, err)
				}
			}
		})
	}
}

func allowedArgs(tool string) string {
	switch tool {
	case "note_write", "playbook_propose":
		return `{"text":"synthetic note"}`
	case "note_search":
		return `{"query":"synthetic"}`
	case "search":
		return `{"query":"synthetic"}`
	case "read_post":
		return `{"url":"https://threads.net/@someone/post/1"}`
	default:
		return `{}`
	}
}

func TestWebSearchIsStudyOnly(t *testing.T) {
	if guard.PhaseAllowed(guard.PhaseDream, "web_search") || guard.PhaseAllowed(guard.PhaseAgent, "web_search") {
		t.Fatal("web_search allowed outside study")
	}
	if !guard.PhaseAllowed(guard.PhaseStudy, "web_search") {
		t.Fatal("web_search refused in study")
	}
	for _, tool := range []string{"post", "reply", "notify_owner", "schedule_next_wake"} {
		if guard.PhaseAllowed(guard.PhaseStudy, tool) {
			t.Fatalf("%s allowed in study", tool)
		}
	}
}
