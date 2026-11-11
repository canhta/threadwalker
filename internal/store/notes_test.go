package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/store"
)

func openNotesTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "threadwalker.db"))
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
		INSERT INTO agents (id, status, next_wake_at, self_note)
		VALUES (?, 'running', NULL, '')
	`, agentID)
	if err != nil {
		t.Fatalf("inserting agent %q: %v", agentID, err)
	}
}

func countRows(t *testing.T, st *store.Store, query string, args ...any) int {
	t.Helper()
	var count int
	if err := st.DB().QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("counting rows with %q: %v", query, err)
	}
	return count
}

func TestWriteAgentNote_InsertsNoteUpdatesSelfNoteAndFTS(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "agent-a")

	at := time.Date(2026, 11, 11, 9, 30, 15, 123456789, time.UTC)
	text := "Vietnam builders prefer practical examples"

	id, err := st.WriteAgentNote(ctx, "agent-a", text, at)
	if err != nil {
		t.Fatalf("WriteAgentNote: %v", err)
	}
	if id <= 0 {
		t.Fatalf("WriteAgentNote returned id %d, want a positive id", id)
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

	var selfNote string
	if err := st.DB().QueryRowContext(ctx,
		`SELECT self_note FROM agents WHERE id = ?`, "agent-a").Scan(&selfNote); err != nil {
		t.Fatalf("reading self_note: %v", err)
	}
	if selfNote != text {
		t.Fatalf("self_note = %q, want %q", selfNote, text)
	}

	notes, err := st.SearchAgentNotes(ctx, "agent-a", "Vietnam", 10)
	if err != nil {
		t.Fatalf("SearchAgentNotes: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("SearchAgentNotes returned %d notes, want 1: %+v", len(notes), notes)
	}
	got := notes[0]
	if got.ID != id || got.AgentID != "agent-a" || got.Text != text {
		t.Fatalf("searched note = %+v, want id %d agent-a text %q", got, id, text)
	}
	if !got.CreatedAt.Equal(at) {
		t.Fatalf("searched created_at = %v, want %v", got.CreatedAt, at)
	}
}

func TestWriteAgentNote_RequiresExistingAgent(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)

	_, err := st.WriteAgentNote(ctx, "missing-agent", "a note", time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("WriteAgentNote for missing agent error = %v, want sql.ErrNoRows", err)
	}
	if got := countRows(t, st, `SELECT COUNT(*) FROM notes`); got != 0 {
		t.Fatalf("notes rows = %d, want 0 after failed write", got)
	}
	if got := countRows(t, st, `SELECT COUNT(*) FROM notes_fts WHERE notes_fts MATCH ?`, "note"); got != 0 {
		t.Fatalf("FTS rows = %d, want 0 after failed write", got)
	}
}

func TestWriteAgentNote_RollsBackNoteAndFTSWhenSelfNoteUpdateFails(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "agent-a")
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE agents SET self_note = ? WHERE id = ?`, "keep this note", "agent-a"); err != nil {
		t.Fatalf("setting original self_note: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx, `
		CREATE TRIGGER fail_self_note_update
		BEFORE UPDATE OF self_note ON agents
		BEGIN
			SELECT RAISE(ABORT, 'forced self_note update failure');
		END
	`); err != nil {
		t.Fatalf("creating failure trigger: %v", err)
	}

	_, err := st.WriteAgentNote(ctx, "agent-a", "rollback signal", time.Date(2026, 11, 11, 1, 2, 3, 0, time.UTC))
	if err == nil {
		t.Fatal("WriteAgentNote succeeded, want the forced self_note failure")
	}
	if got := countRows(t, st, `SELECT COUNT(*) FROM notes`); got != 0 {
		t.Fatalf("notes rows = %d, want 0 after rollback", got)
	}
	if got := countRows(t, st, `SELECT COUNT(*) FROM notes_fts WHERE notes_fts MATCH ?`, "rollback"); got != 0 {
		t.Fatalf("FTS rows = %d, want 0 after rollback", got)
	}
	var selfNote string
	if err := st.DB().QueryRowContext(ctx,
		`SELECT self_note FROM agents WHERE id = ?`, "agent-a").Scan(&selfNote); err != nil {
		t.Fatalf("reading self_note after rollback: %v", err)
	}
	if selfNote != "keep this note" {
		t.Fatalf("self_note = %q, want the original value preserved", selfNote)
	}
}

func TestSearchAgentNotes_IsolatesAgentsAndOrdersNewestFirst(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "agent-a")
	insertNotesTestAgent(t, st, "agent-b")

	base := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	write := func(agentID, text string, at time.Time) int64 {
		t.Helper()
		id, err := st.WriteAgentNote(ctx, agentID, text, at)
		if err != nil {
			t.Fatalf("WriteAgentNote(%s, %q): %v", agentID, text, err)
		}
		return id
	}

	first := write("agent-a", "shared signal first", base)
	write("agent-b", "shared signal from another agent", base.Add(time.Minute))
	newer := write("agent-a", "shared signal newer", base.Add(2*time.Minute))
	tieOlder := write("agent-a", "shared signal tie older", base.Add(3*time.Minute))
	tieNewer := write("agent-a", "shared signal tie newer", base.Add(3*time.Minute))

	notes, err := st.SearchAgentNotes(ctx, "agent-a", "shared", 10)
	if err != nil {
		t.Fatalf("SearchAgentNotes: %v", err)
	}
	wantIDs := []int64{tieNewer, tieOlder, newer, first}
	if len(notes) != len(wantIDs) {
		t.Fatalf("SearchAgentNotes returned %d notes, want %d: %+v", len(notes), len(wantIDs), notes)
	}
	for i, wantID := range wantIDs {
		if notes[i].ID != wantID {
			t.Fatalf("notes[%d].ID = %d, want %d (all: %+v)", i, notes[i].ID, wantID, notes)
		}
		if notes[i].AgentID != "agent-a" {
			t.Fatalf("notes[%d].AgentID = %q, want agent-a", i, notes[i].AgentID)
		}
	}
}

func TestSearchAgentNotes_AppliesBoundedPositiveLimit(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "agent-a")

	base := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	var wroteIDs []int64
	for i := 0; i < 105; i++ {
		id, err := st.WriteAgentNote(ctx, "agent-a", fmt.Sprintf("signal number %d", i), base.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatalf("WriteAgentNote %d: %v", i, err)
		}
		wroteIDs = append(wroteIDs, id)
	}

	notes, err := st.SearchAgentNotes(ctx, "agent-a", "signal", 2)
	if err != nil {
		t.Fatalf("SearchAgentNotes(limit 2): %v", err)
	}
	if len(notes) != 2 {
		t.Fatalf("SearchAgentNotes(limit 2) returned %d notes, want 2", len(notes))
	}
	if notes[0].ID != wroteIDs[104] || notes[1].ID != wroteIDs[103] {
		t.Fatalf("limit 2 returned ids %d,%d, want %d,%d", notes[0].ID, notes[1].ID, wroteIDs[104], wroteIDs[103])
	}

	capped, err := st.SearchAgentNotes(ctx, "agent-a", "signal", 1000)
	if err != nil {
		t.Fatalf("SearchAgentNotes(limit 1000): %v", err)
	}
	if len(capped) != 100 {
		t.Fatalf("SearchAgentNotes(limit 1000) returned %d notes, want the 100-note cap", len(capped))
	}

	for _, limit := range []int{0, -1} {
		if _, err := st.SearchAgentNotes(ctx, "agent-a", "signal", limit); err == nil {
			t.Fatalf("SearchAgentNotes(limit %d) succeeded, want an error", limit)
		}
	}
}

func TestSearchAgentNotes_EmptyMalformedAndUnknownAgent(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "agent-a")
	if _, err := st.WriteAgentNote(ctx, "agent-a", "a real signal", time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("WriteAgentNote: %v", err)
	}

	for _, query := range []string{"", "   ", "\t\n"} {
		notes, err := st.SearchAgentNotes(ctx, "agent-a", query, 10)
		if err != nil {
			t.Fatalf("SearchAgentNotes(%q) error: %v", query, err)
		}
		if len(notes) != 0 {
			t.Fatalf("SearchAgentNotes(%q) returned %d notes, want 0", query, len(notes))
		}
	}

	if _, err := st.SearchAgentNotes(ctx, "agent-a", `"unterminated`, 10); err == nil {
		t.Fatal("SearchAgentNotes with malformed FTS syntax succeeded, want an error")
	}

	notes, err := st.SearchAgentNotes(ctx, "missing-agent", "signal", 10)
	if err != nil {
		t.Fatalf("SearchAgentNotes for missing agent error: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("SearchAgentNotes for missing agent returned %d notes, want 0", len(notes))
	}
}

func TestSearchAgentNotes_TracksFTSUpdateTrigger(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "agent-a")

	id, err := st.WriteAgentNote(ctx, "agent-a", "original wanderlust signal", time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("WriteAgentNote: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE notes SET text = ? WHERE id = ?`, "replacement harbor signal", id); err != nil {
		t.Fatalf("updating note text: %v", err)
	}

	oldTerm, err := st.SearchAgentNotes(ctx, "agent-a", "wanderlust", 10)
	if err != nil {
		t.Fatalf("SearchAgentNotes(old term): %v", err)
	}
	if len(oldTerm) != 0 {
		t.Fatalf("SearchAgentNotes(old term) returned %d notes, want 0 after update", len(oldTerm))
	}
	newTerm, err := st.SearchAgentNotes(ctx, "agent-a", "harbor", 10)
	if err != nil {
		t.Fatalf("SearchAgentNotes(new term): %v", err)
	}
	if len(newTerm) != 1 || newTerm[0].ID != id || newTerm[0].Text != "replacement harbor signal" {
		t.Fatalf("SearchAgentNotes(new term) = %+v, want the updated note %d", newTerm, id)
	}
}

func TestAgentNotes_RejectsCancelledContextAndClosedDB(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "agent-a")

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	at := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	if _, err := st.WriteAgentNote(cancelled, "agent-a", "no write", at); !errors.Is(err, context.Canceled) {
		t.Fatalf("WriteAgentNote with cancelled context error = %v, want context.Canceled", err)
	}
	if _, err := st.SearchAgentNotes(cancelled, "agent-a", "write", 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("SearchAgentNotes with cancelled context error = %v, want context.Canceled", err)
	}
	if got := countRows(t, st, `SELECT COUNT(*) FROM notes`); got != 0 {
		t.Fatalf("notes rows = %d, want 0 after cancelled write", got)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("closing store: %v", err)
	}
	if _, err := st.WriteAgentNote(ctx, "agent-a", "closed", at); err == nil {
		t.Fatal("WriteAgentNote on closed store succeeded, want an error")
	}
	if _, err := st.SearchAgentNotes(ctx, "agent-a", "closed", 10); err == nil {
		t.Fatal("SearchAgentNotes on closed store succeeded, want an error")
	}
}
