package agent_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/store"
)

func TestTwoAgentLogical48HourSoak(t *testing.T) {
	e, err := agent.RunIsolationSoak(context.Background(), filepath.Join(t.TempDir(), "isolation.db"), 48, nil)
	if err != nil {
		t.Fatal(err)
	}
	if e.LogicalHours != 48 || !e.Synthetic || e.Restarts != 1 || !e.Serial || !e.StateIsolated || !e.BudgetIsolated || !e.ContextIsolated || e.MaxActiveBackends != 1 {
		t.Fatalf("bad isolation evidence: %+v", e)
	}
	if len(e.Agents) != 2 || len(e.Memory) < 5 || e.Agents[0].PausedSkips != 4 || e.Agents[1].BudgetDeferrals == 0 || e.Agents[0].ActionLimitStops == 0 || e.Agents[1].KilledSkips != 12 {
		t.Fatalf("missing exercised boundary: %+v", e)
	}
	for _, a := range e.Agents {
		if a.CompletedWakes == 0 || a.CostUSD <= 0 || a.DryRunActions == 0 {
			t.Fatalf("agent did not execute: %+v", a)
		}
	}
}

func TestIsolationSoakRejectsInvalidBoundsAndExistingState(t *testing.T) {
	for _, tc := range []struct {
		path  string
		hours int
	}{{"", 48}, {":memory:", 48}, {filepath.Join(t.TempDir(), "a.db"), 47}, {filepath.Join(t.TempDir(), "b.db"), 721}} {
		if _, err := agent.RunIsolationSoak(context.Background(), tc.path, tc.hours, nil); err == nil {
			t.Fatal("invalid soak accepted")
		}
	}
	path := filepath.Join(t.TempDir(), "occupied.db")
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.EnsureAgent(context.Background(), "operator-agent"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if _, err = agent.RunIsolationSoak(context.Background(), path, 48, nil); err == nil {
		t.Fatal("existing operator state accepted")
	}
}
