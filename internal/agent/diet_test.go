package agent_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/store"
)

func TestWakeDataMessageDiet(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	tmpl := agent.OfflineTemplate()
	tmpl.Persona.AvoidTopics = []string{"politics"}
	r, st := contextRuntime(t, tmpl, now)
	ctx := context.Background()
	for i := 0; i < 30; i++ {
		insertAction(t, st, tmpl.ID, "post", fmt.Sprintf("own post %d %s", i, strings.Repeat("noi dung ", 16)), now.Add(-time.Duration(i+1)*20*time.Minute))
	}
	for i := 0; i < 40; i++ {
		if err := st.AddMemoryEntry(ctx, tmpl.ID, fmt.Sprintf("MEM%02d %s", i, strings.Repeat("lesson ", 15)), now, store.MemoryActor{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, topic := range []string{"hooks", "2026-01-01 old log", "series"} {
		if _, err := st.WriteTopic(ctx, tmpl.ID, topic, "desc", "body", now); err != nil {
			t.Fatal(err)
		}
	}
	var stories []store.TrendStory
	for i := 0; i < 10; i++ {
		u := fmt.Sprintf("https://news.google.com/rss/articles/%s?oc=5", strings.Repeat("CBMi", 60))
		title := fmt.Sprintf("story %d", i)
		if i == 3 {
			title = "politics scandal"
		}
		if i == 4 {
			u = "https://example.com/real"
		}
		stories = append(stories, store.TrendStory{Source: "news", Title: title, URL: u, Rank: i})
	}
	if err := st.RecordTrendStories(ctx, tmpl.ID, "study", stories, now.Add(-time.Hour), 10); err != nil {
		t.Fatal(err)
	}
	messages, err := r.Context(ctx, "agent")
	if err != nil {
		t.Fatal(err)
	}
	body := messages[1].Content
	t.Logf("data message bytes: %d", len(body))
	if len(body) > 12<<10 {
		t.Fatalf("data message is %d bytes, want under 12 KB", len(body))
	}
	for _, bad := range []string{"threads_trend_brief", "news.google.com", "politics scandal", "2026-01-01 old log", "MEM00"} {
		if strings.Contains(body, bad) {
			t.Errorf("data message still has %q", bad)
		}
	}
	for _, good := range []string{"story 0", "example.com/real", `"hooks"`, "MEM39"} {
		if !strings.Contains(body, good) {
			t.Errorf("data message lost %q", good)
		}
	}
	if strings.Index(body, "MEM39") > strings.Index(body, "MEM38") {
		t.Error("memory index not newest first")
	}
}
