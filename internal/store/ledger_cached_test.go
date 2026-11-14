package store_test

import (
	"context"
	"testing"

	"github.com/canhta/threadz/internal/store"
)

func TestRecordLLMUsage_StoresAndSumsCachedTokens(t *testing.T) {
	ctx := context.Background()
	st := openLedgerTestStore(t)
	wakeID := insertLedgerTestWake(t, st, "fixture")
	for _, usage := range []store.LLMUsage{{TokensIn: 100, TokensCached: 60, TokensOut: 5, CostUSD: 0.1}, {TokensIn: 50, TokensCached: 10, CostUSD: 0.1}} {
		if err := st.RecordLLMUsage(ctx, "fixture", wakeID, ledgerTestDay, usage); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.LLMUsageForDay(ctx, "fixture", ledgerTestDay)
	if err != nil || got.TokensCached != 70 || got.TokensIn != 150 {
		t.Fatalf("day usage %+v %v", got, err)
	}
	phase, err := st.LLMUsageForDayPhases(ctx, "fixture", ledgerTestDay, "agent")
	if err != nil || phase.TokensCached != 70 {
		t.Fatalf("phase usage %+v %v", phase, err)
	}
	if err = st.RecordLLMUsage(ctx, "fixture", wakeID, ledgerTestDay, store.LLMUsage{TokensIn: 1, TokensCached: 2}); err == nil {
		t.Fatal("cached tokens above tokens in were accepted")
	}
}
