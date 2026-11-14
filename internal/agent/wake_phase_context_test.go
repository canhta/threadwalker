package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/store"
)

func TestPhaseContextsDifferInPromptAndData(t *testing.T) {
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
	agentMsgs, err := r.wakeContext(ctx, guard.PhaseAgent, 0, "")
	if err != nil || !strings.Contains(agentMsgs[0].Content, "A normal wake") || !strings.Contains(agentMsgs[1].Content, `"counters"`) {
		t.Fatalf("agent context: %v", err)
	}
	dreamMsgs, err := r.wakeContext(ctx, guard.PhaseDream, 0, "")
	if err != nil || dreamMsgs[0].Content != dreamPrompt || !strings.Contains(dreamMsgs[1].Content, `"new_episodes"`) || strings.Contains(dreamMsgs[1].Content, `"counters"`) {
		t.Fatalf("dream context: %v", err)
	}
	if strings.Contains(dreamMsgs[0].Content, OfflineTemplate().Persona.Name) {
		t.Fatal("the dream prompt carries the persona")
	}
	if _, err = r.wakeContext(ctx, "other", 0, ""); err == nil {
		t.Fatal("unknown phase accepted")
	}
}

func TestSystemPromptNeverForcesAWriteOrNamesARemovedTool(t *testing.T) {
	tmpl := OfflineTemplate()
	for _, phase := range []string{guard.PhaseAgent, guard.PhaseDream} {
		prompt := systemPrompt(tmpl, phase)
		for _, forced := range []string{"End the wake", "one-line lessons", "saying why", "Save what you learned"} {
			if strings.Contains(prompt, forced) {
				t.Errorf("%s prompt forces a write: %q", phase, forced)
			}
		}
		for _, removed := range []string{"memory_add", "note_write", "playbook", "strategy_write", "knowledge_read", "read_own_metrics", "explore"} {
			if strings.Contains(prompt, removed) {
				t.Errorf("%s prompt names the removed %q", phase, removed)
			}
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
	messages, err := r.wakeContext(ctx, guard.PhaseAgent, 0, "# Inbox: someone replied\n\n")
	if err != nil || !strings.Contains(messages[1].Content, "# First task: owner notes") || !strings.Contains(messages[1].Content, "post about the AP story") {
		t.Fatalf("user message lacks the note: %v", err)
	}
	if inbox, note := strings.Index(messages[1].Content, "# Inbox"), strings.Index(messages[1].Content, "# First task"); inbox < 0 || inbox > note || strings.Index(messages[1].Content, `"counters"`) > inbox {
		t.Fatal("the data JSON comes first, then the inbox, then the owner notes")
	}
	if messages, err = r.wakeContext(ctx, guard.PhaseDream, 0, ""); err != nil || strings.Contains(messages[1].Content, "First task") {
		t.Fatalf("a dream cannot post, so it carries no note task: %v", err)
	}
}
