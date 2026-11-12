package dashboard_test

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/dashboard"
	"github.com/canhta/threadwalker/internal/store"
)

func TestDashboardShowsWakeStudyAndDreamSpendSeparately(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "phase-page.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	if err = st.EnsureAgent(ctx, "viral"); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		phase string
		cost  float64
	}{{"agent", 0.4}, {"study", 0.2}, {"dream", 0.1}} {
		if _, err = st.DB().Exec(`INSERT INTO ledger(agent_id,day,cost_usd) VALUES('viral',?,?) ON CONFLICT(agent_id,day) DO UPDATE SET cost_usd=ledger.cost_usd+excluded.cost_usd`, now.Format("2006-01-02"), row.cost); err != nil {
			t.Fatal(err)
		}
		if _, err = st.DB().Exec(`INSERT INTO ledger_phases(agent_id,day,phase,cost_usd) VALUES('viral',?,?,?)`, now.Format("2006-01-02"), row.phase, row.cost); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = st.DB().Exec(`INSERT INTO wakes(agent_id,started_at,ended_at,end_reason,kind) VALUES('viral',?,?,'completed','dream')`, now.Format(time.RFC3339), now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	h, err := dashboard.New(dashboard.Options{DB: st.DB(), Now: func() time.Time { return now }, Agents: []dashboard.Agent{{ID: "viral", DailyBudgetUSD: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := h.Page(ctx)
	if err != nil {
		t.Fatal(err)
	}
	view := page.Agents[0]
	if math.Abs(view.SpentUSD-0.7) > 1e-9 || math.Abs(view.WakeUSD-0.6) > 1e-9 || math.Abs(view.StudyUSD-0.2) > 1e-9 || math.Abs(view.DreamUSD-0.1) > 1e-9 {
		t.Fatalf("phase split=%+v", view)
	}
	if math.Abs(view.RemainingUSD-1.3) > 1e-9 {
		t.Fatalf("remaining=%v", view.RemainingUSD)
	}
	if len(view.Wakes) != 1 || view.Wakes[0].Kind != "dream" {
		t.Fatalf("wake kind=%+v", view.Wakes)
	}
}
