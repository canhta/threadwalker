package store_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/store"
)

// This file covers the playbook CRUD contract: once-per-agent seeding, the
// testing cap, per-agent isolation, revision resets, strictly code-owned
// status/counters, rollback, closed connections and malformed persisted rows.

var crudPlaybookBase = time.Date(2026, 11, 11, 9, 30, 15, 123456789, time.UTC)

func crudPlaybookOpen(t *testing.T) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "playbook-crud.db")
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("opening playbook store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing playbook store: %v", err)
		}
	})
	return st, path
}

func crudPlaybookAgent(t *testing.T, st *store.Store, agentID string) {
	t.Helper()
	if err := st.EnsureAgent(context.Background(), agentID); err != nil {
		t.Fatalf("creating agent %q: %v", agentID, err)
	}
}

func crudPlaybookEntries(t *testing.T, st *store.Store, agentID string, limit int) []store.PlaybookEntry {
	t.Helper()
	entries, err := st.PlaybookEntries(context.Background(), agentID, limit)
	if err != nil {
		t.Fatalf("listing playbook entries for %q: %v", agentID, err)
	}
	return entries
}

func crudPlaybookCount(t *testing.T, st *store.Store, agentID string) int {
	t.Helper()
	var count int
	if err := st.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM playbook WHERE agent_id = ?`, agentID).Scan(&count); err != nil {
		t.Fatalf("counting playbook rows for %q: %v", agentID, err)
	}
	return count
}

func crudPlaybookAdd(t *testing.T, st *store.Store, agentID, text string) store.PlaybookEntry {
	t.Helper()
	entry, err := st.ProposePlaybook(context.Background(), agentID, 0, text, crudPlaybookBase)
	if err != nil {
		t.Fatalf("adding playbook entry for %q: %v", agentID, err)
	}
	return entry
}

func TestPlaybookCRUDSeedOnceAndPersistence(t *testing.T) {
	ctx := context.Background()
	st, path := crudPlaybookOpen(t)
	crudPlaybookAgent(t, st, "alpha")

	seeded, err := st.InitializePlaybook(ctx, "alpha", []string{"  ask a question  ", "name the trade-off"}, crudPlaybookBase)
	if err != nil {
		t.Fatalf("initializing playbook: %v", err)
	}
	if !seeded {
		t.Fatal("first initialization reported no state created")
	}

	entries := crudPlaybookEntries(t, st, "alpha", 100)
	if len(entries) != 2 {
		t.Fatalf("seeded entry count = %d, want 2", len(entries))
	}
	for i, wantText := range []string{"ask a question", "name the trade-off"} {
		entry := entries[i]
		if entry.ID <= 0 || entry.AgentID != "alpha" || entry.Text != wantText {
			t.Errorf("seeded entry %d = %+v, want own testing text %q", i, entry, wantText)
		}
		if entry.Status != "testing" || entry.Revision != 1 || entry.N != 0 || entry.Successes != 0 {
			t.Errorf("seeded entry %d state = status=%q revision=%d n=%d successes=%d, want testing/1/0/0",
				i, entry.Status, entry.Revision, entry.N, entry.Successes)
		}
		if !entry.CreatedAt.Equal(crudPlaybookBase) {
			t.Errorf("seeded entry %d created_at = %s, want %s", i, entry.CreatedAt, crudPlaybookBase)
		}
		if entry.DecidedAt != nil {
			t.Errorf("seeded entry %d decided_at = %v, want nil", i, entry.DecidedAt)
		}
	}

	// Runtime state written by the outcome/gate code must survive another
	// initialization, a restart and a changed seed template.
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE playbook SET n=3, successes=1 WHERE id=?`, entries[0].ID); err != nil {
		t.Fatalf("writing simulated runtime counters: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("closing store before restart: %v", err)
	}
	reopened, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopening store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	seeded, err = reopened.InitializePlaybook(ctx, "alpha", []string{"a completely different seed"}, crudPlaybookBase.Add(time.Hour))
	if err != nil {
		t.Fatalf("reinitializing playbook: %v", err)
	}
	if seeded {
		t.Fatal("second initialization reported new state")
	}
	entries = crudPlaybookEntries(t, reopened, "alpha", 100)
	if len(entries) != 2 || entries[0].Text != "ask a question" || entries[0].N != 3 || entries[0].Successes != 1 {
		t.Fatalf("reinitialization changed existing state: %+v", entries)
	}

	// The persistent marker also prevents reseeding after all rows are removed.
	if _, err := reopened.DB().ExecContext(ctx, `DELETE FROM playbook WHERE agent_id='alpha'`); err != nil {
		t.Fatalf("deleting playbook rows: %v", err)
	}
	seeded, err = reopened.InitializePlaybook(ctx, "alpha", []string{"reseed me"}, crudPlaybookBase.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("initializing empty marked playbook: %v", err)
	}
	if seeded {
		t.Fatal("marked playbook reported new state after rows were removed")
	}
	if got := crudPlaybookCount(t, reopened, "alpha"); got != 0 {
		t.Fatalf("marked playbook reseeded %d rows", got)
	}

	// An empty seed list is valid and leaves a known agent with a non-nil list.
	crudPlaybookAgent(t, reopened, "empty")
	seeded, err = reopened.InitializePlaybook(ctx, "empty", nil, crudPlaybookBase)
	if err != nil || !seeded {
		t.Fatalf("empty initialization = (%v, %v), want (true, nil)", seeded, err)
	}
	emptyEntries, err := reopened.PlaybookEntries(ctx, "empty", 100)
	if err != nil {
		t.Fatalf("listing empty playbook: %v", err)
	}
	if emptyEntries == nil || len(emptyEntries) != 0 {
		t.Fatalf("empty playbook entries = %#v, want non-nil empty slice", emptyEntries)
	}
	seeded, err = reopened.InitializePlaybook(ctx, "empty", nil, crudPlaybookBase)
	if err != nil || seeded {
		t.Fatalf("repeated empty initialization = (%v, %v), want (false, nil)", seeded, err)
	}
}

func TestPlaybookCRUDSeedingValidationAndPreExistingRows(t *testing.T) {
	ctx := context.Background()
	st, _ := crudPlaybookOpen(t)
	crudPlaybookAgent(t, st, "alpha")

	cases := []struct {
		name  string
		agent string
		texts []string
		at    time.Time
		want  error
	}{
		{name: "blank agent", agent: " ", texts: []string{"x"}, at: crudPlaybookBase, want: store.ErrPlaybookInput},
		{name: "unknown agent", agent: "missing", texts: []string{"x"}, at: crudPlaybookBase, want: store.ErrPlaybookInput},
		{name: "zero time", agent: "alpha", texts: []string{"x"}, at: time.Time{}, want: store.ErrPlaybookInput},
		{name: "blank text", agent: "alpha", texts: []string{"ok", "   "}, at: crudPlaybookBase, want: store.ErrPlaybookInput},
		{name: "too long text", agent: "alpha", texts: []string{strings.Repeat("a", store.MaxHypothesisTextRunes+1)}, at: crudPlaybookBase, want: store.ErrPlaybookInput},
		{name: "too many texts", agent: "alpha", texts: []string{"1", "2", "3", "4", "5", "6"}, at: crudPlaybookBase, want: store.ErrPlaybookCap},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seeded, err := st.InitializePlaybook(ctx, tc.agent, tc.texts, tc.at)
			if !errors.Is(err, tc.want) {
				t.Fatalf("InitializePlaybook error = %v, want %v", err, tc.want)
			}
			if seeded {
				t.Fatal("invalid initialization reported state created")
			}
		})
	}
	if got := crudPlaybookCount(t, st, "alpha"); got != 0 {
		t.Fatalf("invalid initialization inserted %d rows", got)
	}

	// Exactly five entries is the boundary and stores Unicode codepoints, not bytes.
	five := []string{"1", "2", "3", "4", strings.Repeat("界", store.MaxHypothesisTextRunes)}
	seeded, err := st.InitializePlaybook(ctx, "alpha", five, crudPlaybookBase)
	if err != nil || !seeded {
		t.Fatalf("five-entry initialization = (%v, %v), want (true, nil)", seeded, err)
	}
	if got := crudPlaybookCount(t, st, "alpha"); got != 5 {
		t.Fatalf("five-entry initialization inserted %d rows", got)
	}

	// Pre-existing manual rows: mark initialized without inserting or
	// overwriting, but only while the testing cap holds.
	crudPlaybookAgent(t, st, "manual")
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO playbook(agent_id,text,status,created_at) VALUES('manual','manual runtime text','testing',?)`,
		crudPlaybookBase.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("inserting manual playbook row: %v", err)
	}
	seeded, err = st.InitializePlaybook(ctx, "manual", []string{"seed text"}, crudPlaybookBase)
	if err != nil || !seeded {
		t.Fatalf("pre-existing initialization = (%v, %v), want (true, nil)", seeded, err)
	}
	entries := crudPlaybookEntries(t, st, "manual", 100)
	if len(entries) != 1 || entries[0].Text != "manual runtime text" {
		t.Fatalf("pre-existing initialization reseeded rows: %+v", entries)
	}
	seeded, err = st.InitializePlaybook(ctx, "manual", nil, crudPlaybookBase)
	if err != nil || seeded {
		t.Fatalf("second pre-existing initialization = (%v, %v), want (false, nil)", seeded, err)
	}

	crudPlaybookAgent(t, st, "overfull")
	for i := 0; i < store.MaxTestingHypotheses+1; i++ {
		if _, err := st.DB().ExecContext(ctx,
			`INSERT INTO playbook(agent_id,text,status,created_at) VALUES('overfull',?,'testing',?)`,
			fmt.Sprintf("manual %d", i), crudPlaybookBase.Format(time.RFC3339Nano)); err != nil {
			t.Fatalf("inserting overfull manual row: %v", err)
		}
	}
	if _, err := st.InitializePlaybook(ctx, "overfull", []string{"seed"}, crudPlaybookBase); !errors.Is(err, store.ErrPlaybookCap) {
		t.Fatalf("overfull pre-existing initialization error = %v, want cap error", err)
	}
	var marker int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM playbook_initializations WHERE agent_id='overfull'`).Scan(&marker); err != nil {
		t.Fatalf("counting initialization marker: %v", err)
	}
	if marker != 0 {
		t.Fatal("overfull initialization recorded a marker")
	}
}

func TestPlaybookCRUDConcurrentCapAcrossHandles(t *testing.T) {
	ctx := context.Background()
	st1, path := crudPlaybookOpen(t)
	crudPlaybookAgent(t, st1, "alpha")
	st2, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("opening second handle: %v", err)
	}
	defer st2.Close()

	const workers = 20
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, workers)
	created := make(chan store.PlaybookEntry, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			st := st1
			if i%2 == 1 {
				st = st2
			}
			entry, err := st.ProposePlaybook(ctx, "alpha", 0, fmt.Sprintf("hypothesis %d", i), crudPlaybookBase.Add(time.Duration(i)*time.Second))
			if err != nil {
				errs <- err
				return
			}
			created <- entry
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	close(created)

	gotCreated := 0
	for range created {
		gotCreated++
	}
	gotCap := 0
	for err := range errs {
		if !errors.Is(err, store.ErrPlaybookCap) {
			t.Errorf("concurrent proposal error = %v, want cap error", err)
		} else {
			gotCap++
		}
	}
	if gotCreated != store.MaxTestingHypotheses || gotCap != workers-store.MaxTestingHypotheses {
		t.Fatalf("concurrent outcomes = %d created, %d cap errors; want %d and %d",
			gotCreated, gotCap, store.MaxTestingHypotheses, workers-store.MaxTestingHypotheses)
	}
	entries := crudPlaybookEntries(t, st2, "alpha", 100)
	if len(entries) != store.MaxTestingHypotheses {
		t.Fatalf("persisted entries = %d, want %d", len(entries), store.MaxTestingHypotheses)
	}
	for _, entry := range entries {
		if entry.Revision != 1 || entry.Status != "testing" || entry.N != 0 || entry.Successes != 0 {
			t.Errorf("concurrent entry state = %+v, want testing revision 1 with zero evidence", entry)
		}
	}
}

func TestPlaybookCRUDPerAgentIsolationAndOrdering(t *testing.T) {
	ctx := context.Background()
	st, _ := crudPlaybookOpen(t)
	crudPlaybookAgent(t, st, "alpha")
	crudPlaybookAgent(t, st, "beta")

	alphaA := crudPlaybookAdd(t, st, "alpha", "alpha one")
	alphaB := crudPlaybookAdd(t, st, "alpha", "alpha two")
	betaA := crudPlaybookAdd(t, st, "beta", "beta one")

	alphaEntries := crudPlaybookEntries(t, st, "alpha", 100)
	if len(alphaEntries) != 2 || alphaEntries[0].ID != alphaA.ID || alphaEntries[1].ID != alphaB.ID {
		t.Fatalf("alpha entries = %+v, want own ids %d,%d", alphaEntries, alphaA.ID, alphaB.ID)
	}
	betaEntries := crudPlaybookEntries(t, st, "beta", 100)
	if len(betaEntries) != 1 || betaEntries[0].ID != betaA.ID || betaEntries[0].Text != "beta one" {
		t.Fatalf("beta entries = %+v, want only own id %d", betaEntries, betaA.ID)
	}

	_, err := st.ProposePlaybook(ctx, "alpha", betaA.ID, "hijacked", crudPlaybookBase.Add(time.Hour))
	if !errors.Is(err, store.ErrPlaybookImmutable) {
		t.Fatalf("foreign amendment error = %v, want immutable error", err)
	}
	if strings.Contains(err.Error(), "hijacked") || strings.Contains(err.Error(), fmt.Sprint(betaA.ID)) {
		t.Fatalf("foreign amendment error leaked text or id: %v", err)
	}
	betaEntries = crudPlaybookEntries(t, st, "beta", 100)
	if len(betaEntries) != 1 || betaEntries[0].Text != "beta one" || betaEntries[0].Revision != 1 {
		t.Fatalf("foreign amendment changed beta: %+v", betaEntries)
	}

	// Status ordering is testing, established, rejected, then id.
	crudPlaybookAgent(t, st, "order")
	for i := 0; i < 3; i++ {
		crudPlaybookAdd(t, st, "order", fmt.Sprintf("order %d", i))
	}
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE playbook SET status='established', decided_at=? WHERE agent_id='order' AND text='order 0'`,
		crudPlaybookBase.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("marking established: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE playbook SET status='rejected', decided_at=? WHERE agent_id='order' AND text='order 1'`,
		crudPlaybookBase.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("marking rejected: %v", err)
	}
	ordered := crudPlaybookEntries(t, st, "order", 100)
	if len(ordered) != 3 || ordered[0].Text != "order 2" || ordered[1].Text != "order 0" || ordered[2].Text != "order 1" {
		t.Fatalf("status ordering = %+v, want testing, established, rejected", ordered)
	}
	if ordered[0].DecidedAt != nil {
		t.Errorf("testing decided_at = %v, want nil", ordered[0].DecidedAt)
	}
	for i := 1; i < 3; i++ {
		if ordered[i].DecidedAt == nil || !ordered[i].DecidedAt.Equal(crudPlaybookBase) {
			t.Errorf("terminal entry %d decided_at = %v, want %s", i, ordered[i].DecidedAt, crudPlaybookBase)
		}
	}
}

func TestPlaybookCRUDAmendRevisionResetAndIdenticalText(t *testing.T) {
	ctx := context.Background()
	st, _ := crudPlaybookOpen(t)
	crudPlaybookAgent(t, st, "alpha")
	entry := crudPlaybookAdd(t, st, "alpha", "first text")

	if _, err := st.DB().ExecContext(ctx,
		`UPDATE playbook SET n=4, successes=2 WHERE id=?`, entry.ID); err != nil {
		t.Fatalf("writing simulated evidence: %v", err)
	}
	amended, err := st.ProposePlaybook(ctx, "alpha", entry.ID, "  second text  ", crudPlaybookBase.Add(time.Hour))
	if err != nil {
		t.Fatalf("amending testing entry: %v", err)
	}
	if amended.ID != entry.ID || amended.Text != "second text" || amended.Revision != 2 {
		t.Fatalf("amended entry = %+v, want same id, trimmed text, revision 2", amended)
	}
	if amended.N != 0 || amended.Successes != 0 {
		t.Fatalf("amended entry evidence = n=%d successes=%d, want reset to 0", amended.N, amended.Successes)
	}
	if !amended.CreatedAt.Equal(entry.CreatedAt) {
		t.Fatalf("amended created_at = %s, want preserved %s", amended.CreatedAt, entry.CreatedAt)
	}
	if amended.Status != "testing" {
		t.Fatalf("amended status = %q, want testing", amended.Status)
	}

	// Identical text is a no-op, even after the counters have moved.
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE playbook SET n=2, successes=1 WHERE id=?`, entry.ID); err != nil {
		t.Fatalf("writing evidence after amendment: %v", err)
	}
	same, err := st.ProposePlaybook(ctx, "alpha", entry.ID, "second text", crudPlaybookBase.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("re-proposing identical text: %v", err)
	}
	if same.Revision != 2 || same.N != 2 || same.Successes != 1 || !same.CreatedAt.Equal(entry.CreatedAt) {
		t.Fatalf("identical proposal changed state: %+v", same)
	}

	// Amending at the cap is allowed because it creates no new testing entry.
	for i := 0; i < store.MaxTestingHypotheses-1; i++ {
		crudPlaybookAdd(t, st, "alpha", fmt.Sprintf("fill %d", i))
	}
	amended, err = st.ProposePlaybook(ctx, "alpha", entry.ID, "third text", crudPlaybookBase.Add(3*time.Hour))
	if err != nil {
		t.Fatalf("amending at the cap: %v", err)
	}
	if amended.Revision != 3 || amended.Text != "third text" {
		t.Fatalf("cap amendment = %+v, want revision 3", amended)
	}

	// Mutating a returned value never writes through to the row.
	amended.Text = "mutated in caller"
	amended.Status = "established"
	amended.N = 99
	persisted := crudPlaybookEntries(t, st, "alpha", 100)
	var found *store.PlaybookEntry
	for i := range persisted {
		if persisted[i].ID == entry.ID {
			found = &persisted[i]
			break
		}
	}
	if found == nil || found.Text != "third text" || found.Status != "testing" || found.N != 0 {
		t.Fatalf("caller mutation reached the database: %+v", persisted)
	}
}

func TestPlaybookCRUDImmutableTargetsAndSafeErrors(t *testing.T) {
	ctx := context.Background()
	st, _ := crudPlaybookOpen(t)
	crudPlaybookAgent(t, st, "alpha")
	entry := crudPlaybookAdd(t, st, "alpha", "original")
	decided := crudPlaybookAdd(t, st, "alpha", "decided")
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE playbook SET status='established', decided_at=? WHERE id=?`,
		crudPlaybookBase.Format(time.RFC3339Nano), decided.ID); err != nil {
		t.Fatalf("deciding entry: %v", err)
	}

	cases := []struct {
		name string
		id   int64
		want error
	}{
		{name: "negative", id: -1, want: store.ErrPlaybookInput},
		{name: "missing", id: 999999, want: store.ErrPlaybookImmutable},
		{name: "decided", id: decided.ID, want: store.ErrPlaybookImmutable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := st.ProposePlaybook(ctx, "alpha", tc.id, "replacement", crudPlaybookBase.Add(time.Hour))
			if !errors.Is(err, tc.want) {
				t.Fatalf("ProposePlaybook error = %v, want %v", err, tc.want)
			}
			if got != (store.PlaybookEntry{}) {
				t.Fatalf("failed proposal returned entry %+v, want zero value", got)
			}
		})
	}
	entries := crudPlaybookEntries(t, st, "alpha", 100)
	if len(entries) != 2 || entries[0].ID != entry.ID || entries[0].Text != "original" || entries[0].Revision != 1 {
		t.Fatalf("immutable proposals changed testing entry: %+v", entries)
	}
	if entries[1].Status != "established" || entries[1].Text != "decided" {
		t.Fatalf("immutable proposals changed decided entry: %+v", entries[1])
	}
}

func TestPlaybookCRUDInputAndAgentStatusValidation(t *testing.T) {
	ctx := context.Background()
	st, _ := crudPlaybookOpen(t)
	crudPlaybookAgent(t, st, "alpha")
	crudPlaybookAgent(t, st, "paused")
	crudPlaybookAgent(t, st, "killed")
	if err := st.PauseOwnerAgent(ctx, "paused"); err != nil {
		t.Fatalf("pausing agent: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE agents SET status='killed' WHERE id='killed'`); err != nil {
		t.Fatalf("killing agent: %v", err)
	}

	proposalCases := []struct {
		name  string
		agent string
		id    int64
		text  string
		at    time.Time
		want  error
	}{
		{name: "blank agent", agent: " ", text: "x", at: crudPlaybookBase, want: store.ErrPlaybookInput},
		{name: "unknown agent", agent: "missing", text: "x", at: crudPlaybookBase, want: store.ErrPlaybookInput},
		{name: "blank text", agent: "alpha", text: " \t ", at: crudPlaybookBase, want: store.ErrPlaybookInput},
		{name: "too long", agent: "alpha", text: strings.Repeat("界", store.MaxHypothesisTextRunes+1), at: crudPlaybookBase, want: store.ErrPlaybookInput},
		{name: "zero time", agent: "alpha", text: "x", at: time.Time{}, want: store.ErrPlaybookInput},
		{name: "paused", agent: "paused", text: "x", at: crudPlaybookBase, want: store.ErrPlaybookInput},
		{name: "killed", agent: "killed", text: "x", at: crudPlaybookBase, want: store.ErrPlaybookInput},
	}
	for _, tc := range proposalCases {
		t.Run("propose/"+tc.name, func(t *testing.T) {
			if _, err := st.ProposePlaybook(ctx, tc.agent, tc.id, tc.text, tc.at); !errors.Is(err, tc.want) {
				t.Fatalf("ProposePlaybook error = %v, want %v", err, tc.want)
			}
		})
	}
	for _, agent := range []string{"paused", "killed"} {
		t.Run("initialize/"+agent, func(t *testing.T) {
			if _, err := st.InitializePlaybook(ctx, agent, []string{"x"}, crudPlaybookBase); !errors.Is(err, store.ErrPlaybookInput) {
				t.Fatalf("InitializePlaybook error = %v, want input error", err)
			}
		})
	}

	// Limit and listing validation.
	for _, limit := range []int{-1, 0, 101} {
		if _, err := st.PlaybookEntries(ctx, "alpha", limit); !errors.Is(err, store.ErrPlaybookInput) {
			t.Errorf("PlaybookEntries limit %d error = %v, want input error", limit, err)
		}
	}
	for _, agent := range []string{"", "  ", "missing"} {
		if _, err := st.PlaybookEntries(ctx, agent, 10); !errors.Is(err, store.ErrPlaybookInput) {
			t.Errorf("PlaybookEntries agent %q error = %v, want input error", agent, err)
		}
	}
}

func TestPlaybookCRUDRollbackOnFailureAndCancellation(t *testing.T) {
	ctx := context.Background()
	st, _ := crudPlaybookOpen(t)
	crudPlaybookAgent(t, st, "alpha")

	if _, err := st.DB().ExecContext(ctx, `
		CREATE TRIGGER playbook_crud_fail_insert BEFORE INSERT ON playbook
		WHEN NEW.text = 'trigger-fail'
		BEGIN SELECT RAISE(ABORT, 'forced failure'); END;`); err != nil {
		t.Fatalf("creating failing playbook trigger: %v", err)
	}
	if _, err := st.ProposePlaybook(ctx, "alpha", 0, "trigger-fail", crudPlaybookBase); err == nil {
		t.Fatal("proposal under failing trigger succeeded")
	}
	if got := crudPlaybookCount(t, st, "alpha"); got != 0 {
		t.Fatalf("failed proposal left %d rows", got)
	}

	if _, err := st.InitializePlaybook(ctx, "alpha", []string{"first", "trigger-fail", "third"}, crudPlaybookBase); err == nil {
		t.Fatal("initialization under failing trigger succeeded")
	}
	if got := crudPlaybookCount(t, st, "alpha"); got != 0 {
		t.Fatalf("failed initialization left %d playbook rows", got)
	}
	var marker int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM playbook_initializations WHERE agent_id='alpha'`).Scan(&marker); err != nil {
		t.Fatalf("counting marker after failure: %v", err)
	}
	if marker != 0 {
		t.Fatalf("failed initialization left %d markers", marker)
	}

	if _, err := st.DB().ExecContext(ctx, `DROP TRIGGER playbook_crud_fail_insert`); err != nil {
		t.Fatalf("dropping failing insert trigger: %v", err)
	}
	entry := crudPlaybookAdd(t, st, "alpha", "stable")
	if _, err := st.DB().ExecContext(ctx, `
		CREATE TRIGGER playbook_crud_fail_update BEFORE UPDATE ON playbook
		BEGIN SELECT RAISE(ABORT, 'forced failure'); END;`); err != nil {
		t.Fatalf("creating failing update trigger: %v", err)
	}
	if _, err := st.ProposePlaybook(ctx, "alpha", entry.ID, "changed", crudPlaybookBase.Add(time.Hour)); err == nil {
		t.Fatal("amendment under failing trigger succeeded")
	}
	entries := crudPlaybookEntries(t, st, "alpha", 100)
	if len(entries) != 1 || entries[0].Text != "stable" || entries[0].Revision != 1 {
		t.Fatalf("failed amendment changed state: %+v", entries)
	}
	if _, err := st.DB().ExecContext(ctx, `DROP TRIGGER playbook_crud_fail_update`); err != nil {
		t.Fatalf("dropping failing update trigger: %v", err)
	}

	// A cancelled context must not create a row or a marker.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := st.ProposePlaybook(cancelled, "alpha", 0, "cancelled add", crudPlaybookBase.Add(time.Hour)); err == nil {
		t.Fatal("cancelled proposal succeeded")
	}
	crudPlaybookAgent(t, st, "cancelled")
	if _, err := st.InitializePlaybook(cancelled, "cancelled", []string{"x"}, crudPlaybookBase); err == nil {
		t.Fatal("cancelled initialization succeeded")
	}
	if got := crudPlaybookCount(t, st, "alpha"); got != 1 {
		t.Fatalf("cancelled proposal changed row count to %d", got)
	}
	if got := crudPlaybookCount(t, st, "cancelled"); got != 0 {
		t.Fatalf("cancelled initialization inserted %d rows", got)
	}
}

func TestPlaybookCRUDClosedDBAndMalformedRows(t *testing.T) {
	ctx := context.Background()
	st, _ := crudPlaybookOpen(t)
	crudPlaybookAgent(t, st, "alpha")
	entry := crudPlaybookAdd(t, st, "alpha", "row text")

	// Each malformed persisted shape must fail the read instead of being
	// silently normalised.
	malformed := []struct {
		statement string
		args      []any
	}{
		{statement: `UPDATE playbook SET n=-1 WHERE id=?`, args: []any{entry.ID}},
		{statement: `UPDATE playbook SET successes=n+1 WHERE id=?`, args: []any{entry.ID}},
		{statement: `UPDATE playbook SET created_at='not-a-time' WHERE id=?`, args: []any{entry.ID}},
		{statement: `UPDATE playbook SET decided_at=? WHERE id=?`, args: []any{crudPlaybookBase.Format(time.RFC3339Nano), entry.ID}},
	}
	for i, tc := range malformed {
		if _, err := st.DB().ExecContext(ctx,
			`UPDATE playbook SET status='testing', n=1, successes=0, created_at=?, decided_at=NULL WHERE id=?`,
			crudPlaybookBase.Format(time.RFC3339Nano), entry.ID); err != nil {
			t.Fatalf("reset malformed row %d: %v", i, err)
		}
		if _, err := st.DB().ExecContext(ctx, tc.statement, tc.args...); err != nil {
			t.Fatalf("writing malformed row %d: %v", i, err)
		}
		if _, err := st.PlaybookEntries(ctx, "alpha", 10); err == nil {
			t.Errorf("malformed row %d was accepted", i)
		}
	}

	// Terminal status without a decision time is malformed too.
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE playbook SET status='established', n=1, successes=0, decided_at=NULL WHERE id=?`, entry.ID); err != nil {
		t.Fatalf("writing terminal row without decision time: %v", err)
	}
	if _, err := st.PlaybookEntries(ctx, "alpha", 10); err == nil {
		t.Error("terminal row without decided_at was accepted")
	}

	// A status outside the enum is malformed even if the table's check
	// constraint is bypassed by a writer.
	conn, err := st.DB().Conn(ctx)
	if err != nil {
		t.Fatalf("opening malformed-status connection: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA ignore_check_constraints=ON`); err != nil {
		conn.Close()
		t.Fatalf("disabling check constraints: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		`UPDATE playbook SET status='bogus', decided_at=? WHERE id=?`,
		crudPlaybookBase.Format(time.RFC3339Nano), entry.ID); err != nil {
		conn.Close()
		t.Fatalf("writing invalid status: %v", err)
	}
	conn.Close()
	if _, err := st.PlaybookEntries(ctx, "alpha", 10); err == nil {
		t.Error("invalid persisted status was accepted")
	}

	// Closed connections return errors instead of panicking. This store is
	// opened directly so the cleanup from crudPlaybookOpen doesn't close twice.
	closed, err := store.Open(ctx, filepath.Join(t.TempDir(), "playbook-closed.db"))
	if err != nil {
		t.Fatalf("opening store for close test: %v", err)
	}
	if err := closed.Close(); err != nil {
		t.Fatalf("closing store under test: %v", err)
	}
	if _, err := closed.InitializePlaybook(ctx, "alpha", []string{"x"}, crudPlaybookBase); err == nil {
		t.Error("InitializePlaybook on closed store succeeded")
	}
	if _, err := closed.PlaybookEntries(ctx, "alpha", 10); err == nil {
		t.Error("PlaybookEntries on closed store succeeded")
	}
	if _, err := closed.ProposePlaybook(ctx, "alpha", 0, "x", crudPlaybookBase); err == nil {
		t.Error("ProposePlaybook on closed store succeeded")
	}
}

func TestPlaybookCRUDTimestampsAndLimits(t *testing.T) {
	ctx := context.Background()
	st, _ := crudPlaybookOpen(t)
	crudPlaybookAgent(t, st, "alpha")
	for i := 0; i < 3; i++ {
		crudPlaybookAdd(t, st, "alpha", fmt.Sprintf("entry %d", i))
	}

	zoned := crudPlaybookBase.In(time.FixedZone("UTC+7", 7*3600))
	entry, err := st.ProposePlaybook(ctx, "alpha", 0, "zoned", zoned)
	if err != nil {
		t.Fatalf("proposing with zoned time: %v", err)
	}
	if !entry.CreatedAt.Equal(zoned) || entry.CreatedAt.Location() != time.UTC {
		t.Fatalf("created_at = %s, want UTC instant %s", entry.CreatedAt, zoned)
	}

	all := crudPlaybookEntries(t, st, "alpha", 100)
	if len(all) != 4 {
		t.Fatalf("entries = %d, want 4", len(all))
	}
	if one := crudPlaybookEntries(t, st, "alpha", 1); len(one) != 1 || one[0].ID != all[0].ID {
		t.Fatalf("limit 1 = %+v, want id %d", one, all[0].ID)
	}
	limited := crudPlaybookEntries(t, st, "alpha", 2)
	if len(limited) != 2 || limited[0].ID != all[0].ID || limited[1].ID != all[1].ID {
		t.Fatalf("limit 2 = %+v, want first two ids %d,%d", limited, all[0].ID, all[1].ID)
	}
}

func TestPlaybookCRUDConcurrentInitializationIsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	st1, path := crudPlaybookOpen(t)
	crudPlaybookAgent(t, st1, "alpha")
	st2, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("opening second initialization handle: %v", err)
	}
	defer st2.Close()

	const workers = 12
	start := make(chan struct{})
	results := make(chan bool, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			st := st1
			if i%2 == 1 {
				st = st2
			}
			texts := []string{"a", "b"}
			if i%3 == 0 {
				texts = []string{"c", "d", "e"}
			}
			created, err := st.InitializePlaybook(ctx, "alpha", texts, crudPlaybookBase.Add(time.Duration(i)*time.Minute))
			if err != nil {
				errs <- err
				return
			}
			results <- created
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	close(results)

	createdCount := 0
	for created := range results {
		if created {
			createdCount++
		}
	}
	for err := range errs {
		t.Errorf("concurrent initialization error: %v", err)
	}
	if createdCount != 1 {
		t.Fatalf("concurrent initializations reported %d creators, want exactly 1", createdCount)
	}
	var markers int
	if err := st2.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM playbook_initializations WHERE agent_id='alpha'`).Scan(&markers); err != nil {
		t.Fatalf("counting initialization markers: %v", err)
	}
	if markers != 1 {
		t.Fatalf("initialization markers = %d, want 1", markers)
	}
	entries := crudPlaybookEntries(t, st2, "alpha", 100)
	if len(entries) != 2 && len(entries) != 3 {
		t.Fatalf("seeded entries = %d, want one complete two- or three-entry seed", len(entries))
	}
	for _, entry := range entries {
		if entry.Revision != 1 || entry.Status != "testing" || entry.N != 0 || entry.Successes != 0 {
			t.Fatalf("concurrently seeded entry = %+v, want untouched testing revision 1", entry)
		}
	}
}

func TestPlaybookCRUDConcurrentAmendmentsSerialize(t *testing.T) {
	ctx := context.Background()
	st1, path := crudPlaybookOpen(t)
	crudPlaybookAgent(t, st1, "alpha")
	entry := crudPlaybookAdd(t, st1, "alpha", "original")
	st2, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("opening second amendment handle: %v", err)
	}
	defer st2.Close()

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i, text := range []string{"left", "right"} {
		wg.Add(1)
		go func(i int, text string) {
			defer wg.Done()
			<-start
			st := st1
			if i == 1 {
				st = st2
			}
			_, err := st.ProposePlaybook(ctx, "alpha", entry.ID, text, crudPlaybookBase.Add(time.Duration(i+1)*time.Hour))
			if err != nil {
				errs <- err
			}
		}(i, text)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent amendment error: %v", err)
	}

	entries := crudPlaybookEntries(t, st2, "alpha", 100)
	if len(entries) != 1 {
		t.Fatalf("concurrent amendments produced %d rows, want 1", len(entries))
	}
	final := entries[0]
	if final.Revision != 3 {
		t.Fatalf("concurrent amendment revision = %d, want 3", final.Revision)
	}
	if final.Text != "left" && final.Text != "right" {
		t.Fatalf("concurrent amendment text = %q, want one proposer's text", final.Text)
	}
	if final.N != 0 || final.Successes != 0 || final.Status != "testing" {
		t.Fatalf("concurrent amendment evidence/status = %+v, want reset testing entry", final)
	}
	if !final.CreatedAt.Equal(entry.CreatedAt) {
		t.Fatalf("concurrent amendment created_at = %s, want preserved %s", final.CreatedAt, entry.CreatedAt)
	}
}

func TestPlaybookCRUDTextBoundaryPausedReadsAndSafeCorruptionErrors(t *testing.T) {
	ctx := context.Background()
	st, _ := crudPlaybookOpen(t)
	crudPlaybookAgent(t, st, "alpha")

	boundary := strings.Repeat("界", store.MaxHypothesisTextRunes)
	entry, err := st.ProposePlaybook(ctx, "alpha", 0, boundary, crudPlaybookBase)
	if err != nil {
		t.Fatalf("proposing exactly %d codepoints: %v", store.MaxHypothesisTextRunes, err)
	}
	if entry.Text != boundary {
		t.Fatal("boundary text was not stored exactly")
	}
	if _, err := st.ProposePlaybook(ctx, "alpha", 0, boundary+"界", crudPlaybookBase); !errors.Is(err, store.ErrPlaybookInput) {
		t.Fatalf("oversized proposal error = %v, want input error", err)
	}
	farFuture := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := st.ProposePlaybook(ctx, "alpha", 0, "future", farFuture); !errors.Is(err, store.ErrPlaybookInput) {
		t.Fatalf("year-10000 proposal error = %v, want input error", err)
	}
	if _, err := st.InitializePlaybook(ctx, "alpha", []string{"x"}, farFuture); !errors.Is(err, store.ErrPlaybookInput) {
		t.Fatalf("year-10000 initialization error = %v, want input error", err)
	}

	// Paused and killed agents keep read access to their own playbook but can
	// neither add nor amend.
	for _, status := range []string{"paused", "killed"} {
		agentID := "reader-" + status
		crudPlaybookAgent(t, st, agentID)
		existing := crudPlaybookAdd(t, st, agentID, "readable")
		if status == "paused" {
			if err := st.PauseOwnerAgent(ctx, agentID); err != nil {
				t.Fatalf("pausing %q: %v", agentID, err)
			}
		} else if _, err := st.DB().ExecContext(ctx, `UPDATE agents SET status='killed' WHERE id=?`, agentID); err != nil {
			t.Fatalf("killing %q: %v", agentID, err)
		}
		entries := crudPlaybookEntries(t, st, agentID, 100)
		if len(entries) != 1 || entries[0].ID != existing.ID {
			t.Fatalf("%s read = %+v, want own existing entry", status, entries)
		}
		if _, err := st.ProposePlaybook(ctx, agentID, existing.ID, "amended", crudPlaybookBase.Add(time.Hour)); !errors.Is(err, store.ErrPlaybookInput) {
			t.Fatalf("%s amendment error = %v, want input error", status, err)
		}
		entries = crudPlaybookEntries(t, st, agentID, 100)
		if len(entries) != 1 || entries[0].Text != "readable" || entries[0].Revision != 1 {
			t.Fatalf("%s amendment changed state: %+v", status, entries)
		}
	}

	// Per-agent initialization markers are independent.
	crudPlaybookAgent(t, st, "seed-one")
	crudPlaybookAgent(t, st, "seed-two")
	if created, err := st.InitializePlaybook(ctx, "seed-one", []string{"one"}, crudPlaybookBase); err != nil || !created {
		t.Fatalf("seed-one initialization = (%v, %v)", created, err)
	}
	if created, err := st.InitializePlaybook(ctx, "seed-two", []string{"two", "three"}, crudPlaybookBase); err != nil || !created {
		t.Fatalf("seed-two initialization = (%v, %v)", created, err)
	}
	if got := crudPlaybookCount(t, st, "seed-one"); got != 1 {
		t.Fatalf("seed-one rows = %d, want 1", got)
	}
	if got := crudPlaybookCount(t, st, "seed-two"); got != 2 {
		t.Fatalf("seed-two rows = %d, want 2", got)
	}

	// A malformed row's error must not echo hypothesis text.
	const secret = "SECRET-HYPOTHESIS-TEXT"
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO playbook(agent_id,text,status,revision,n,successes,created_at) VALUES('alpha',?,'testing',1,-1,0,?)`,
		secret, crudPlaybookBase.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("inserting secret malformed row: %v", err)
	}
	_, err = st.PlaybookEntries(ctx, "alpha", 100)
	if err == nil {
		t.Fatal("malformed secret row was accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("malformed-row error leaked hypothesis text: %v", err)
	}
}
