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

func TestWakeContextOwnActionsMatchModeAndOmittedIsNeverNegative(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	tmpl := agent.OfflineTemplate()
	r, st := contextRuntime(t, tmpl, now)
	ins := func(dry, failed int, text string, at time.Time) {
		t.Helper()
		if _, err := st.DB().Exec(`INSERT INTO actions(agent_id,kind,text,dry_run,failed,posted_at) VALUES(?,'reply',?,?,?,?)`, tmpl.ID, text, dry, failed, at.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 26; i++ {
		ins(1, 0, fmt.Sprintf("dry %d", i), now.Add(-time.Duration(i+1)*time.Minute))
	}
	for i := 0; i < 4; i++ {
		ins(0, 0, "LIVE ROW TEXT", now.Add(-time.Duration(i)*time.Second))
	}
	ins(1, 1, "FAILED TEXT", now.Add(-time.Second))
	messages, err := r.Context(context.Background(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(messages[1].Content, "LIVE ROW TEXT") || strings.Contains(messages[1].Content, "FAILED TEXT") {
		t.Fatal("live or failed row in a dry-run context")
	}
	var data struct {
		OwnActions []ownAction `json:"own_actions"`
		Counters   struct {
			InWindow int `json:"own_actions_in_window"`
			Shown    int `json:"own_actions_shown"`
			Omitted  int `json:"own_actions_omitted"`
		} `json:"counters"`
	}
	if err = json.Unmarshal([]byte(messages[1].Content), &data); err != nil {
		t.Fatal(err)
	}
	if c := data.Counters; c.InWindow != 26 || c.Shown != 26 || c.Omitted != 0 || len(data.OwnActions) != 26 {
		t.Fatalf("counters=%+v shown=%d, want 26 in window, 26 shown, 0 omitted", c, len(data.OwnActions))
	}
}
