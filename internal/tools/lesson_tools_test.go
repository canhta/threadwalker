package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/tools"
	"github.com/canhta/threadz/internal/trends"
)

// dreamFixture is a dream wake over ten posts measured at 6h with likes
// 5 3 8 1 9 0 7 2 6 4 (views equal likes): ids 3, 5 and 7 are top; 4, 6 and 8
// are bottom.
type dreamFixture struct {
	t     *testing.T
	st    *store.Store
	d     *tools.Dispatcher
	calls int
}

func newDreamFixture(t *testing.T, phase string) *dreamFixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "dream.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	tmpl := config.Template{ID: "agent", DryRun: true, Limits: config.LimitsConfig{PostsPerDay: 3, RepliesPerDay: 5, RepliesPerUser7d: 1, MinSecondsBetweenActions: 1, MaxStepsPerWake: 10, MaxUSDPerDay: 1, MinMinutesBetweenWakes: 15}}
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	for i, likes := range []int{5, 3, 8, 1, 9, 0, 7, 2, 6, 4} {
		posted := time.Date(2026, 11, 1, i, 0, 0, 0, time.UTC)
		measured := posted.Add(6 * time.Hour).Format(time.RFC3339Nano)
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url,media_id) VALUES('agent','post','p',0,?,?,?)`, []any{posted.Format(time.RFC3339Nano), fmt.Sprintf("https://threads.test/@a/post/%d", i), fmt.Sprintf("m%d", i)}},
			{`INSERT INTO metrics(agent_id,action_id,measured_at,views,likes,replies) VALUES('agent',?,?,?,?,0)`, []any{i + 1, measured, likes, likes}},
			{`INSERT INTO metric_jobs(agent_id,action_id,window_hours,due_at,not_before,completed_at) VALUES('agent',?,6,?,?,?)`, []any{i + 1, measured, measured, measured}},
		} {
			if _, err = st.DB().Exec(q.sql, q.args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	var wakeID int64
	if phase == guard.PhaseDream {
		wakeID, err = st.BeginPhaseWake(ctx, tmpl.ID, store.WakeDream, "2026-11-10", now)
	} else {
		wakeID, err = st.BeginWake(ctx, tmpl.ID, now, now.Add(time.Hour))
	}
	if err != nil {
		t.Fatal(err)
	}
	d, err := tools.NewDispatcher(tools.Options{Scope: guard.Scope{Store: st, Template: tmpl, WakeID: wakeID, Phase: phase, Now: func() time.Time { return now }}, FetchTrends: func(context.Context) ([]trends.Trend, error) { return nil, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return &dreamFixture{t: t, st: st, d: d}
}

// call returns the tool's result, or the reason code of a refusal or failure.
func (f *dreamFixture) call(tool string, args map[string]any) (string, string) {
	f.t.Helper()
	f.calls++
	raw, _ := json.Marshal(args)
	out, err := f.d.Dispatch(context.Background(), llm.ToolCall{ID: fmt.Sprintf("c%d", f.calls), Type: "function", Function: llm.CallFunction{Name: tool, Arguments: string(raw)}})
	var blocked *guard.BlockedError
	switch {
	case errors.As(err, &blocked):
		if guard.Hint(blocked.Reason, blocked.Detail) == "" || strings.Contains(guard.Hint(blocked.Reason, blocked.Detail), "%") {
			f.t.Errorf("%s has no usable hint: %q", blocked.Reason, guard.Hint(blocked.Reason, blocked.Detail))
		}
		return "", blocked.Reason
	case err != nil:
		return "", "error: " + err.Error()
	}
	return out, ""
}

func TestLessonToolsAreOfferedOnlyToTheDream(t *testing.T) {
	for _, phase := range []string{guard.PhaseAgent, guard.PhaseDream} {
		f := newDreamFixture(t, phase)
		offered := map[string]bool{}
		for _, def := range f.d.Definitions() {
			offered[def.Function.Name] = true
			if spec, _ := guard.Spec(def.Function.Name); len(spec.Description) > 300 && strings.HasPrefix(spec.Name, "lesson_") {
				t.Errorf("%s description is %d characters", spec.Name, len(spec.Description))
			}
		}
		for _, name := range []string{"lesson_add", "lesson_confirm", "lesson_retire"} {
			if offered[name] != (phase == guard.PhaseDream) {
				t.Errorf("%s offered=%v in %s", name, offered[name], phase)
			}
		}
	}
	f := newDreamFixture(t, guard.PhaseAgent)
	if _, reason := f.call("lesson_add", map[string]any{"text": "x", "kind": "post", "polarity": "do", "action_ids": []int{3, 5}}); reason != "phase_read_only" {
		t.Fatalf("agent wake called lesson_add: %q", reason)
	}
}

func TestLessonAddGates(t *testing.T) {
	f := newDreamFixture(t, guard.PhaseDream)
	// A tool refused three times for one reason closes, so each gate gets its own wake.
	add := func(text, kind, polarity string, ids ...int) string {
		_, reason := newDreamFixture(t, guard.PhaseDream).call("lesson_add", map[string]any{"text": text, "kind": kind, "polarity": polarity, "action_ids": ids})
		return reason
	}
	for _, tc := range []struct {
		name, text, kind, polarity string
		ids                        []int
		want                       string
	}{
		{"one citation", "Mở bằng số.", "post", "do", []int{3}, "lesson_citations"},
		{"bad kind", "Mở bằng số.", "story", "do", []int{3, 5}, "lesson_enum"},
		{"bad polarity", "Mở bằng số.", "post", "should", []int{3, 5}, "lesson_enum"},
		{"mid action", "Mở bằng số.", "post", "do", []int{3, 1}, "lesson_band"},
		{"avoid on tops", "Mở bằng số.", "post", "avoid", []int{3, 5}, "lesson_band"},
		{"wrong kind", "Mở bằng số.", "reply", "do", []int{3, 5}, "lesson_kind_mismatch"},
		{"p ref", "Bài p12 mở bằng số.", "post", "do", []int{3, 5}, "stale_ref"},
		{"number not measured", "Bài có 77 lượt thích.", "post", "do", []int{3, 5}, "lesson_number_mismatch"},
		{"foreign link", "Xem https://example.com/x", "post", "do", []int{3, 5}, "lesson_foreign_url"},
		{"iso date", "Bài ngày 2026-11-10 mở bằng số.", "post", "do", []int{3, 5}, "lesson_has_ref"},
		{"action id", "Làm như action 3 mở bằng số.", "post", "do", []int{3, 5}, "lesson_has_ref"},
		{"action id with underscore", "Giống action_id: 5.", "post", "do", []int{3, 5}, "lesson_has_ref"},
		{"secret", "sk-abcdefghijklmnopqrstuvwx", "post", "do", []int{3, 5}, "lesson_secret"},
		{"too long", strings.Repeat("ab ", 81), "post", "do", []int{3, 5}, "lesson_too_long"},
	} {
		if got := add(tc.text, tc.kind, tc.polarity, tc.ids...); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	// Numbers may be the counters of any cited action: 8 is on action 3, 9 on action 5.
	out, reason := f.call("lesson_add", map[string]any{"text": "Bài có 8 và 9 lượt thích mở bằng số.", "kind": "post", "polarity": "do", "action_ids": []int{3, 5}})
	if reason != "" || !strings.Contains(out, `"lesson_id":1`) {
		t.Fatalf("add: %q %q", out, reason)
	}
	if _, reason = f.call("lesson_add", map[string]any{"text": "BÀI có 8 và 9 lượt thích, mở bằng số", "kind": "post", "polarity": "do", "action_ids": []int{3, 5}}); reason != "lesson_duplicate" {
		t.Errorf("duplicate: %q", reason)
	}

	if _, reason = f.call("lesson_confirm", map[string]any{"id": 1, "action_ids": []int{7}}); reason != "lesson_old_episode" {
		t.Errorf("confirm with an episode older than the lesson: %q", reason)
	}
	if _, err := f.st.DB().Exec(`UPDATE lessons SET confirmed_at='2026-11-01T00:00:00Z' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, reason = f.call("lesson_confirm", map[string]any{"id": 1, "action_ids": []int{3}}); reason != "lesson_no_new_evidence" {
		t.Errorf("confirm without new evidence: %q", reason)
	}
	if _, reason = f.call("lesson_confirm", map[string]any{"id": 1, "action_ids": []int{7}}); reason != "" {
		t.Errorf("confirm: %q", reason)
	}
	if _, reason = f.call("lesson_confirm", map[string]any{"id": 2, "action_ids": []int{7}}); reason != "lesson_unknown" {
		t.Errorf("confirm unknown: %q", reason)
	}
	if _, reason = f.call("lesson_retire", map[string]any{"id": 1, "reason": "no longer holds"}); reason != "" {
		t.Errorf("retire: %q", reason)
	}
	if _, reason = f.call("lesson_retire", map[string]any{"id": 1, "reason": "again"}); reason != "lesson_unknown" {
		t.Errorf("retire twice: %q", reason)
	}
	all, _ := f.st.Lessons(context.Background(), "agent", true)
	if len(all) != 1 || all[0].RetiredBy != "dream" || len(all[0].Evidence) != 3 {
		t.Fatalf("stored %+v", all)
	}
}
