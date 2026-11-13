package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
)

func recalled(t *testing.T, setup func(ctx context.Context, id string, w func(topic, text string))) []string {
	t.Helper()
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	tmpl := agent.OfflineTemplate()
	r, st := contextRuntime(t, tmpl, now)
	ctx := context.Background()
	old := now.Add(-72 * time.Hour)
	setup(ctx, tmpl.ID, func(topic, text string) {
		if _, err := st.WriteTopic(ctx, tmpl.ID, topic, "d", text, old); err != nil {
			t.Fatal(err)
		}
	})
	insertAction(t, st, tmpl.ID, "post", "testing question hooks today", now.Add(-time.Hour))
	messages, err := r.Context(ctx, "agent")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range messages {
		var d struct {
			Items []struct{ Snippet string } `json:"recalled_memory"`
		}
		if json.Unmarshal([]byte(m.Content), &d) == nil && d.Items != nil {
			var out []string
			for _, i := range d.Items {
				out = append(out, i.Snippet)
			}
			return out
		}
	}
	return nil
}

func TestRecallBringsBackARelevantOldNote(t *testing.T) {
	got := recalled(t, func(_ context.Context, _ string, w func(string, string)) {
		w("hooks", "question hooks doubled the reply rate last week")
		w("unrelated", "grocery list: rice, eggs")
	})
	if len(got) != 1 || !strings.Contains(got[0], "hooks") {
		t.Fatalf("recalled=%q, want only the hooks note", got)
	}
}

func TestRecallEmptyMemoryAddsNothing(t *testing.T) {
	if got := recalled(t, func(context.Context, string, func(string, string)) {}); got != nil {
		t.Fatalf("recalled=%q", got)
	}
}

func TestRecallIsCappedByCountAndBytes(t *testing.T) {
	got := recalled(t, func(_ context.Context, _ string, w func(string, string)) {
		for i := range 20 {
			w(fmt.Sprintf("t%d", i), "question hooks "+strings.Repeat("x ", 300))
		}
	})
	total := 0
	for _, s := range got {
		total += len(s)
	}
	if len(got) == 0 || len(got) > 6 || total > 4<<10 {
		t.Fatalf("hits=%d bytes=%d", len(got), total)
	}
}

func TestRecallIsScopedToTheAgent(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	tmpl := agent.OfflineTemplate()
	r, st := contextRuntime(t, tmpl, now)
	ctx := context.Background()
	if err := st.EnsureAgent(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteTopic(ctx, "other", "hooks", "d", "question hooks OTHERSECRET", now.Add(-72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	insertAction(t, st, tmpl.ID, "post", "question hooks", now.Add(-time.Hour))
	messages, err := r.Context(ctx, "agent")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range messages {
		if strings.Contains(m.Content, "OTHERSECRET") {
			t.Fatal("recall crossed agents")
		}
	}
}
