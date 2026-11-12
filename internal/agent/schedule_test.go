package agent_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/agent"
	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/store"
)

func mustPhaseSchedule(t *testing.T, schedule config.ScheduleConfig) *agent.PhaseSchedule {
	t.Helper()
	s, err := agent.NewPhaseSchedule(schedule)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDreamDueUsesLocalZoneAndRunsOncePerLocalDay(t *testing.T) {
	s := mustPhaseSchedule(t, config.ScheduleConfig{Timezone: "Asia/Ho_Chi_Minh", DreamAt: "23:30"})
	// 15:00 UTC is 22:00 in Ho Chi Minh City: dream is later today, local.
	now := time.Date(2026, 1, 1, 15, 0, 0, 0, time.UTC)
	due, key, ok := s.DreamDue(now, time.Time{})
	if !ok || key != "2026-01-01" || !due.Equal(time.Date(2026, 1, 1, 23, 30, 0, 0, time.FixedZone("ict", 7*3600))) {
		t.Fatalf("due=%v key=%s ok=%v", due, key, ok)
	}
	// A fresh agent waits for its first occurrence instead of dreaming at once.
	now = time.Date(2026, 1, 2, 0, 10, 0, 0, time.UTC) // 07:10 local Jan 2
	due, key, ok = s.DreamDue(now, time.Time{})
	if !ok || key != "2026-01-02" || !due.After(now) {
		t.Fatalf("fresh dream: due=%v key=%s ok=%v", due, key, ok)
	}
	// A missed dream after downtime is due once, not skipped.
	lastRun := time.Date(2025, 12, 30, 23, 30, 0, 0, time.FixedZone("ict", 7*3600))
	due, key, ok = s.DreamDue(now, lastRun)
	if !ok || key != "2026-01-01" || due.After(now) {
		t.Fatalf("missed dream: due=%v key=%s ok=%v", due, key, ok)
	}
	// After that dream ran, the same local day cannot run again.
	last := time.Date(2026, 1, 1, 23, 40, 0, 0, time.FixedZone("ict", 7*3600))
	due, key, ok = s.DreamDue(now, last)
	if !ok || key != "2026-01-02" || !due.After(now) {
		t.Fatalf("repeat dream: due=%v key=%s ok=%v", due, key, ok)
	}
}

func TestStudyDueUsesLocalSlotsAndCollapsesMissedSlots(t *testing.T) {
	s := mustPhaseSchedule(t, config.ScheduleConfig{Timezone: "Asia/Ho_Chi_Minh", StudySlots: []string{"08:00", "13:00"}})
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) // 07:00 local
	due, key, ok := s.StudyDue(now, time.Time{})
	if !ok || key != "2026-01-01T08:00" || !due.Equal(time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)) {
		t.Fatalf("next slot: due=%v key=%s ok=%v", due, key, ok)
	}
	// 08:10 local: the 08:00 slot is due (one catch-up) and the next is 13:00.
	now = time.Date(2026, 1, 1, 1, 10, 0, 0, time.UTC)
	due, key, ok = s.StudyDue(now, time.Time{})
	if !ok || key != "2026-01-01T08:00" || due.After(now) {
		t.Fatalf("missed slot: due=%v key=%s ok=%v", due, key, ok)
	}
	last := time.Date(2026, 1, 1, 1, 15, 0, 0, time.UTC)
	due, key, ok = s.StudyDue(now.Add(2*time.Hour), last)
	if !ok || key != "2026-01-01T13:00" || !due.Equal(time.Date(2026, 1, 1, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("next slot after study: due=%v key=%s ok=%v", due, key, ok)
	}
}

func TestPhaseScheduleDisabledAndInvalid(t *testing.T) {
	s := mustPhaseSchedule(t, config.ScheduleConfig{})
	if s.DreamEnabled() || s.StudyEnabled() {
		t.Fatal("empty schedule should disable both phases")
	}
	if _, err := agent.NewPhaseSchedule(config.ScheduleConfig{DreamAt: "25:00"}); err == nil {
		t.Fatal("invalid dream time accepted")
	}
	if _, err := agent.NewPhaseSchedule(config.ScheduleConfig{Timezone: "Not/AZone"}); err == nil {
		t.Fatal("invalid zone accepted")
	}
}

func TestDreamRunsOnceAcrossRestartAndDoesNotMoveNextWake(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "dream.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 1, 1, 23, 45, 0, 0, time.FixedZone("ict", 7*3600))
	clock := func() time.Time { return now }
	tmpl := agent.OfflineTemplate()
	tmpl.Schedule = config.ScheduleConfig{Timezone: "Asia/Ho_Chi_Minh", DreamAt: "23:30"}
	tmpl.Limits.MaxUSDPerDream = 0.30
	tmpl.Limits.MaxStepsPerDream = 10
	before := now.Add(2 * time.Hour)
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	if err = st.SetNextWake(ctx, tmpl.ID, before); err != nil {
		t.Fatal(err)
	}
	// A prior night's dream makes today's occurrence due, even at 23:45.
	if _, err = st.DB().Exec(`INSERT INTO wakes(agent_id,started_at,ended_at,end_reason,kind,phase_key) VALUES(?,?,'completed','2025-12-31T16:30:00Z','dream','2025-12-31')`, tmpl.ID, "2025-12-31T16:30:00Z"); err != nil {
		t.Fatal(err)
	}
	makeRuntime := func() *agent.Runtime {
		fixture := agent.NewOfflineFixture(clock)
		runtime, newErr := agent.New(agent.Options{Store: st, Template: tmpl, Now: clock, HTTPClient: fixture.Client()})
		if newErr != nil {
			t.Fatal(newErr)
		}
		if startErr := runtime.Start(ctx); startErr != nil {
			t.Fatal(startErr)
		}
		return runtime
	}
	r := makeRuntime()
	due, kind, key, ok, err := r.PhaseDue(ctx, now)
	if err != nil || !ok || kind != store.WakeDream || !due.Equal(now.Add(-15*time.Minute)) {
		t.Fatalf("phase due=%v kind=%s key=%s ok=%v err=%v", due, kind, key, ok, err)
	}
	wake, err := r.RunPhase(ctx, kind, key)
	if err != nil || wake.EndReason != "completed" {
		t.Fatalf("dream wake=%+v err=%v", wake, err)
	}
	// A duplicate occurrence is a no-op even across a restart.
	if _, err = r.RunPhase(ctx, kind, key); err != nil {
		t.Fatalf("duplicate phase: %v", err)
	}
	r2 := makeRuntime()
	if _, err = r2.RunPhase(ctx, kind, key); err != nil {
		t.Fatalf("duplicate phase after restart: %v", err)
	}
	var count int
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM wakes WHERE agent_id=? AND kind='dream' AND phase_key='2026-01-01'`, tmpl.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("dream wakes=%d err=%v", count, err)
	}
	state, err := st.AgentState(ctx, tmpl.ID)
	if err != nil || !state.NextWake.Equal(before) {
		t.Fatalf("next wake moved: %v err=%v", state.NextWake, err)
	}
	// Same local day: no second dream.
	now = now.Add(5 * time.Minute)
	due, _, _, ok, err = r2.PhaseDue(ctx, now)
	if err != nil || ok && due.Before(now) {
		t.Fatalf("dream repeated same day: due=%v ok=%v err=%v", due, ok, err)
	}
}
