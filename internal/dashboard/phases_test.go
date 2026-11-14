package dashboard_test

import (
	"context"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/dashboard"
	"github.com/canhta/threadz/internal/store"
)

func TestDashboardShowsSpendAndPlainPhaseWords(t *testing.T) {
	now := fixedNow()
	today := now.Format("2006-01-02")
	snap := started()
	snap.Ledger = []store.DashboardLedgerDay{{Day: today, CostUSD: 0.7}}
	snap.Phases = []store.DashboardPhaseSpend{{Day: today, Phase: "agent", CostUSD: 0.4}, {Day: today, Phase: "dream", CostUSD: 0.1}}
	snap.Wakes = []store.DashboardWake{finishedWake(1, "dream", "completed", 0)}
	h, err := dashboard.New(dashboard.Options{Source: newSource(snap), Now: func() time.Time { return now }, Agents: []dashboard.Agent{{ID: "alpha", DailyBudgetUSD: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := h.Page(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if page.KPIs[5].Value != "$0.70 / $2.00" {
		t.Fatalf("spend kpi=%+v", page.KPIs[5])
	}
	if !page.Burn.HasRows || page.Burn.MultiDay {
		t.Fatalf("burn panel=%+v", page.Burn)
	}
	if page.Burn.PercentText != "35.0% of the $2.00 limit" {
		t.Fatalf("percent=%q", page.Burn.PercentText)
	}
	if page.Burn.PhaseText != "Runs $0.40, Planning $0.10" {
		t.Fatalf("phases=%q", page.Burn.PhaseText)
	}
	if len(page.Wakes.Rows) != 1 || page.Wakes.Rows[0].Kind != "Planning" {
		t.Fatalf("wake kind=%+v", page.Wakes.Rows)
	}
}

func TestSpendWithoutLedgerRowFallsBackToPhases(t *testing.T) {
	today := fixedNow().Format("2006-01-02")
	snap := started()
	snap.Phases = []store.DashboardPhaseSpend{{Day: today, Phase: "agent", CostUSD: 0.3}}
	page := mustPage(t, newHandler(newSource(snap)))
	if page.KPIs[5].Value != "$0.30 / $2.00" {
		t.Fatalf("spend kpi=%+v", page.KPIs[5])
	}
}
