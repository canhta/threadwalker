package store_test

// Independent acceptance suite for the #10 playbook store contract. It uses
// only the public store API and the published SQLite schema; it never imports
// the implementation package internals. The suite is expected to fail to
// compile until the CRUD owner's immutable playbook commit is applied.

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/store"
)

var playbookAcceptanceAt = time.Date(2026, 5, 4, 10, 30, 15, 123456789, time.UTC)

func playbookAcceptanceStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "playbook-acceptance.db"))
	if err != nil {
		t.Fatalf("opening playbook acceptance store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing playbook acceptance store: %v", err)
		}
	})
	return st
}

func playbookAcceptanceAgent(t *testing.T, st *store.Store, agentID string) {
	t.Helper()
	if err := st.EnsureAgent(context.Background(), agentID); err != nil {
		t.Fatalf("creating agent %q: %v", agentID, err)
	}
}

func playbookAcceptanceInitialize(t *testing.T, st *store.Store, agentID string, texts []string, at time.Time) bool {
	t.Helper()
	created, err := st.InitializePlaybook(context.Background(), agentID, texts, at)
	if err != nil {
		t.Fatalf("initializing playbook for %q: %v", agentID, err)
	}
	return created
}

func playbookAcceptanceEntries(t *testing.T, st *store.Store, agentID string, limit int) []store.PlaybookEntry {
	t.Helper()
	entries, err := st.PlaybookEntries(context.Background(), agentID, limit)
	if err != nil {
		t.Fatalf("reading playbook entries for %q: %v", agentID, err)
	}
	if entries == nil {
		t.Fatalf("playbook entries for %q are nil, want a nonnil slice", agentID)
	}
	return entries
}

func playbookAcceptanceAdd(t *testing.T, st *store.Store, agentID, text string, at time.Time) store.PlaybookEntry {
	t.Helper()
	entry, err := st.ProposePlaybook(context.Background(), agentID, 0, text, at)
	if err != nil {
		t.Fatalf("adding playbook entry for %q: %v", agentID, err)
	}
	return entry
}

func playbookAcceptanceSetState(t *testing.T, st *store.Store, id int64, status string, n, successes int64) {
	t.Helper()
	var decided any
	if status != "testing" {
		decided = playbookAcceptanceAt.Add(30 * time.Hour).Format(time.RFC3339Nano)
	}
	if _, err := st.DB().Exec(`UPDATE playbook SET status=?,n=?,successes=?,decided_at=? WHERE id=?`, status, n, successes, decided, id); err != nil {
		t.Fatalf("setting playbook entry %d state: %v", id, err)
	}
}

func playbookAcceptanceStatus(t *testing.T, st *store.Store, id int64) string {
	t.Helper()
	var status string
	if err := st.DB().QueryRow(`SELECT status FROM playbook WHERE id=?`, id).Scan(&status); err != nil {
		t.Fatalf("reading playbook entry %d status: %v", id, err)
	}
	return status
}

func playbookAcceptanceCount(t *testing.T, st *store.Store, agentID string) int {
	t.Helper()
	var count int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM playbook WHERE agent_id=?`, agentID).Scan(&count); err != nil {
		t.Fatalf("counting playbook entries for %q: %v", agentID, err)
	}
	return count
}

func playbookAcceptanceInitializationCount(t *testing.T, st *store.Store, agentID string) int {
	t.Helper()
	var count int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM playbook_initializations WHERE agent_id=?`, agentID).Scan(&count); err != nil {
		t.Fatalf("counting playbook initialization marker for %q: %v", agentID, err)
	}
	return count
}

func TestPlaybookInitializeSeedsOncePerAgentAndPersistsMarker(t *testing.T) {
	ctx := context.Background()
	st := playbookAcceptanceStore(t)
	playbookAcceptanceAgent(t, st, "acceptance")

	created, err := st.InitializePlaybook(ctx, "acceptance", []string{"first hypothesis", " second hypothesis "}, playbookAcceptanceAt)
	if err != nil || !created {
		t.Fatalf("first initialization created=%v err=%v", created, err)
	}
	entries := playbookAcceptanceEntries(t, st, "acceptance", 100)
	if len(entries) != 2 {
		t.Fatalf("seeded entries = %d, want 2", len(entries))
	}
	for i, entry := range entries {
		if entry.AgentID != "acceptance" || entry.Status != "testing" || entry.Revision != 1 || entry.N != 0 || entry.Successes != 0 {
			t.Fatalf("seeded entry %d = %+v", i, entry)
		}
		if !entry.CreatedAt.Equal(playbookAcceptanceAt) || entry.DecidedAt != nil {
			t.Fatalf("seeded entry %d times = %+v", i, entry)
		}
		if strings.TrimSpace(entry.Text) == "" {
			t.Fatalf("seeded entry %d text is blank: %q", i, entry.Text)
		}
	}
	firstIDs := []int64{entries[0].ID, entries[1].ID}
	firstTexts := []string{entries[0].Text, entries[1].Text}

	// A later initialization with edited text must not reseed, renumber or
	// rewrite existing rows, and must report false.
	created = playbookAcceptanceInitialize(t, st, "acceptance", []string{"replacement one", "replacement two", "extra"}, playbookAcceptanceAt.Add(time.Hour))
	if created {
		t.Fatal("second initialization reported a fresh seed")
	}
	entries = playbookAcceptanceEntries(t, st, "acceptance", 100)
	if len(entries) != 2 || entries[0].ID != firstIDs[0] || entries[1].ID != firstIDs[1] || entries[0].Text != firstTexts[0] || entries[1].Text != firstTexts[1] {
		t.Fatalf("second initialization modified entries: %+v", entries)
	}

	if count := playbookAcceptanceInitializationCount(t, st, "acceptance"); count != 1 {
		t.Fatalf("initialization markers = %d, want 1", count)
	}
	var initializedAt string
	if err := st.DB().QueryRow(`SELECT initialized_at FROM playbook_initializations WHERE agent_id=?`, "acceptance").Scan(&initializedAt); err != nil {
		t.Fatalf("reading initialization timestamp: %v", err)
	}
	stamp, err := time.Parse(time.RFC3339Nano, initializedAt)
	if err != nil || !stamp.Equal(playbookAcceptanceAt) {
		t.Fatalf("initialized_at = %q err=%v", initializedAt, err)
	}

	// A decided entry survives a subsequent initialization untouched.
	playbookAcceptanceSetState(t, st, firstIDs[0], "established", 12, 9)
	if created := playbookAcceptanceInitialize(t, st, "acceptance", []string{"again"}, playbookAcceptanceAt.Add(2*time.Hour)); created {
		t.Fatal("third initialization reported a fresh seed")
	}
	entries = playbookAcceptanceEntries(t, st, "acceptance", 100)
	var decidedEntry *store.PlaybookEntry
	for i := range entries {
		if entries[i].ID == firstIDs[0] {
			decidedEntry = &entries[i]
		}
	}
	if len(entries) != 2 || decidedEntry == nil || decidedEntry.Status != "established" || decidedEntry.N != 12 || decidedEntry.Successes != 9 {
		t.Fatalf("decided entry changed after initialization: %+v", entries)
	}

	// Closing and reopening the same database must not reinsert entries.
	path := filepath.Join(t.TempDir(), "reopen.db")
	reopened, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	playbookAcceptanceAgent(t, reopened, "acceptance")
	if created := playbookAcceptanceInitialize(t, reopened, "acceptance", []string{"a", "b"}, playbookAcceptanceAt); !created {
		t.Fatal("fresh database initialization reported false")
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err = store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if created := playbookAcceptanceInitialize(t, reopened, "acceptance", []string{"new text"}, playbookAcceptanceAt.Add(time.Hour)); created {
		t.Fatal("reopened database reseeded the playbook")
	}
	if got := len(playbookAcceptanceEntries(t, reopened, "acceptance", 100)); got != 2 {
		t.Fatalf("entries after reopen = %d, want 2", got)
	}
}

func TestPlaybookInitializeAcceptsEmptyListAndBounds(t *testing.T) {
	st := playbookAcceptanceStore(t)
	playbookAcceptanceAgent(t, st, "empty")

	if !playbookAcceptanceInitialize(t, st, "empty", nil, playbookAcceptanceAt) {
		t.Fatal("empty initialization reported false")
	}
	if entries := playbookAcceptanceEntries(t, st, "empty", 100); len(entries) != 0 {
		t.Fatalf("empty initialization created %d entries", len(entries))
	}
	if playbookAcceptanceInitialize(t, st, "empty", []string{"late"}, playbookAcceptanceAt.Add(time.Hour)) {
		t.Fatal("empty agent reseeded after initialization")
	}
	if entries := playbookAcceptanceEntries(t, st, "empty", 100); len(entries) != 0 {
		t.Fatalf("late texts were inserted: %+v", entries)
	}

	unique := []string{"one", "two", "three", "four", "five"}
	playbookAcceptanceAgent(t, st, "cap")
	if !playbookAcceptanceInitialize(t, st, "cap", unique, playbookAcceptanceAt) {
		t.Fatal("five-entry initialization reported false")
	}
	if created, err := st.InitializePlaybook(context.Background(), "cap2", []string{"one", "two", "three", "four", "five", "six"}, playbookAcceptanceAt); !errors.Is(err, store.ErrPlaybookCap) || created {
		t.Fatalf("six-entry initialization created=%v err=%v, want ErrPlaybookCap", created, err)
	}
	if count := playbookAcceptanceCount(t, st, "cap2"); count != 0 {
		t.Fatalf("rejected initialization inserted %d entries", count)
	}

	// Unicode code points, not bytes, define the ceiling.
	atLimit := strings.Repeat("界", store.MaxHypothesisTextRunes)
	overLimit := atLimit + "界"
	playbookAcceptanceAgent(t, st, "runic")
	entry := playbookAcceptanceAdd(t, st, "runic", atLimit, playbookAcceptanceAt)
	if len([]rune(entry.Text)) != store.MaxHypothesisTextRunes {
		t.Fatalf("limit text runes = %d", len([]rune(entry.Text)))
	}
	if _, err := st.ProposePlaybook(context.Background(), "runic", 0, overLimit, playbookAcceptanceAt); !errors.Is(err, store.ErrPlaybookInput) {
		t.Fatalf("over-limit text error = %v, want ErrPlaybookInput", err)
	}
	if _, err := st.InitializePlaybook(context.Background(), "cap2", []string{overLimit}, playbookAcceptanceAt); !errors.Is(err, store.ErrPlaybookInput) {
		t.Fatalf("over-limit initialization error = %v, want ErrPlaybookInput", err)
	}
}

func TestPlaybookInitializeValidatesAgentTimeAndText(t *testing.T) {
	ctx := context.Background()
	st := playbookAcceptanceStore(t)
	playbookAcceptanceAgent(t, st, "known")

	for name, call := range map[string]func() (bool, error){
		"unknown_agent": func() (bool, error) {
			return st.InitializePlaybook(ctx, "missing", []string{"text"}, playbookAcceptanceAt)
		},
		"blank_agent": func() (bool, error) {
			return st.InitializePlaybook(ctx, "  ", []string{"text"}, playbookAcceptanceAt)
		},
		"zero_time": func() (bool, error) {
			return st.InitializePlaybook(ctx, "known", []string{"text"}, time.Time{})
		},
		"blank_text": func() (bool, error) {
			return st.InitializePlaybook(ctx, "known", []string{"ok", "   "}, playbookAcceptanceAt)
		},
	} {
		t.Run(name, func(t *testing.T) {
			created, err := call()
			if err == nil || created {
				t.Fatalf("invalid initialization created=%v err=%v", created, err)
			}
			if name != "blank_agent" && name != "unknown_agent" && playbookAcceptanceCount(t, st, "known") != 0 {
				t.Fatal("failed initialization inserted rows")
			}
		})
	}

	for _, status := range []string{"paused", "killed"} {
		playbookAcceptanceAgent(t, st, "stopped-"+status)
		if _, err := st.DB().Exec(`UPDATE agents SET status=? WHERE id=?`, status, "stopped-"+status); err != nil {
			t.Fatal(err)
		}
		created, err := st.InitializePlaybook(ctx, "stopped-"+status, []string{"text"}, playbookAcceptanceAt)
		if err == nil || created {
			t.Fatalf("%s agent initialized created=%v err=%v", status, created, err)
		}
		if count := playbookAcceptanceCount(t, st, "stopped-"+status); count != 0 {
			t.Fatalf("%s agent inserted %d entries", status, count)
		}
		if count := playbookAcceptanceInitializationCount(t, st, "stopped-"+status); count != 0 {
			t.Fatalf("%s agent marked initialized", status)
		}
	}
}

func TestPlaybookInitializeMarksPreexistingEntriesWithoutReseeding(t *testing.T) {
	ctx := context.Background()
	st := playbookAcceptanceStore(t)
	playbookAcceptanceAgent(t, st, "legacy")
	if _, err := st.DB().Exec(`INSERT INTO playbook(agent_id,text,status,revision,n,successes,created_at) VALUES('legacy','manual runtime state','testing',1,7,4,?)`, playbookAcceptanceAt.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("inserting preexisting entry: %v", err)
	}

	created, err := st.InitializePlaybook(ctx, "legacy", []string{"seed one", "seed two"}, playbookAcceptanceAt.Add(time.Hour))
	if err != nil || !created {
		t.Fatalf("preexisting initialization created=%v err=%v", created, err)
	}
	entries := playbookAcceptanceEntries(t, st, "legacy", 100)
	if len(entries) != 1 || entries[0].Text != "manual runtime state" || entries[0].N != 7 || entries[0].Successes != 4 {
		t.Fatalf("preexisting entries were reseeded or rewritten: %+v", entries)
	}
	if count := playbookAcceptanceInitializationCount(t, st, "legacy"); count != 1 {
		t.Fatalf("preexisting initialization markers = %d, want 1", count)
	}
	if created := playbookAcceptanceInitialize(t, st, "legacy", []string{"later"}, playbookAcceptanceAt.Add(2*time.Hour)); created {
		t.Fatal("preexisting agent reseeded on second call")
	}
}

func TestPlaybookInitializationMarkerSurvivesDeletedRows(t *testing.T) {
	st := playbookAcceptanceStore(t)
	playbookAcceptanceAgent(t, st, "deleted")
	if !playbookAcceptanceInitialize(t, st, "deleted", []string{"one", "two"}, playbookAcceptanceAt) {
		t.Fatal("first initialization reported false")
	}
	if _, err := st.DB().Exec(`DELETE FROM playbook WHERE agent_id='deleted'`); err != nil {
		t.Fatal(err)
	}
	if created := playbookAcceptanceInitialize(t, st, "deleted", []string{"resurrected"}, playbookAcceptanceAt.Add(time.Hour)); created {
		t.Fatal("initialization reseeded after rows were deleted")
	}
	if entries := playbookAcceptanceEntries(t, st, "deleted", 100); len(entries) != 0 {
		t.Fatalf("deleted playbook was repopulated: %+v", entries)
	}
	if count := playbookAcceptanceInitializationCount(t, st, "deleted"); count != 1 {
		t.Fatalf("marker count = %d, want 1", count)
	}
}

func TestPlaybookEntriesScopeOrderingAndLimits(t *testing.T) {
	ctx := context.Background()
	st := playbookAcceptanceStore(t)
	playbookAcceptanceAgent(t, st, "own")
	playbookAcceptanceAgent(t, st, "other")

	own := []store.PlaybookEntry{
		playbookAcceptanceAdd(t, st, "own", "own one", playbookAcceptanceAt),
		playbookAcceptanceAdd(t, st, "own", "own two", playbookAcceptanceAt.Add(time.Minute)),
		playbookAcceptanceAdd(t, st, "own", "own three", playbookAcceptanceAt.Add(2*time.Minute)),
		playbookAcceptanceAdd(t, st, "own", "own four", playbookAcceptanceAt.Add(3*time.Minute)),
	}
	foreign := playbookAcceptanceAdd(t, st, "other", "foreign entry", playbookAcceptanceAt)
	playbookAcceptanceSetState(t, st, own[1].ID, "established", 5, 3)
	playbookAcceptanceSetState(t, st, own[2].ID, "rejected", 4, 1)
	playbookAcceptanceSetState(t, st, own[3].ID, "established", 6, 4)

	entries := playbookAcceptanceEntries(t, st, "own", 100)
	if len(entries) != 4 {
		t.Fatalf("own entries = %d, want 4", len(entries))
	}
	wantOrder := []int64{own[0].ID, own[1].ID, own[3].ID, own[2].ID}
	for i, want := range wantOrder {
		if entries[i].ID != want {
			t.Fatalf("entry order[%d] = %d, want %d (all=%+v)", i, entries[i].ID, want, entries)
		}
	}
	for _, entry := range entries {
		if entry.AgentID != "own" || entry.ID == foreign.ID {
			t.Fatalf("foreign entry leaked: %+v", entry)
		}
	}
	if limited := playbookAcceptanceEntries(t, st, "own", 2); len(limited) != 2 || limited[0].ID != wantOrder[0] || limited[1].ID != wantOrder[1] {
		t.Fatalf("limit 2 entries = %+v", limited)
	}
	if limited := playbookAcceptanceEntries(t, st, "own", 1); len(limited) != 1 || limited[0].ID != wantOrder[0] {
		t.Fatalf("limit 1 entries = %+v", limited)
	}

	playbookAcceptanceAgent(t, st, "known-empty")
	empty := playbookAcceptanceEntries(t, st, "known-empty", 100)
	if len(empty) != 0 {
		t.Fatalf("known empty agent returned %+v", empty)
	}

	for name, call := range map[string]func() ([]store.PlaybookEntry, error){
		"zero_limit":    func() ([]store.PlaybookEntry, error) { return st.PlaybookEntries(ctx, "own", 0) },
		"over_limit":    func() ([]store.PlaybookEntry, error) { return st.PlaybookEntries(ctx, "own", 101) },
		"negative":      func() ([]store.PlaybookEntry, error) { return st.PlaybookEntries(ctx, "own", -1) },
		"blank_agent":   func() ([]store.PlaybookEntry, error) { return st.PlaybookEntries(ctx, "", 10) },
		"unknown_agent": func() ([]store.PlaybookEntry, error) { return st.PlaybookEntries(ctx, "nobody", 10) },
	} {
		t.Run(name, func(t *testing.T) {
			entries, err := call()
			if !errors.Is(err, store.ErrPlaybookInput) || entries != nil {
				t.Fatalf("invalid entries call = %+v err=%v, want ErrPlaybookInput", entries, err)
			}
		})
	}
}

func TestPlaybookEntriesFailSafelyOnMalformedRows(t *testing.T) {
	ctx := context.Background()
	st := playbookAcceptanceStore(t)
	playbookAcceptanceAgent(t, st, "broken")
	if _, err := st.DB().Exec(`INSERT INTO playbook(agent_id,text,status,revision,n,successes,created_at) VALUES('broken','bad time','testing',1,0,0,'not-a-timestamp')`); err != nil {
		t.Fatalf("inserting malformed entry: %v", err)
	}
	entries, err := st.PlaybookEntries(ctx, "broken", 10)
	if err == nil {
		t.Fatalf("malformed row returned entries=%+v without error", entries)
	}
	if entries != nil {
		t.Fatalf("malformed row returned partial entries=%+v", entries)
	}

	// Impossible counter states are not valid persisted state either.
	for name, counters := range map[string]string{
		"negative_n":          "-1,0",
		"successes_over_n":    "3,4",
		"terminal_no_decided": "0,0",
	} {
		channel := "broken-" + name
		playbookAcceptanceAgent(t, st, channel)
		status, revision := "testing", "1"
		if name == "terminal_no_decided" {
			status = "established"
		}
		query := `INSERT INTO playbook(agent_id,text,status,revision,n,successes,created_at) VALUES(?,?,'` + status + `',` + revision + `,` + counters + `,?)`
		if _, err := st.DB().Exec(query, channel, name, playbookAcceptanceAt.Format(time.RFC3339Nano)); err != nil {
			t.Fatalf("inserting %s row: %v", name, err)
		}
		if entries, err := st.PlaybookEntries(ctx, channel, 10); err == nil {
			t.Fatalf("%s row was accepted: %+v", name, entries)
		}
	}
}

func TestPlaybookProposeAddsTrimsAndResetsOwnTestingEntry(t *testing.T) {
	ctx := context.Background()
	st := playbookAcceptanceStore(t)
	playbookAcceptanceAgent(t, st, "proposer")

	entry, err := st.ProposePlaybook(ctx, "proposer", 0, "  short form gets more replies  ", playbookAcceptanceAt)
	if err != nil {
		t.Fatalf("adding entry: %v", err)
	}
	if entry.ID <= 0 || entry.AgentID != "proposer" || entry.Text != "short form gets more replies" || entry.Status != "testing" || entry.Revision != 1 || entry.N != 0 || entry.Successes != 0 || !entry.CreatedAt.Equal(playbookAcceptanceAt) || entry.DecidedAt != nil {
		t.Fatalf("added entry = %+v", entry)
	}

	// Evidence owned by code is reset only when the text genuinely changes.
	if _, err := st.DB().Exec(`UPDATE playbook SET n=8,successes=5 WHERE id=?`, entry.ID); err != nil {
		t.Fatal(err)
	}
	same, err := st.ProposePlaybook(ctx, "proposer", entry.ID, "  short form gets more replies  ", playbookAcceptanceAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("identical amendment: %v", err)
	}
	if same.ID != entry.ID || same.Text != entry.Text || same.Revision != 1 || same.N != 8 || same.Successes != 5 || !same.CreatedAt.Equal(entry.CreatedAt) {
		t.Fatalf("identical amendment changed state: %+v", same)
	}

	amended, err := st.ProposePlaybook(ctx, "proposer", entry.ID, "questions in the first line get more replies", playbookAcceptanceAt.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("amending entry: %v", err)
	}
	if amended.ID != entry.ID || amended.Text != "questions in the first line get more replies" || amended.Status != "testing" || amended.Revision != 2 || amended.N != 0 || amended.Successes != 0 || !amended.CreatedAt.Equal(entry.CreatedAt) || amended.DecidedAt != nil {
		t.Fatalf("amended entry = %+v", amended)
	}

	stored := playbookAcceptanceEntries(t, st, "proposer", 100)
	if len(stored) != 1 || stored[0] != amended {
		t.Fatalf("stored amended entry = %+v, want %+v", stored, amended)
	}

	// Trimmed-identical text after trimming is still identical, even when the
	// caller adds surrounding whitespace.
	if _, err := st.DB().Exec(`UPDATE playbook SET n=2,successes=1 WHERE id=?`, entry.ID); err != nil {
		t.Fatal(err)
	}
	again, err := st.ProposePlaybook(ctx, "proposer", entry.ID, " questions in the first line get more replies ", playbookAcceptanceAt.Add(3*time.Hour))
	if err != nil {
		t.Fatalf("trim-identical amendment: %v", err)
	}
	if again.Revision != 2 || again.N != 2 || again.Successes != 1 {
		t.Fatalf("trim-identical amendment = %+v", again)
	}

	for name, text := range map[string]string{
		"blank":     "   ",
		"too_long":  strings.Repeat("x", store.MaxHypothesisTextRunes+1),
		"zero_time": "valid text",
	} {
		t.Run(name, func(t *testing.T) {
			at := playbookAcceptanceAt
			if name == "zero_time" {
				at = time.Time{}
			}
			if _, err := st.ProposePlaybook(ctx, "proposer", 0, text, at); !errors.Is(err, store.ErrPlaybookInput) {
				t.Fatalf("invalid add error = %v, want ErrPlaybookInput", err)
			}
		})
	}
	if count := playbookAcceptanceCount(t, st, "proposer"); count != 1 {
		t.Fatalf("invalid proposals inserted entries: %d", count)
	}
}

func TestPlaybookProposeTestingCapCountsOnlyTestingEntries(t *testing.T) {
	ctx := context.Background()
	st := playbookAcceptanceStore(t)
	playbookAcceptanceAgent(t, st, "capped")

	entries := make([]store.PlaybookEntry, 0, store.MaxTestingHypotheses)
	for i := 0; i < store.MaxTestingHypotheses; i++ {
		entries = append(entries, playbookAcceptanceAdd(t, st, "capped", "hypothesis "+string(rune('a'+i)), playbookAcceptanceAt.Add(time.Duration(i)*time.Minute)))
	}
	if _, err := st.ProposePlaybook(ctx, "capped", 0, "one too many", playbookAcceptanceAt.Add(time.Hour)); !errors.Is(err, store.ErrPlaybookCap) {
		t.Fatalf("sixth testing entry error = %v, want ErrPlaybookCap", err)
	}
	if count := playbookAcceptanceCount(t, st, "capped"); count != store.MaxTestingHypotheses {
		t.Fatalf("capped entries = %d, want %d", count, store.MaxTestingHypotheses)
	}

	// Decided entries do not consume the testing cap.
	playbookAcceptanceSetState(t, st, entries[0].ID, "rejected", 20, 5)
	replacement := playbookAcceptanceAdd(t, st, "capped", "replacement hypothesis", playbookAcceptanceAt.Add(2*time.Hour))
	if replacement.Status != "testing" || replacement.Revision != 1 {
		t.Fatalf("replacement entry = %+v", replacement)
	}
	if count := playbookAcceptanceCount(t, st, "capped"); count != store.MaxTestingHypotheses+1 {
		t.Fatalf("entries after replacement = %d", count)
	}
	// Amending an existing testing entry does not consume a new slot.
	amended, err := st.ProposePlaybook(ctx, "capped", entries[1].ID, "amended hypothesis", playbookAcceptanceAt.Add(3*time.Hour))
	if err != nil || amended.Revision != 2 {
		t.Fatalf("amendment at cap = %+v err=%v", amended, err)
	}
	if _, err := st.ProposePlaybook(ctx, "capped", 0, "still over cap", playbookAcceptanceAt.Add(4*time.Hour)); !errors.Is(err, store.ErrPlaybookCap) {
		t.Fatalf("over-cap add error = %v, want ErrPlaybookCap", err)
	}
}

func TestPlaybookProposeRejectsForeignMissingAndDecidedIDs(t *testing.T) {
	ctx := context.Background()
	st := playbookAcceptanceStore(t)
	playbookAcceptanceAgent(t, st, "owner")
	playbookAcceptanceAgent(t, st, "foreign")

	own := playbookAcceptanceAdd(t, st, "owner", "own mutable hypothesis", playbookAcceptanceAt)
	decided := playbookAcceptanceAdd(t, st, "owner", "decided hypothesis", playbookAcceptanceAt.Add(time.Minute))
	playbookAcceptanceSetState(t, st, decided.ID, "established", 30, 21)
	foreign := playbookAcceptanceAdd(t, st, "foreign", "FOREIGN SECRET TEXT", playbookAcceptanceAt)

	cases := []struct {
		name string
		id   int64
	}{
		{name: "foreign", id: foreign.ID},
		{name: "missing", id: 987654},
		{name: "decided_established", id: decided.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := st.ProposePlaybook(ctx, "owner", tc.id, "attempted rewrite", playbookAcceptanceAt.Add(time.Hour))
			if !errors.Is(err, store.ErrPlaybookImmutable) {
				t.Fatalf("error = %v, want ErrPlaybookImmutable", err)
			}
			message := err.Error()
			for _, leak := range []string{"FOREIGN SECRET TEXT", "foreign", "987654"} {
				if strings.Contains(message, leak) {
					t.Fatalf("immutable error leaked %q: %q", leak, message)
				}
			}
		})
	}
	if got := playbookAcceptanceEntries(t, st, "owner", 100); len(got) != 2 || got[0] != own {
		t.Fatalf("owner entries changed: %+v", got)
	}
	if got := playbookAcceptanceEntries(t, st, "foreign", 100); len(got) != 1 || got[0] != foreign {
		t.Fatalf("foreign entries changed: %+v", got)
	}

	if _, err := st.ProposePlaybook(ctx, "owner", -1, "negative id", playbookAcceptanceAt); !errors.Is(err, store.ErrPlaybookInput) {
		t.Fatalf("negative id error = %v, want ErrPlaybookInput", err)
	}
	playbookAcceptanceSetState(t, st, decided.ID, "rejected", 30, 1)
	if _, err := st.ProposePlaybook(ctx, "owner", decided.ID, "rewrite after rejection", playbookAcceptanceAt.Add(time.Hour)); !errors.Is(err, store.ErrPlaybookImmutable) {
		t.Fatalf("rejected id error = %v, want ErrPlaybookImmutable", err)
	}
}

func TestPlaybookProposeRefusesPausedAndKilledAgents(t *testing.T) {
	ctx := context.Background()
	st := playbookAcceptanceStore(t)
	for _, status := range []string{"paused", "killed"} {
		agentID := "stopped-" + status
		playbookAcceptanceAgent(t, st, agentID)
		entry := playbookAcceptanceAdd(t, st, agentID, "before stop", playbookAcceptanceAt)
		if _, err := st.DB().Exec(`UPDATE agents SET status=? WHERE id=?`, status, agentID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ProposePlaybook(ctx, agentID, 0, "after stop", playbookAcceptanceAt.Add(time.Hour)); err == nil {
			t.Fatalf("%s agent added an entry", status)
		}
		if _, err := st.ProposePlaybook(ctx, agentID, entry.ID, "amended after stop", playbookAcceptanceAt.Add(time.Hour)); err == nil {
			t.Fatalf("%s agent amended an entry", status)
		}
		entries := playbookAcceptanceEntries(t, st, agentID, 100)
		if len(entries) != 1 || entries[0] != entry {
			t.Fatalf("%s entries changed: %+v", status, entries)
		}
	}
}

func TestPlaybookConcurrentHandlesSeedOnceAndRespectCap(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concurrent.db")
	first, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	playbookAcceptanceAgent(t, first, "shared")

	var wg sync.WaitGroup
	created := make(chan bool, 2)
	errs := make(chan error, 2)
	for _, st := range []*store.Store{first, second} {
		wg.Add(1)
		go func(st *store.Store) {
			defer wg.Done()
			ok, err := st.InitializePlaybook(ctx, "shared", []string{"first seed", "second seed"}, playbookAcceptanceAt)
			created <- ok
			errs <- err
		}(st)
	}
	wg.Wait()
	close(created)
	close(errs)
	trues := 0
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent initialization: %v", err)
		}
	}
	for ok := range created {
		if ok {
			trues++
		}
	}
	if trues != 1 {
		t.Fatalf("concurrent initializations reported %d fresh seeds, want 1", trues)
	}
	if got := len(playbookAcceptanceEntries(t, first, "shared", 100)); got != 2 {
		t.Fatalf("concurrent seed entries = %d, want 2", got)
	}
	if count := playbookAcceptanceInitializationCount(t, first, "shared"); count != 1 {
		t.Fatalf("concurrent initialization markers = %d, want 1", count)
	}

	// Concurrent adders on separate handles must not exceed the testing cap.
	var addWG sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		st := first
		if i%2 == 1 {
			st = second
		}
		addWG.Add(1)
		go func(st *store.Store, i int) {
			defer addWG.Done()
			_, err := st.ProposePlaybook(ctx, "shared", 0, "concurrent hypothesis "+string(rune('a'+i)), playbookAcceptanceAt.Add(time.Duration(i)*time.Second))
			results <- err
		}(st, i)
	}
	addWG.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, store.ErrPlaybookCap) {
			t.Fatalf("concurrent add error = %v, want ErrPlaybookCap", err)
		}
	}
	entries := playbookAcceptanceEntries(t, first, "shared", 100)
	// The two seeded testing entries already consume two cap slots.
	wantAdds := store.MaxTestingHypotheses - 2
	if successes != wantAdds || len(entries) != store.MaxTestingHypotheses {
		t.Fatalf("concurrent successes=%d entries=%d, want %d/%d", successes, len(entries), wantAdds, store.MaxTestingHypotheses)
	}
}

func TestPlaybookPersistenceAndCancellationFailures(t *testing.T) {
	ctx := context.Background()
	st := playbookAcceptanceStore(t)
	playbookAcceptanceAgent(t, st, "cancelled")

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := st.InitializePlaybook(cancelled, "cancelled", []string{"text"}, playbookAcceptanceAt); err == nil {
		t.Fatal("cancelled initialization succeeded")
	}
	if count := playbookAcceptanceCount(t, st, "cancelled"); count != 0 {
		t.Fatalf("cancelled initialization inserted %d entries", count)
	}
	if count := playbookAcceptanceInitializationCount(t, st, "cancelled"); count != 0 {
		t.Fatalf("cancelled initialization wrote %d markers", count)
	}
	if _, err := st.ProposePlaybook(cancelled, "cancelled", 0, "text", playbookAcceptanceAt); err == nil {
		t.Fatal("cancelled proposal succeeded")
	}
	if count := playbookAcceptanceCount(t, st, "cancelled"); count != 0 {
		t.Fatalf("cancelled proposal inserted %d entries", count)
	}

	closed, err := store.Open(ctx, filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	playbookAcceptanceAgent(t, closed, "closed")
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := closed.InitializePlaybook(ctx, "closed", []string{"text"}, playbookAcceptanceAt); err == nil {
		t.Fatal("initialization against a closed store succeeded")
	}
	if _, err := closed.PlaybookEntries(ctx, "closed", 10); err == nil {
		t.Fatal("entries against a closed store succeeded")
	}
	if _, err := closed.ProposePlaybook(ctx, "closed", 0, "text", playbookAcceptanceAt); err == nil {
		t.Fatal("proposal against a closed store succeeded")
	}
}

func TestPlaybookEntryJSONUsesLowerSnakeCase(t *testing.T) {
	decided := playbookAcceptanceAt.Add(time.Hour)
	entry := store.PlaybookEntry{
		ID:        7,
		AgentID:   "agent",
		Text:      "hypothesis",
		Status:    "established",
		Revision:  3,
		N:         12,
		Successes: 8,
		CreatedAt: playbookAcceptanceAt,
		DecidedAt: &decided,
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "agent_id", "text", "status", "revision", "n", "successes", "created_at", "decided_at"} {
		if _, ok := keys[key]; !ok {
			t.Fatalf("JSON missing %q: %s", key, raw)
		}
	}
	for _, key := range []string{"AgentID", "CreatedAt", "DecidedAt", "Successes"} {
		if _, ok := keys[key]; ok {
			t.Fatalf("JSON kept Go name %q: %s", key, raw)
		}
	}
}
