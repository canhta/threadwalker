package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
)

func seedLessons(t *testing.T, s *ctlServer) {
	t.Helper()
	at := ctlNow.Add(-48 * time.Hour).Format(time.RFC3339Nano)
	for _, l := range []struct{ kind, polarity, text string }{
		{"reply", "do", "Answer with one concrete number."},
		{"post", "avoid", "Open with a greeting."},
	} {
		if _, err := s.st.DB().Exec(`INSERT INTO lessons(agent_id,kind,polarity,text,evidence_json,created_at,confirmed_at) VALUES(?,?,?,?,'[]',?,?)`, s.id, l.kind, l.polarity, l.text, at, at); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCtlLessonsListsAndForgetsThroughTheService(t *testing.T) {
	s := newCtlServer(t)
	seedLessons(t, s)
	code, out, stderr := s.ctl("lessons", "--agent", s.id)
	if code != 0 || !strings.Contains(out, "#1  reply do  active\n  Answer with one concrete number.\n  applied 0") || !strings.Contains(out, "#2  post avoid  active") {
		t.Fatalf("list code=%d out=%q err=%s", code, out, stderr)
	}
	if code, out, stderr = s.ctl("lessons", "forget", "2", "--agent", s.id); code != 0 || !strings.Contains(out, "lesson #2 forgotten") {
		t.Fatalf("forget code=%d out=%q err=%s", code, out, stderr)
	}
	code, out, _ = s.ctl("lessons", "--agent", s.id, "--json")
	rows := decode[[]lessonRow](t, out)
	if code != 0 || len(rows) != 2 || rows[0].ID != 1 || !rows[0].RetiredAt.IsZero() || rows[1].ID != 2 || rows[1].RetiredBy != store.RetiredByOwner {
		t.Fatalf("after forget: %s", out)
	}
	if code, _, stderr = s.ctl("lessons", "forget", "2", "--agent", s.id); code == 0 || !strings.Contains(stderr, "no active lesson #2") {
		t.Fatalf("second forget code=%d err=%s", code, stderr)
	}
	for _, args := range [][]string{{"lessons", "forget", "--agent", s.id}, {"lessons", "forget", "x", "--agent", s.id}} {
		if code, _, _ = s.ctl(args...); code != exitUsage {
			t.Fatalf("%v code=%d", args, code)
		}
	}
}

func TestCtlStatusShowsCachedInputShare(t *testing.T) {
	s := newCtlServer(t)
	if _, out, _ := s.ctl("status"); strings.Contains(out, "cached input") {
		t.Fatalf("cached line without usage: %q", out)
	}
	code, out, stderr := s.ctl("wake", "--agent", s.id, "--json")
	if code != 0 {
		t.Fatalf("wake: %d %s", code, stderr)
	}
	o := decode[wakeOutcome](t, out)
	day := ctlNow.Format(time.DateOnly)
	if err := s.st.RecordLLMUsage(t.Context(), s.id, o.WakeID, day, store.LLMUsage{TokensIn: 200, TokensCached: 150, CostUSD: 0.01}); err != nil {
		t.Fatal(err)
	}
	total, err := s.st.LLMUsageForDay(t.Context(), s.id, day)
	if err != nil || total.TokensCached != 150 {
		t.Fatalf("ledger %+v %v", total, err)
	}
	want := fmt.Sprintf("  cached input today: %.0f%% of %d tokens\n", 15000/float64(total.TokensIn), total.TokensIn)
	if _, out, _ = s.ctl("status"); !strings.Contains(out, want) {
		t.Fatalf("status=%q want %q", out, want)
	}
}
