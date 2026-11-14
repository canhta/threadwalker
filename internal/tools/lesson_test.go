package tools_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/tools"
	"github.com/canhta/threadz/internal/trends"
)

func dreamSetup(t *testing.T) (*store.Store, *tools.Dispatcher, *bytes.Buffer) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "dream.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	_, err = st.DB().Exec(`INSERT INTO agents(id,status) VALUES('agent','running'),('other','running');
INSERT INTO actions(id,agent_id,kind,text,thread_url,dry_run,posted_at) VALUES
 (1,'agent','post','mine','https://threads.example/p/1',0,'2026-11-10T00:00:00Z'),
 (2,'other','post','theirs','https://threads.example/p/2',0,'2026-11-10T00:00:00Z');
INSERT INTO metrics(agent_id,action_id,measured_at,views,likes,replies,reposts) VALUES
 ('agent',1,'2026-11-10T01:00:00Z',120,7,3,1),
 ('other',2,'2026-11-10T01:00:00Z',999,50,9,4)`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	tmpl := config.Template{ID: "agent", DryRun: true, Limits: config.LimitsConfig{PostsPerDay: 100, RepliesPerDay: 100, RepliesPerUser7d: 10, MinSecondsBetweenActions: 1, MinMinutesBetweenWakes: 15}}
	wakeID, err := st.BeginPhaseWake(ctx, tmpl.ID, store.WakeDream, "2026-11-11", now)
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	d, err := tools.NewDispatcher(tools.Options{Embedder: testEmbedder(t, st), Scope: guard.Scope{Store: st, Template: tmpl, WakeID: wakeID, Phase: guard.PhaseDream, Now: func() time.Time { return now }, Logger: slog.New(slog.NewJSONHandler(logs, nil))}, FetchTrends: func(context.Context) ([]trends.Trend, error) { return nil, nil }, FetchStudy: func(context.Context) (trends.StudyFeed, error) { return trends.StudyFeed{}, nil }, SearchWeb: trends.NewWebSearcher(http.DefaultClient)})
	if err != nil {
		t.Fatal(err)
	}
	return st, d, logs
}

func TestDreamLessonChecks(t *testing.T) {
	for _, tc := range []struct {
		name, args, reason string
	}{
		{"valid", `{"text":"Post 120 views, 7 likes: short roasts work","action_id":1}`, ""},
		{"no numbers", `{"text":"Short roasts work","action_id":1}`, ""},
		{"own url", `{"text":"see https://threads.example/p/1 for the shape","action_id":1}`, ""},
		{"missing id", `{"text":"Short roasts work"}`, "lesson_missing_citation"},
		{"foreign id", `{"text":"Short roasts work","action_id":2}`, "lesson_foreign_citation"},
		{"unknown id", `{"text":"Short roasts work","action_id":77}`, "lesson_foreign_citation"},
		{"wrong number", `{"text":"Post got 121 views","action_id":1}`, "lesson_number_mismatch"},
		{"other agents number", `{"text":"Post got 999 views","action_id":1}`, "lesson_number_mismatch"},
		{"decimal", `{"text":"Post got 7.5 likes","action_id":1}`, "lesson_number_mismatch"},
		{"foreign url", `{"text":"see https://threads.example/p/2","action_id":1}`, "lesson_foreign_url"},
		{"secret", `{"text":"key sk-abcdefghijklmnopqrstuvwx","action_id":1}`, "lesson_secret"},
		{"bearer", `{"text":"use Bearer abcdefghijklmnopqrstuvwx","action_id":1}`, "lesson_secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, d, logs := dreamSetup(t)
			_, err := d.Dispatch(context.Background(), call("memory_add", tc.args))
			index, _ := st.MemoryIndex(context.Background(), "agent", false)
			if tc.reason == "" {
				if err != nil || len(index) != 1 {
					t.Fatalf("err %v index %v", err, index)
				}
				return
			}
			if err == nil || len(index) != 0 {
				t.Fatalf("accepted: err %v index %v", err, index)
			}
			var result string
			if err := st.DB().QueryRow(`SELECT result_json FROM tool_calls WHERE tool='memory_add'`).Scan(&result); err != nil || !strings.Contains(result, `"reason":"`+tc.reason+`"`) {
				t.Fatalf("audit row %q: %v", result, err)
			}
			if !strings.Contains(logs.String(), `"event":"lesson.rejected"`) || !strings.Contains(logs.String(), `"reason":"`+tc.reason+`"`) {
				t.Fatalf("missing log %s: %s", tc.reason, logs)
			}
		})
	}
}

func TestDreamLessonCap(t *testing.T) {
	st, d, _ := dreamSetup(t)
	for i := 0; i < guard.MaxDreamLessons+2; i++ {
		_, err := d.Dispatch(context.Background(), call("memory_add", fmt.Sprintf(`{"text":"Short roasts work %c","action_id":1}`, rune('a'+i))))
		if (err != nil) != (i >= guard.MaxDreamLessons) {
			t.Fatalf("lesson %d: %v", i, err)
		}
	}
	if index, _ := st.MemoryIndex(context.Background(), "agent", false); len(index) != guard.MaxDreamLessons {
		t.Fatal(index)
	}
}

func TestStrategyWriteRequiresOwnedCitation(t *testing.T) {
	for _, tc := range []struct {
		name, args string
		ok         bool
	}{
		{"cited", `{"text":"short roasts, no hashtags","citations":["action:1"]}`, true},
		{"uncited", `{"text":"short roasts"}`, false},
		{"empty citations", `{"text":"short roasts","citations":[]}`, false},
		{"foreign action", `{"text":"short roasts","citations":["action:2"]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, d, _ := dreamSetup(t)
			_, err := d.Dispatch(context.Background(), call("strategy_write", tc.args))
			history, _ := st.StrategyHistory(context.Background(), "agent")
			if tc.ok != (err == nil) || tc.ok != (len(history) == 1) {
				t.Fatalf("err %v history %v", err, history)
			}
		})
	}
}

func TestMemorySupersedeToolArchivesAndJournals(t *testing.T) {
	st, d, _ := dreamSetup(t)
	ctx := context.Background()
	for _, text := range []string{"post at 21h", "post at 7h"} {
		if err := st.AddMemoryEntry(ctx, "agent", text, time.Now(), store.MemoryActor{Phase: guard.PhaseDream}); err != nil {
			t.Fatal(err)
		}
	}
	live, _ := st.MemoryIndex(ctx, "agent", false)
	if _, err := d.Dispatch(ctx, call("memory_supersede", fmt.Sprintf(`{"old_id":%d,"new_id":%d}`, live[0].ID, live[1].ID))); err != nil {
		t.Fatal(err)
	}
	if left, _ := st.MemoryIndex(ctx, "agent", false); len(left) != 1 || left[0].Text != "post at 7h" {
		t.Fatalf("index = %v", left)
	}
	var wake int64
	if err := st.DB().QueryRow(`SELECT wake_id FROM memory_journal WHERE change='superseded'`).Scan(&wake); err != nil || wake == 0 {
		t.Fatalf("journal wake %d, %v", wake, err)
	}
}
