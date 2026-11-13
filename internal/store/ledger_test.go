package store_test

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"testing"

	"github.com/canhta/threadz/internal/store"
)

const ledgerTestDay = "2026-11-11"

func openLedgerTestStore(t *testing.T) *store.Store {
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

func insertLedgerTestWake(t *testing.T, st *store.Store, agentID string) int64 {
	t.Helper()
	result, err := st.DB().ExecContext(context.Background(),
		`INSERT INTO wakes (agent_id, started_at) VALUES (?, ?)`, agentID, "2026-11-11T00:00:00Z")
	if err != nil {
		t.Fatalf("inserting wake for agent %q: %v", agentID, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("reading inserted wake id: %v", err)
	}
	return id
}

func wakeUsage(t *testing.T, st *store.Store, wakeID int64) store.LLMUsage {
	t.Helper()
	var usage store.LLMUsage
	err := st.DB().QueryRowContext(context.Background(),
		`SELECT tokens_in, tokens_out, cost_usd FROM wakes WHERE id = ?`, wakeID).
		Scan(&usage.TokensIn, &usage.TokensOut, &usage.CostUSD)
	if err != nil {
		t.Fatalf("reading wake %d usage: %v", wakeID, err)
	}
	return usage
}

func assertUsage(t *testing.T, got, want store.LLMUsage) {
	t.Helper()
	if got.TokensIn != want.TokensIn || got.TokensOut != want.TokensOut || math.Abs(got.CostUSD-want.CostUSD) > 1e-9 {
		t.Fatalf("usage = %+v, want %+v", got, want)
	}
}

func TestRecordLLMUsage_AccumulatesTotalsInLedgerAndWake(t *testing.T) {
	ctx := context.Background()
	st := openLedgerTestStore(t)
	wakeID := insertLedgerTestWake(t, st, "fixture")

	responses := []store.LLMUsage{
		{TokensIn: 100, TokensOut: 40, CostUSD: 0.25},
		{TokensIn: 30, TokensOut: 12, CostUSD: 0.5},
	}
	for _, usage := range responses {
		if err := st.RecordLLMUsage(ctx, "fixture", wakeID, ledgerTestDay, usage); err != nil {
			t.Fatalf("RecordLLMUsage(%+v): %v", usage, err)
		}
	}

	want := store.LLMUsage{TokensIn: 130, TokensOut: 52, CostUSD: 0.75}
	ledgerTotals, err := st.LLMUsageForDay(ctx, "fixture", ledgerTestDay)
	if err != nil {
		t.Fatalf("LLMUsageForDay: %v", err)
	}
	assertUsage(t, ledgerTotals, want)
	assertUsage(t, wakeUsage(t, st, wakeID), want)
}

func TestLLMUsageForDay_IsolatesAgentAndDay(t *testing.T) {
	ctx := context.Background()
	st := openLedgerTestStore(t)
	wakeA1 := insertLedgerTestWake(t, st, "agent-a")
	wakeA2 := insertLedgerTestWake(t, st, "agent-a")
	wakeB := insertLedgerTestWake(t, st, "agent-b")

	record := func(agentID string, wakeID int64, day string, usage store.LLMUsage) {
		t.Helper()
		if err := st.RecordLLMUsage(ctx, agentID, wakeID, day, usage); err != nil {
			t.Fatalf("RecordLLMUsage(%s, %s, %+v): %v", agentID, day, usage, err)
		}
	}
	record("agent-a", wakeA1, "2026-11-11", store.LLMUsage{TokensIn: 10, TokensOut: 1, CostUSD: 0.1})
	record("agent-a", wakeA2, "2026-11-12", store.LLMUsage{TokensIn: 20, TokensOut: 2, CostUSD: 0.2})
	record("agent-b", wakeB, "2026-11-11", store.LLMUsage{TokensIn: 30, TokensOut: 3, CostUSD: 0.3})

	cases := []struct {
		name    string
		agentID string
		day     string
		want    store.LLMUsage
	}{
		{"agent a first day", "agent-a", "2026-11-11", store.LLMUsage{TokensIn: 10, TokensOut: 1, CostUSD: 0.1}},
		{"agent a second day", "agent-a", "2026-11-12", store.LLMUsage{TokensIn: 20, TokensOut: 2, CostUSD: 0.2}},
		{"agent b first day", "agent-b", "2026-11-11", store.LLMUsage{TokensIn: 30, TokensOut: 3, CostUSD: 0.3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := st.LLMUsageForDay(ctx, tc.agentID, tc.day)
			if err != nil {
				t.Fatalf("LLMUsageForDay(%s, %s): %v", tc.agentID, tc.day, err)
			}
			assertUsage(t, got, tc.want)
		})
	}
}

func TestLLMUsageForDay_ReturnsZeroForMissingDay(t *testing.T) {
	ctx := context.Background()
	st := openLedgerTestStore(t)
	wakeID := insertLedgerTestWake(t, st, "fixture")
	if err := st.RecordLLMUsage(ctx, "fixture", wakeID, ledgerTestDay, store.LLMUsage{TokensIn: 7, TokensOut: 3, CostUSD: 0.07}); err != nil {
		t.Fatalf("RecordLLMUsage: %v", err)
	}

	cases := []struct {
		name    string
		agentID string
		day     string
	}{
		{"missing day", "fixture", "2026-11-12"},
		{"missing agent", "other-agent", ledgerTestDay},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := st.LLMUsageForDay(ctx, tc.agentID, tc.day)
			if err != nil {
				t.Fatalf("LLMUsageForDay(%s, %s): %v", tc.agentID, tc.day, err)
			}
			assertUsage(t, got, store.LLMUsage{})
		})
	}
}

func TestRecordLLMUsage_MissingWakeLeavesTotalsUnchanged(t *testing.T) {
	ctx := context.Background()
	st := openLedgerTestStore(t)
	wakeID := insertLedgerTestWake(t, st, "fixture")
	before := store.LLMUsage{TokensIn: 5, TokensOut: 2, CostUSD: 0.05}
	if err := st.RecordLLMUsage(ctx, "fixture", wakeID, ledgerTestDay, before); err != nil {
		t.Fatalf("seeding usage: %v", err)
	}

	err := st.RecordLLMUsage(ctx, "fixture", wakeID+9999, ledgerTestDay, store.LLMUsage{TokensIn: 100, TokensOut: 50, CostUSD: 1})
	if err == nil {
		t.Fatal("RecordLLMUsage with a missing wake returned nil error")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("RecordLLMUsage missing wake error = %v, want wrapped sql.ErrNoRows", err)
	}

	ledgerTotals, err := st.LLMUsageForDay(ctx, "fixture", ledgerTestDay)
	if err != nil {
		t.Fatalf("LLMUsageForDay: %v", err)
	}
	assertUsage(t, ledgerTotals, before)
	assertUsage(t, wakeUsage(t, st, wakeID), before)
}

func TestRecordLLMUsage_WrongAgentWakeLeavesTotalsUnchanged(t *testing.T) {
	ctx := context.Background()
	st := openLedgerTestStore(t)
	wakeID := insertLedgerTestWake(t, st, "agent-a")
	before := store.LLMUsage{TokensIn: 5, TokensOut: 2, CostUSD: 0.05}
	if err := st.RecordLLMUsage(ctx, "agent-a", wakeID, ledgerTestDay, before); err != nil {
		t.Fatalf("seeding usage: %v", err)
	}

	if err := st.RecordLLMUsage(ctx, "agent-b", wakeID, ledgerTestDay, store.LLMUsage{TokensIn: 100, TokensOut: 50, CostUSD: 1}); err == nil {
		t.Fatal("RecordLLMUsage with another agent's wake returned nil error")
	}

	agentATotals, err := st.LLMUsageForDay(ctx, "agent-a", ledgerTestDay)
	if err != nil {
		t.Fatalf("LLMUsageForDay(agent-a): %v", err)
	}
	assertUsage(t, agentATotals, before)
	assertUsage(t, wakeUsage(t, st, wakeID), before)

	agentBTotals, err := st.LLMUsageForDay(ctx, "agent-b", ledgerTestDay)
	if err != nil {
		t.Fatalf("LLMUsageForDay(agent-b): %v", err)
	}
	assertUsage(t, agentBTotals, store.LLMUsage{})
}

func TestRecordLLMUsage_RollsBackWhenLedgerWriteFails(t *testing.T) {
	ctx := context.Background()
	st := openLedgerTestStore(t)
	wakeID := insertLedgerTestWake(t, st, "fixture")
	if _, err := st.DB().ExecContext(ctx, `
		CREATE TRIGGER reject_ledger_write BEFORE INSERT ON ledger_phases
		BEGIN
			SELECT RAISE(ABORT, 'ledger write rejected by test trigger');
		END
	`); err != nil {
		t.Fatalf("creating ledger failure trigger: %v", err)
	}

	err := st.RecordLLMUsage(ctx, "fixture", wakeID, ledgerTestDay, store.LLMUsage{TokensIn: 10, TokensOut: 5, CostUSD: 0.1})
	if err == nil {
		t.Fatal("RecordLLMUsage returned nil error while the ledger write was failing")
	}

	ledgerTotals, err := st.LLMUsageForDay(ctx, "fixture", ledgerTestDay)
	if err != nil {
		t.Fatalf("LLMUsageForDay: %v", err)
	}
	assertUsage(t, ledgerTotals, store.LLMUsage{})
	assertUsage(t, wakeUsage(t, st, wakeID), store.LLMUsage{})
}

func TestRecordLLMUsage_RollsBackWhenWakeWriteFails(t *testing.T) {
	ctx := context.Background()
	st := openLedgerTestStore(t)
	wakeID := insertLedgerTestWake(t, st, "fixture")
	if _, err := st.DB().ExecContext(ctx, `
		CREATE TRIGGER reject_wake_update BEFORE UPDATE ON wakes
		BEGIN
			SELECT RAISE(ABORT, 'wake write rejected by test trigger');
		END
	`); err != nil {
		t.Fatalf("creating wake failure trigger: %v", err)
	}

	err := st.RecordLLMUsage(ctx, "fixture", wakeID, ledgerTestDay, store.LLMUsage{TokensIn: 10, TokensOut: 5, CostUSD: 0.1})
	if err == nil {
		t.Fatal("RecordLLMUsage returned nil error while the wake write was failing")
	}

	ledgerTotals, err := st.LLMUsageForDay(ctx, "fixture", ledgerTestDay)
	if err != nil {
		t.Fatalf("LLMUsageForDay: %v", err)
	}
	assertUsage(t, ledgerTotals, store.LLMUsage{})
	assertUsage(t, wakeUsage(t, st, wakeID), store.LLMUsage{})
}

func TestRecordLLMUsage_RejectsInvalidUsage(t *testing.T) {
	ctx := context.Background()
	st := openLedgerTestStore(t)
	wakeID := insertLedgerTestWake(t, st, "fixture")

	cases := []struct {
		name  string
		usage store.LLMUsage
	}{
		{"negative tokens in", store.LLMUsage{TokensIn: -1}},
		{"negative tokens out", store.LLMUsage{TokensOut: -1}},
		{"negative cost", store.LLMUsage{CostUSD: -0.01}},
		{"NaN cost", store.LLMUsage{CostUSD: math.NaN()}},
		{"positive infinite cost", store.LLMUsage{CostUSD: math.Inf(1)}},
		{"negative infinite cost", store.LLMUsage{CostUSD: math.Inf(-1)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := st.RecordLLMUsage(ctx, "fixture", wakeID, ledgerTestDay, tc.usage); err == nil {
				t.Fatalf("RecordLLMUsage(%+v) returned nil error, want validation error", tc.usage)
			}
		})
	}

	ledgerTotals, err := st.LLMUsageForDay(ctx, "fixture", ledgerTestDay)
	if err != nil {
		t.Fatalf("LLMUsageForDay: %v", err)
	}
	assertUsage(t, ledgerTotals, store.LLMUsage{})
	assertUsage(t, wakeUsage(t, st, wakeID), store.LLMUsage{})
}

func TestRecordLLMUsage_CancelledContext(t *testing.T) {
	st := openLedgerTestStore(t)
	wakeID := insertLedgerTestWake(t, st, "fixture")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := st.RecordLLMUsage(ctx, "fixture", wakeID, ledgerTestDay, store.LLMUsage{TokensIn: 10, TokensOut: 5, CostUSD: 0.1})
	if err == nil {
		t.Fatal("RecordLLMUsage with a cancelled context returned nil error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RecordLLMUsage cancelled context error = %v, want context.Canceled", err)
	}

	ledgerTotals, err := st.LLMUsageForDay(context.Background(), "fixture", ledgerTestDay)
	if err != nil {
		t.Fatalf("LLMUsageForDay: %v", err)
	}
	assertUsage(t, ledgerTotals, store.LLMUsage{})
	assertUsage(t, wakeUsage(t, st, wakeID), store.LLMUsage{})
}

func TestLLMUsageForDay_CancelledContext(t *testing.T) {
	st := openLedgerTestStore(t)
	wakeID := insertLedgerTestWake(t, st, "fixture")
	if err := st.RecordLLMUsage(context.Background(), "fixture", wakeID, ledgerTestDay, store.LLMUsage{TokensIn: 10, TokensOut: 5, CostUSD: 0.1}); err != nil {
		t.Fatalf("seeding usage: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := st.LLMUsageForDay(ctx, "fixture", ledgerTestDay); err == nil {
		t.Fatal("LLMUsageForDay with a cancelled context returned nil error")
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("LLMUsageForDay cancelled context error = %v, want context.Canceled", err)
	}
}

func TestCriticSpendHasItsOwnLedgerPhase(t *testing.T) {
	st := openLedgerTestStore(t)
	ctx := context.Background()
	if _, err := st.DB().Exec(`INSERT INTO agents(id,status) VALUES ('a','running')`); err != nil {
		t.Fatal(err)
	}
	wake := insertLedgerTestWake(t, st, "a")
	if err := st.RecordLLMUsage(ctx, "a", wake, ledgerTestDay, store.LLMUsage{TokensIn: 10, TokensOut: 5, CostUSD: 0.5}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordLLMUsage(ctx, "a", wake, ledgerTestDay, store.LLMUsage{TokensIn: 4, TokensOut: 2, CostUSD: 0.25, Phase: "critic"}); err != nil {
		t.Fatal(err)
	}
	phases, err := st.LLMUsageByPhaseForDay(ctx, "a", ledgerTestDay)
	if err != nil || phases["critic"].CostUSD != 0.25 || phases["agent"].CostUSD != 0.5 {
		t.Fatalf("phases %+v %v", phases, err)
	}
	// Critic spend counts toward the daily total and the wake's own cost.
	if total, _ := st.LLMUsageForDay(ctx, "a", ledgerTestDay); total.CostUSD != 0.75 {
		t.Fatalf("total %+v", total)
	}
	if got := wakeUsage(t, st, wake); got.CostUSD != 0.75 {
		t.Fatalf("wake %+v", got)
	}
}
