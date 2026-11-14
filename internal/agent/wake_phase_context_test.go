package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/store"
)

func TestPhaseContextsShareOwnActionsAndDifferInJob(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	r, err := New(Options{Store: st, Template: OfflineTemplate(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec(`INSERT INTO actions(agent_id,kind,text,dry_run,posted_at) VALUES(?,?,?,1,?)`, r.opts.Template.ID, "post", "own line", now.Add(-time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	for phase, job := range map[string]string{guard.PhaseAgent: "A normal wake", guard.PhaseDream: "A dream", guard.PhaseStudy: "A study session"} {
		messages, err := r.wakeContext(ctx, phase, 0)
		if err != nil {
			t.Fatalf("%s: %v", phase, err)
		}
		if !strings.Contains(messages[0].Content, job) || !strings.Contains(messages[1].Content, "own line") {
			t.Fatalf("%s: job or own actions missing", phase)
		}
	}
	if _, err = r.wakeContext(ctx, "other", 0); err == nil {
		t.Fatal("unknown phase accepted")
	}
}

func TestSystemPromptNeverForcesANoteAndOffersMemoryAddOnlyToTheDream(t *testing.T) {
	tmpl := OfflineTemplate()
	for _, phase := range []string{guard.PhaseAgent, guard.PhaseDream, guard.PhaseStudy} {
		prompt := systemPrompt(tmpl, phase)
		for _, forced := range []string{"End the wake", "one-line lessons", "saying why", "Save what you learned"} {
			if strings.Contains(prompt, forced) {
				t.Errorf("%s prompt forces a write: %q", phase, forced)
			}
		}
		if got := strings.Contains(prompt, "memory_add"); got != (phase == guard.PhaseDream) {
			t.Errorf("%s prompt names memory_add = %v", phase, got)
		}
	}
}

func TestHiddenToolsLeaveTheWakeInstructions(t *testing.T) {
	tmpl := OfflineTemplate()
	full := systemPrompt(tmpl, guard.PhaseAgent)
	if !strings.Contains(full, "without target_id") || !strings.Contains(full, "Threads search hit") || strings.Contains(full, "profile_posts") || strings.Contains(full, "read_post,") {
		t.Fatal("full prompt should mention search and the permalink reply path only")
	}
	hidden := systemPrompt(tmpl, guard.PhaseAgent, "search", "profile_posts")
	for _, word := range []string{"profile_posts", "Threads search hit", " search,"} {
		if strings.Contains(hidden, word) {
			t.Fatalf("hidden prompt still mentions %q", word)
		}
	}
}

func TestOpenOwnerNoteIsTheFirstTaskOfAWake(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	r, err := New(Options{Store: st, Template: OfflineTemplate(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.OwnerNote(ctx, []string{r.opts.Template.ID}, "k", "post about the AP story", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	messages, err := r.wakeContext(ctx, guard.PhaseAgent, 0)
	if err != nil || !strings.HasPrefix(messages[1].Content, "# First task: owner notes") || !strings.Contains(messages[1].Content, "post about the AP story") {
		t.Fatalf("user message does not open with the note: %v", err)
	}
	if messages, err = r.wakeContext(ctx, guard.PhaseDream, 0); err != nil || strings.Contains(messages[1].Content, "First task") {
		t.Fatalf("a dream cannot post, so it carries no note task: %v", err)
	}
}
