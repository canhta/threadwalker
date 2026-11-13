package dashboard

import (
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
)

func TestHistoryRangesNeverExceedRetention(t *testing.T) {
	for _, table := range []string{"wakes", "tool_calls", "trend_stories"} {
		age, ok := store.RetentionAge(table)
		if !ok {
			t.Fatalf("%s has no retention", table)
		}
		keep := int(age / (24 * time.Hour))
		for _, days := range rangeDays {
			got := historyDays(table, days)
			if got > keep {
				t.Errorf("%s shows %d days, keeps %d", table, got, keep)
			}
			if got < days && keep >= days {
				t.Errorf("%s clamped %d to %d though it keeps %d", table, days, got, keep)
			}
		}
	}
	if historyDays("trend_stories", 30) != 7 || historyDays("ledger", 30) != 30 {
		t.Fatal("trend history must clamp to 7 days and unpruned tables must not")
	}
}
