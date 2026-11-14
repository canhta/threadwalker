package agent

import (
	"errors"
	"time"

	"github.com/canhta/threadz/internal/config"
)

// PhaseSchedule turns the template's local-time dream and quiet-window settings
// into due times. It is pure: callers inject the clock, and no network or DB is touched.
type PhaseSchedule struct {
	loc     *time.Location
	dreamAt string
	// quietFrom/quietUntil are the local quiet window; empty is none.
	quietFrom, quietUntil string
}

// NewPhaseSchedule validates schedule fields. An empty dream time disables the
// dream; it never falls back to a global flag.
func NewPhaseSchedule(schedule config.ScheduleConfig) (*PhaseSchedule, error) {
	zone := schedule.Timezone
	if zone == "" {
		zone = "Asia/Ho_Chi_Minh"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, errors.New("configuring phases: invalid schedule timezone")
	}
	s := &PhaseSchedule{loc: loc, quietFrom: schedule.QuietFrom, quietUntil: schedule.QuietUntil}
	if schedule.DreamAt != "" {
		if _, err := time.Parse("15:04", schedule.DreamAt); err != nil {
			return nil, errors.New("configuring phases: invalid dream time")
		}
		s.dreamAt = schedule.DreamAt
	}
	return s, nil
}

// NextQuietFrom is the next start of the quiet window at or after at; false
// when there is no window.
func (s *PhaseSchedule) NextQuietFrom(at time.Time) (time.Time, bool) {
	if s == nil || s.quietFrom == "" {
		return time.Time{}, false
	}
	local := at.In(s.loc)
	h, m := clock(s.quietFrom)
	from := time.Date(local.Year(), local.Month(), local.Day(), h, m, 0, 0, s.loc)
	if from.Before(local) {
		from = time.Date(local.Year(), local.Month(), local.Day()+1, h, m, 0, 0, s.loc)
	}
	return from, true
}

// AfterQuiet returns at, or the end of the local quiet window when at falls
// inside it. A window may cross midnight (01:00-07:00 or 23:00-07:00).
func (s *PhaseSchedule) AfterQuiet(at time.Time) time.Time {
	if s == nil || s.quietFrom == "" {
		return at
	}
	local := at.In(s.loc)
	day := func(offset int, hhmm string) time.Time {
		h, m := clock(hhmm)
		d := local.AddDate(0, 0, offset)
		return time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, s.loc)
	}
	for _, offset := range []int{-1, 0} {
		from := day(offset, s.quietFrom)
		until := day(offset, s.quietUntil)
		if !until.After(from) {
			until = until.AddDate(0, 0, 1)
		}
		if !local.Before(from) && local.Before(until) {
			return until
		}
	}
	return at
}

// DreamEnabled reports whether the template configures a dream time.
func (s *PhaseSchedule) DreamEnabled() bool { return s != nil && s.dreamAt != "" }

// DreamDue returns the next dream instant and its once-per-local-day key. The
// returned instant may be in the past (a missed dream runs once, never twice).
func (s *PhaseSchedule) DreamDue(now, last time.Time) (time.Time, string, bool) {
	if !s.DreamEnabled() {
		return time.Time{}, "", false
	}
	localNow := now.In(s.loc)
	hour, minute := clock(s.dreamAt)
	today := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), hour, minute, 0, 0, s.loc)
	next := today
	if !localNow.Before(today) {
		next = today.AddDate(0, 0, 1)
	}
	if last.IsZero() {
		// First run on a fresh agent waits for the next occurrence; there is no
		// evidence a night was missed.
		return next, s.localDay(next), true
	}
	latest := today
	if localNow.Before(today) {
		latest = today.AddDate(0, 0, -1)
	}
	occurrence := latest
	if s.localDay(last) >= s.localDay(latest) {
		occurrence = next
	}
	return occurrence, s.localDay(occurrence), true
}

func (s *PhaseSchedule) localDay(at time.Time) string {
	return at.In(s.loc).Format("2006-01-02")
}

func clock(value string) (int, int) {
	parsed, _ := time.Parse("15:04", value)
	return parsed.Hour(), parsed.Minute()
}
