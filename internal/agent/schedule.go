package agent

import (
	"errors"
	"sort"
	"time"

	"github.com/canhta/threadz/internal/config"
)

// PhaseSchedule turns the template's local-time dream/study settings into due
// times. It is pure: callers inject the clock, and no network or DB is touched.
type PhaseSchedule struct {
	loc        *time.Location
	dreamAt    string
	studySlots []string
	// quietFrom/quietUntil are the local quiet window; empty is none.
	quietFrom, quietUntil string
}

// NewPhaseSchedule validates schedule fields. Empty dream/study settings disable
// that phase; they never fall back to a global flag.
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
	for _, slot := range schedule.StudySlots {
		if _, err := time.Parse("15:04", slot); err != nil {
			return nil, errors.New("configuring phases: invalid study slot")
		}
		s.studySlots = append(s.studySlots, slot)
	}
	sort.Strings(s.studySlots)
	return s, nil
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

// StudyEnabled reports whether the template configures study slots.
func (s *PhaseSchedule) StudyEnabled() bool { return s != nil && len(s.studySlots) > 0 }

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

// StudyDue returns the next study slot and its local occurrence key. Slots are
// local wall-clock times; a missed slot collapses into one catch-up run.
func (s *PhaseSchedule) StudyDue(now, last time.Time) (time.Time, string, bool) {
	if !s.StudyEnabled() {
		return time.Time{}, "", false
	}
	localNow := now.In(s.loc)
	var candidates []time.Time
	if last.IsZero() {
		for _, slot := range s.studySlots {
			hour, minute := clock(slot)
			candidates = append(candidates, time.Date(localNow.Year(), localNow.Month(), localNow.Day(), hour, minute, 0, 0, s.loc))
		}
		picked := candidates[0]
		for _, candidate := range candidates {
			if !candidate.After(localNow) {
				picked = candidate
			}
		}
		return picked, s.phaseKey("study", picked), true
	}
	for _, dayOffset := range []int{-1, 0, 1} {
		day := localNow.AddDate(0, 0, dayOffset)
		for _, slot := range s.studySlots {
			hour, minute := clock(slot)
			candidate := time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, s.loc)
			if candidate.After(last) {
				candidates = append(candidates, candidate)
			}
		}
	}
	for _, candidate := range candidates {
		if candidate.After(last) {
			return candidate, s.phaseKey("study", candidate), true
		}
	}
	next := localNow.AddDate(0, 0, 1)
	hour, minute := clock(s.studySlots[0])
	candidate := time.Date(next.Year(), next.Month(), next.Day(), hour, minute, 0, 0, s.loc)
	return candidate, s.phaseKey("study", candidate), true
}

func (s *PhaseSchedule) localDay(at time.Time) string {
	return at.In(s.loc).Format("2006-01-02")
}

func (s *PhaseSchedule) phaseKey(kind string, at time.Time) string {
	if kind == "dream" {
		return s.localDay(at)
	}
	return s.localDay(at) + "T" + at.In(s.loc).Format("15:04")
}

func clock(value string) (int, int) {
	parsed, _ := time.Parse("15:04", value)
	return parsed.Hour(), parsed.Minute()
}
