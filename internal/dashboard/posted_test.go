package dashboard_test

import (
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/store"
)

func TestPostedCountHasNoRowCap(t *testing.T) {
	snap := started()
	for i := 0; i < 600; i++ {
		snap.Publications = append(snap.Publications, pub(int64(600-i), "post", ago(0), ownLink(idCode("P", i))))
	}
	page := mustPage(t, newHandler(newSource(snap)))
	if page.KPIs[3].Value != "600" {
		t.Fatalf("posted count capped: %+v", page.KPIs[3])
	}
}

func TestDraftsAreNotShown(t *testing.T) {
	snap := started()
	snap.Publications = []store.DashboardPublication{pub(1, "post", ago(0), ownLink("REAL1"))}
	snap.Drafts = 1
	h := newHandler(newSource(snap))
	page := mustPage(t, h)
	if page.KPIs[3].Value != "1" || page.KPIs[3].Note != "" || strings.Contains(get(h, "/").Body.String(), "draft") {
		t.Fatalf("a draft shows on the page: %+v", page.KPIs[3])
	}
}

func TestUnknownSpendPhaseFailsTheRead(t *testing.T) {
	snap := started()
	snap.Phases = []store.DashboardPhaseSpend{{Day: "2026-11-12", Phase: "mystery", CostUSD: 0.5}}
	h := newHandler(newSource(snap))
	if _, err := h.Page(t.Context()); err == nil {
		t.Fatal("unknown phase was accepted")
	}
	rr := get(h, "/")
	if rr.Code != 503 || strings.Contains(rr.Body.String(), "mystery") {
		t.Fatalf("unknown phase response: %d %s", rr.Code, rr.Body.String())
	}
}
