package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
)

func openNotesTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "threadz.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing store: %v", err)
		}
	})
	return st
}

func insertNotesTestAgent(t *testing.T, st *store.Store, agentID string) {
	t.Helper()
	_, err := st.DB().ExecContext(context.Background(), `
		INSERT INTO agents (id, status, next_wake_at)
		VALUES (?, 'running', NULL)
	`, agentID)
	if err != nil {
		t.Fatalf("inserting agent %q: %v", agentID, err)
	}
}

var noteSeq atomic.Int64

func writeNote(st *store.Store, ctx context.Context, agentID, text string, at time.Time) (int64, error) {
	return st.WriteTopic(ctx, agentID, fmt.Sprintf("topic-%d", noteSeq.Add(1)), "", text, at)
}

func countRows(t *testing.T, st *store.Store, query string, args ...any) int {
	t.Helper()
	var count int
	if err := st.DB().QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("counting rows with %q: %v", query, err)
	}
	return count
}

func TestWriteTopic_InsertsNoteAndFTS(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "agent-a")

	at := time.Date(2026, 11, 11, 9, 30, 15, 123456789, time.UTC)
	text := "Vietnam builders prefer practical examples"

	id, err := writeNote(st, ctx, "agent-a", text, at)
	if err != nil {
		t.Fatalf("WriteTopic: %v", err)
	}
	if id <= 0 {
		t.Fatalf("WriteTopic returned id %d, want a positive id", id)
	}

	var agentID, gotText, createdAt string
	if err := st.DB().QueryRowContext(ctx,
		`SELECT agent_id, text, created_at FROM notes WHERE id = ?`, id).
		Scan(&agentID, &gotText, &createdAt); err != nil {
		t.Fatalf("reading note %d: %v", id, err)
	}
	if agentID != "agent-a" || gotText != text {
		t.Fatalf("note = agent %q text %q, want agent-a %q", agentID, gotText, text)
	}
	if want := at.Format(time.RFC3339Nano); createdAt != want {
		t.Fatalf("created_at = %q, want %q", createdAt, want)
	}

	hits, err := st.SearchMemory(ctx, "agent-a", "Vietnam", nil, time.Time{}, time.Time{}, 10)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(hits) != 1 || hits[0].Ref != fmt.Sprintf("note:%d", id) || hits[0].Kind != "note" || !hits[0].At.Equal(at) {
		t.Fatalf("SearchMemory = %+v, want note %d at %v", hits, id, at)
	}
	got, err := st.ReadMemory(ctx, "agent-a", hits[0].Ref)
	if err != nil {
		t.Fatalf("ReadMemory: %v", err)
	}
	if got.Text != text || got.Kind != "note" || !got.At.Equal(at) {
		t.Fatalf("ReadMemory = %+v, want text %q at %v", got, text, at)
	}
}

func searchRefs(t *testing.T, st *store.Store, agent, query string, since, until time.Time, limit int) []string {
	t.Helper()
	hits, err := st.SearchMemory(context.Background(), agent, query, nil, since, until, limit)
	if err != nil {
		t.Fatalf("SearchMemory(%q): %v", query, err)
	}
	refs := make([]string, len(hits))
	for i, hit := range hits {
		refs[i] = hit.Ref
	}
	return refs
}

func insertAction(t *testing.T, st *store.Store, agent, kind, text string, at time.Time) int64 {
	t.Helper()
	result, err := st.DB().Exec(`INSERT INTO actions (agent_id, kind, text, posted_at) VALUES (?, ?, ?, ?)`, agent, kind, text, at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("inserting action: %v", err)
	}
	id, _ := result.LastInsertId()
	return id
}

func TestSearchMemory_PunctuationAndOperatorsNeverError(t *testing.T) {
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "agent-a")
	id, err := writeNote(st, context.Background(), "agent-a", "co-op not and or near: pricing", time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`"`, `"unterminated`, `co-op`, `a:b`, `AND`, `NOT`, `OR NEAR`, `x AND`, `(`, `*`, `^pricing`, `pricing"; DROP TABLE notes; --`, `NOT pricing`} {
		if _, err := st.SearchMemory(context.Background(), "agent-a", query, nil, time.Time{}, time.Time{}, 10); err != nil {
			t.Fatalf("SearchMemory(%q) error: %v", query, err)
		}
	}
	if got := searchRefs(t, st, "agent-a", "NOT pricing", time.Time{}, time.Time{}, 10); len(got) != 1 || got[0] != fmt.Sprintf("note:%d", id) {
		t.Fatalf("operator word must be a term, got %v", got)
	}
	for _, query := range []string{"", "   ", "\t\n", `"`, "--", "!!!"} {
		if got := searchRefs(t, st, "agent-a", query, time.Time{}, time.Time{}, 10); len(got) != 0 {
			t.Fatalf("SearchMemory(%q) = %v, want none", query, got)
		}
	}
	if _, err := st.SearchMemory(context.Background(), "agent-a", "x", nil, time.Time{}, time.Time{}, 0); err == nil {
		t.Fatal("limit 0 accepted")
	}
}

func TestSearchMemory_RanksBetterMatchAboveNewer(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "agent-a")
	base := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	strong, _ := writeNote(st, ctx, "agent-a", "token budget token budget exceeded", base)
	weak, _ := writeNote(st, ctx, "agent-a", "the cloud bill mentions token once among many other unrelated words here", base.Add(time.Hour))
	tieOlder, _ := writeNote(st, ctx, "agent-a", "bonus tip for spring", base.Add(2*time.Hour))
	tieNewer, _ := writeNote(st, ctx, "agent-a", "bonus tip for fall", base.Add(3*time.Hour))

	got := searchRefs(t, st, "agent-a", "token budget", time.Time{}, time.Time{}, 10)
	if want := []string{fmt.Sprintf("note:%d", strong), fmt.Sprintf("note:%d", weak)}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ranking = %v, want %v", got, want)
	}
	got = searchRefs(t, st, "agent-a", "bonus", time.Time{}, time.Time{}, 10)
	if want := []string{fmt.Sprintf("note:%d", tieNewer), fmt.Sprintf("note:%d", tieOlder)}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("tie order = %v, want newest first %v", got, want)
	}
	hits, _ := st.SearchMemory(ctx, "agent-a", "budget", nil, time.Time{}, time.Time{}, 10)
	if len(hits) != 1 || !strings.Contains(hits[0].Snippet, "[budget]") {
		t.Fatalf("snippet = %+v, want matched term marked", hits)
	}
}

func TestSearchMemory_ActionsFiltersAndIsolation(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "agent-a")
	insertNotesTestAgent(t, st, "agent-b")
	base := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	oldPost := insertAction(t, st, "agent-a", "post", "benchmark hype again", base)
	newReply := insertAction(t, st, "agent-a", "reply", "benchmark theatre", base.Add(48*time.Hour))
	insertAction(t, st, "agent-b", "post", "benchmark of another agent", base)
	if _, err := writeNote(st, ctx, "agent-b", "benchmark private note", base); err != nil {
		t.Fatal(err)
	}
	noteID, _ := writeNote(st, ctx, "agent-a", "benchmark note", base.Add(24*time.Hour))
	p, r, n := fmt.Sprintf("action:%d", oldPost), fmt.Sprintf("action:%d", newReply), fmt.Sprintf("note:%d", noteID)

	all := searchRefs(t, st, "agent-a", "benchmark", time.Time{}, time.Time{}, 10)
	if len(all) != 3 {
		t.Fatalf("own notes and actions = %v, want 3 and no other agent's rows", all)
	}
	if got := searchRefs(t, st, "agent-a", "benchmark", base.Add(time.Hour), time.Time{}, 10); fmt.Sprint(got) != fmt.Sprint([]string{r, n}) && fmt.Sprint(got) != fmt.Sprint([]string{n, r}) {
		t.Fatalf("since filter = %v", got)
	}
	if got := searchRefs(t, st, "agent-a", "benchmark", time.Time{}, base.Add(time.Hour), 10); fmt.Sprint(got) != fmt.Sprint([]string{p}) {
		t.Fatalf("until filter = %v", got)
	}
	if got := searchRefs(t, st, "agent-a", "benchmark", base.Add(time.Hour), base.Add(36*time.Hour), 10); fmt.Sprint(got) != fmt.Sprint([]string{n}) {
		t.Fatalf("since+until filter = %v", got)
	}
	if got := searchRefs(t, st, "agent-a", "benchmark", time.Time{}, time.Time{}, 2); len(got) != 2 {
		t.Fatalf("limit 2 = %v", got)
	}

	hits, _ := st.SearchMemory(ctx, "agent-a", "theatre", nil, time.Time{}, time.Time{}, 10)
	if len(hits) != 1 || hits[0].Kind != "reply" {
		t.Fatalf("action hit = %+v, want a reply", hits)
	}
	mem, err := st.ReadMemory(ctx, "agent-a", r)
	if err != nil || mem.Text != "benchmark theatre" || mem.Kind != "reply" {
		t.Fatalf("ReadMemory(action) = %+v, %v", mem, err)
	}
	for _, ref := range []string{"note:999", "action:0", "note", "note:x", "post:1", ""} {
		if _, err := st.ReadMemory(ctx, "agent-a", ref); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("ReadMemory(%q) = %v, want ErrNoRows", ref, err)
		}
	}
	if _, err := st.ReadMemory(ctx, "agent-b", p); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("ReadMemory of another agent's action = %v, want ErrNoRows", err)
	}
	if _, err := st.ReadMemory(ctx, "agent-b", n); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("ReadMemory of another agent's note = %v, want ErrNoRows", err)
	}
}

func TestSearchMemory_Vietnamese_KeepsToneMarksApart(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "agent-a")
	at := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	code, _ := writeNote(st, ctx, "agent-a", "dọn mã nguồn sau demo", at)
	writeNote(st, ctx, "agent-a", "con ma hiện hồn lúc 2h sáng", at.Add(time.Minute))
	if got := searchRefs(t, st, "agent-a", "mã", time.Time{}, time.Time{}, 10); fmt.Sprint(got) != fmt.Sprint([]string{fmt.Sprintf("note:%d", code)}) {
		t.Fatalf("mã matched %v, want only the exact tone", got)
	}
}

func TestSearchMemory_TracksFTSUpdateTrigger(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "agent-a")
	id, _ := writeNote(st, ctx, "agent-a", "original wanderlust signal", time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
	actionID := insertAction(t, st, "agent-a", "post", "original wanderlust post", time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
	st.DB().ExecContext(ctx, `UPDATE notes SET text = ? WHERE id = ?`, "replacement harbor signal", id)
	st.DB().ExecContext(ctx, `UPDATE actions SET text = ? WHERE id = ?`, "replacement harbor post", actionID)
	if got := searchRefs(t, st, "agent-a", "wanderlust", time.Time{}, time.Time{}, 10); len(got) != 0 {
		t.Fatalf("old term still indexed: %v", got)
	}
	if got := searchRefs(t, st, "agent-a", "harbor", time.Time{}, time.Time{}, 10); len(got) != 2 {
		t.Fatalf("new term = %v, want the updated note and action", got)
	}
	st.DB().ExecContext(ctx, `DELETE FROM notes WHERE id = ?`, id)
	st.DB().ExecContext(ctx, `DELETE FROM actions WHERE id = ?`, actionID)
	if got := searchRefs(t, st, "agent-a", "harbor", time.Time{}, time.Time{}, 10); len(got) != 0 {
		t.Fatalf("deleted rows still indexed: %v", got)
	}
}
func TestAgentNotes_RejectsCancelledContextAndClosedDB(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "agent-a")

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	at := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	if _, err := writeNote(st, cancelled, "agent-a", "no write", at); !errors.Is(err, context.Canceled) {
		t.Fatalf("WriteTopic with cancelled context error = %v, want context.Canceled", err)
	}
	if _, err := st.SearchMemory(cancelled, "agent-a", "write", nil, time.Time{}, time.Time{}, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("SearchMemory with cancelled context error = %v, want context.Canceled", err)
	}
	if got := countRows(t, st, `SELECT COUNT(*) FROM notes`); got != 0 {
		t.Fatalf("notes rows = %d, want 0 after cancelled write", got)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("closing store: %v", err)
	}
	if _, err := writeNote(st, ctx, "agent-a", "closed", at); err == nil {
		t.Fatal("WriteTopic on closed store succeeded, want an error")
	}
	if _, err := st.SearchMemory(ctx, "agent-a", "closed", nil, time.Time{}, time.Time{}, 10); err == nil {
		t.Fatal("SearchMemory on closed store succeeded, want an error")
	}
}
