package config

import (
	"testing"
	"time"
)

// 16:30Z and 17:30Z are on one UTC day but fall at 23:30 and 00:30 at UTC+7.
func TestSpendDayIsTheLocalCalendarDay(t *testing.T) {
	l := LimitsConfig{Timezone: "Asia/Ho_Chi_Minh"}
	late := time.Date(2026, 1, 1, 16, 30, 0, 0, time.UTC)
	early := time.Date(2026, 1, 1, 17, 30, 0, 0, time.UTC)
	if l.Day(late) != "2026-01-01" || l.Day(early) != "2026-01-02" {
		t.Fatalf("days %s %s", l.Day(late), l.Day(early))
	}
	start, end, _ := l.DayBounds(early)
	if !start.Equal(time.Date(2026, 1, 1, 17, 0, 0, 0, time.UTC)) || !end.Equal(time.Date(2026, 1, 2, 17, 0, 0, 0, time.UTC)) {
		t.Fatalf("bounds %v %v", start, end)
	}
	if (LimitsConfig{}).Day(early) != "2026-01-01" {
		t.Fatal("empty zone is UTC")
	}
}
