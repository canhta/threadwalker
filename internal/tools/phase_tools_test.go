package tools_test

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/tools"
	"github.com/canhta/threadz/internal/trends"
)

type fakeOwner struct{}

func (fakeOwner) QueueOwner(context.Context, string, string, bool) (int64, error) { return 1, nil }

func phaseDispatcher(t *testing.T, phase string) *tools.Dispatcher {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "phase.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	tmpl := config.Template{ID: "agent", DryRun: true, Limits: config.LimitsConfig{PostsPerDay: 3, RepliesPerDay: 5, RepliesPerUser7d: 1, MinSecondsBetweenActions: 1, MaxStepsPerWake: 10, MaxUSDPerDay: 1, MinMinutesBetweenWakes: 15}}
	tmpl.Study.WebSearch = true
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	var wakeID int64
	switch phase {
	case guard.PhaseDream:
		wakeID, err = st.BeginPhaseWake(ctx, tmpl.ID, store.WakeDream, "2026-01-01", now)
	case guard.PhaseStudy:
		wakeID, err = st.BeginPhaseWake(ctx, tmpl.ID, store.WakeStudy, "2026-01-01T09:00", now)
	default:
		wakeID, err = st.BeginWake(ctx, tmpl.ID, now, now.Add(time.Hour))
	}
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return now }
	d, err := tools.NewDispatcher(tools.Options{Embedder: testEmbedder(t, st), Scope: guard.Scope{Store: st, Template: tmpl, WakeID: wakeID, Phase: phase, Now: clock}, Owner: fakeOwner{}, SearchWeb: trends.NewWebSearcher(http.DefaultClient), FetchTrends: func(context.Context) ([]trends.Trend, error) { return nil, nil }, FetchStudy: func(context.Context) (trends.StudyFeed, error) { return trends.StudyFeed{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestPhasesAreOfferedOnlyToolsTheyMayCall(t *testing.T) {
	for _, phase := range []string{guard.PhaseAgent, guard.PhaseDream, guard.PhaseStudy} {
		t.Run(phase, func(t *testing.T) {
			offered := map[string]bool{}
			for _, def := range phaseDispatcher(t, phase).Definitions() {
				offered[def.Function.Name] = true
				if spec, ok := guard.Spec(def.Function.Name); !ok || !spec.AllowedIn(phase) {
					t.Fatalf("%s offered in %s", def.Function.Name, phase)
				}
			}
			for _, name := range []string{"post", "reply", "mark_do_not_contact", "notify_owner", "ask_owner"} {
				if offered[name] == (phase != guard.PhaseAgent) {
					t.Fatalf("%s offered=%v in %s", name, offered[name], phase)
				}
			}
			if offered["web_search"] != (phase != guard.PhaseDream) {
				t.Fatalf("web_search offered=%v in %s", offered["web_search"], phase)
			}
		})
	}
}
