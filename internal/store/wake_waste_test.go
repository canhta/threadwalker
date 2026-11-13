package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
)

func TestMetricDeferralsCountAndEndAtTheCap(t *testing.T) {
	s := metricCoreStore(t)
	ctx := context.Background()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	metricCoreAction(t, s, "a", at.Format(time.RFC3339Nano), "https://example.test/own", false)
	if _, err := s.SeedMetricJobs(ctx, "a", at); err != nil {
		t.Fatal(err)
	}
	now := at.Add(time.Hour)
	for i := 1; i <= store.MaxMetricAttempts; i++ {
		jobs, err := s.ClaimMetricJobs(ctx, "a", now, time.Minute, 1)
		if err != nil || len(jobs) != 1 || jobs[0].Attempts != max(i-2, 0) {
			t.Fatalf("claim %d: %+v %v", i, jobs, err)
		}
		reason := "read_failed"
		if i == 1 {
			reason = "source_unavailable" // not the read's fault: not counted
		}
		if err = s.DeferMetricJobs(ctx, jobs, now, now.Add(time.Second), reason); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	var open int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM metric_jobs WHERE window_hours=1 AND completed_at IS NULL`).Scan(&open); err != nil || open != 1 {
		t.Fatalf("the uncounted deferral should leave the job open once more: open=%d err=%v", open, err)
	}
	jobs, err := s.ClaimMetricJobs(ctx, "a", now, time.Minute, 1)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim: %+v %v", jobs, err)
	}
	if err = s.DeferMetricJobs(ctx, jobs, now, now.Add(time.Second), "read_failed"); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err = s.DB().QueryRow(`SELECT reason FROM metric_jobs WHERE window_hours=1 AND completed_at IS NOT NULL`).Scan(&reason); err != nil || reason != "read_failed" {
		t.Fatalf("job should end with its last reason: %q %v", reason, err)
	}
}

func TestOwnerNoteStaysOpenUntilNamedOrExpired(t *testing.T) {
	s := metricCoreStore(t)
	ctx := context.Background()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.OwnerNote(ctx, []string{"a"}, "k1", "post about the AP story", at); err != nil {
		t.Fatal(err)
	}
	open := func(now time.Time) int {
		notes, err := s.OpenOwnerNotes(ctx, "a", now)
		if err != nil {
			t.Fatal(err)
		}
		return len(notes)
	}
	if n := open(at.Add(47 * time.Hour)); n != 1 {
		t.Fatalf("open after 47h = %d", n)
	}
	if n := open(at.Add(49 * time.Hour)); n != 0 {
		t.Fatalf("open after 49h = %d", n)
	}
	notes, _ := s.OpenOwnerNotes(ctx, "a", at)
	if _, err := s.DB().Exec(`INSERT INTO actions(agent_id,kind,text,posted_at,answers_note) VALUES('a','post','x',?,?)`, at.Format(time.RFC3339Nano), notes[0].ID); err != nil {
		t.Fatal(err)
	}
	if n := open(at.Add(time.Hour)); n != 0 {
		t.Fatalf("open after an action named it = %d", n)
	}
}

func TestRefusedToolsExpireAfterADay(t *testing.T) {
	s := metricCoreStore(t)
	ctx := context.Background()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.RefuseTool(ctx, "a", "search", at); err != nil {
		t.Fatal(err)
	}
	if got, err := s.RefusedTools(ctx, "a", at.Add(23*time.Hour)); err != nil || len(got) != 1 || got[0] != "search" {
		t.Fatalf("refused = %v %v", got, err)
	}
	if got, err := s.RefusedTools(ctx, "a", at.Add(25*time.Hour)); err != nil || len(got) != 0 {
		t.Fatalf("refused after a day = %v %v", got, err)
	}
}
