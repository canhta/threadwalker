package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
)

type ownAction struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type wakeData struct {
	OwnActions []ownAction `json:"own_actions"`
	Counters   struct {
		Posts   int `json:"posts_today"`
		Replies int `json:"replies_today"`
	} `json:"counters"`
}

func contextRuntime(t *testing.T, tmpl config.Template, now time.Time) (*agent.Runtime, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return r, st
}

func insertAction(t *testing.T, st *store.Store, agentID, kind, text string, at time.Time) {
	t.Helper()
	if _, err := st.DB().Exec(`INSERT INTO actions(agent_id,kind,text,dry_run,posted_at) VALUES(?,?,?,1,?)`, agentID, kind, text, at.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}

func decodeWake(t *testing.T, r *agent.Runtime) wakeData {
	t.Helper()
	messages, err := r.Context(context.Background(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	var data wakeData
	if err = json.Unmarshal([]byte(messages[1].Content), &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSystemPromptGolden(t *testing.T) {
	tmpls, err := config.Load("../../templates", config.Options{Getenv: func(string) string { return "dummy" }})
	if err != nil {
		t.Fatal(err)
	}
	viral := tmpls[len(tmpls)-1] // sorted by file name; viral sorts last
	if viral.ID != "viral" {
		t.Fatalf("last template = %q, want viral", viral.ID)
	}
	r, _ := contextRuntime(t, viral, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	messages, err := r.Context(context.Background(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "viral.system.golden")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err = os.WriteFile(path, []byte(messages[0].Content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if messages[0].Role != "system" || messages[0].Content != string(want) {
		t.Fatalf("system prompt differs from %s; rerun with UPDATE_GOLDEN=1 after reviewing\n%s", path, messages[0].Content)
	}
}

func TestWakeContextHoldsOnlyOwnRecentActions(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	tmpl := agent.OfflineTemplate()
	r, st := contextRuntime(t, tmpl, now)
	insertAction(t, st, tmpl.ID, "post", "own fresh post", now.Add(-time.Hour))
	insertAction(t, st, tmpl.ID, "reply", "own fresh reply", now.Add(-2*time.Hour))
	insertAction(t, st, tmpl.ID, "post", "own stale post", now.Add(-25*time.Hour))
	insertAction(t, st, "other", "post", "OTHER AGENT TEXT", now.Add(-time.Hour))
	data := decodeWake(t, r)
	if len(data.OwnActions) != 2 || data.OwnActions[0].Text != "own fresh post" || data.OwnActions[1].Kind != "reply" {
		t.Fatalf("own_actions=%+v", data.OwnActions)
	}
	if data.Counters.Posts != 1 || data.Counters.Replies != 1 {
		t.Fatalf("counters=%+v", data.Counters)
	}
	messages, _ := r.Context(context.Background(), "agent")
	if strings.Contains(messages[0].Content+messages[1].Content, "OTHER AGENT TEXT") {
		t.Fatal("another agent's action in context")
	}
}

func TestWakeContextRedactsOwnActions(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	tmpl := agent.OfflineTemplate()
	r, st := contextRuntime(t, tmpl, now)
	insertAction(t, st, tmpl.ID, "post", "leaked offline-dummy-key here", now.Add(-time.Hour))
	messages, _ := r.Context(context.Background(), "agent")
	if strings.Contains(messages[1].Content, "offline-dummy-key") {
		t.Fatal("secret in own action text")
	}
}

func TestWakeContextIsBoundedWith500Actions(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	tmpl := agent.OfflineTemplate()
	r, st := contextRuntime(t, tmpl, now)
	long := strings.Repeat("ạ", 2000)
	for i := 0; i < 500; i++ {
		insertAction(t, st, tmpl.ID, "reply", fmt.Sprintf("%03d %s", i, long), now.Add(-time.Duration(i+1)*time.Second))
	}
	data := decodeWake(t, r)
	bytes := 0
	for _, a := range data.OwnActions {
		bytes += len(a.Text)
	}
	if len(data.OwnActions) == 0 || len(data.OwnActions) > 30 || bytes > 8<<10 {
		t.Fatalf("actions=%d bytes=%d", len(data.OwnActions), bytes)
	}
	if !strings.HasPrefix(data.OwnActions[0].Text, "000 ") {
		t.Fatalf("newest action not first: %.10s", data.OwnActions[0].Text)
	}
	if data.Counters.Replies != 500 {
		t.Fatalf("counters count every action: %+v", data.Counters)
	}
}
