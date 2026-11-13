package store_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestSetNextWake_PersistsUTCAndPreservesAgentFields(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		at   time.Time
	}{
		{"UTC", time.Date(2026, 11, 12, 9, 30, 0, 123456789, time.UTC)},
		{"non-UTC zone", time.Date(2026, 11, 12, 16, 30, 0, 123456789, time.FixedZone("ICT", 7*60*60))},
		{"past time is not clamped", base.Add(-48 * time.Hour)},
		{"beyond 24 hours is not clamped", base.Add(72 * time.Hour)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := openNotesTestStore(t)
			insertNotesTestAgent(t, st, "agent-a")
			if _, err := st.DB().ExecContext(ctx, `
				UPDATE agents SET next_wake_at = ? WHERE id = ?
			`, "2026-11-11T00:00:00Z", "agent-a"); err != nil {
				t.Fatalf("seeding agent fields: %v", err)
			}

			if err := st.SetNextWake(ctx, "agent-a", tc.at); err != nil {
				t.Fatalf("SetNextWake: %v", err)
			}

			var raw, status string
			if err := st.DB().QueryRowContext(ctx,
				`SELECT next_wake_at, status FROM agents WHERE id = ?`, "agent-a").
				Scan(&raw, &status); err != nil {
				t.Fatalf("reading agent: %v", err)
			}
			wantRaw := tc.at.UTC().Format(time.RFC3339Nano)
			if raw != wantRaw {
				t.Fatalf("next_wake_at = %q, want %q", raw, wantRaw)
			}
			parsed, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				t.Fatalf("parsing stored next_wake_at: %v", err)
			}
			if !parsed.Equal(tc.at) || parsed.Location() != time.UTC {
				t.Fatalf("next_wake_at parses as %v in %v, want %v in UTC", parsed, parsed.Location(), tc.at)
			}
			if status != "running" {
				t.Fatalf("status = %q, want running", status)
			}
		})
	}
}

func TestSetNextWake_MissingAgentErrors(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)

	err := st.SetNextWake(ctx, "missing-agent", time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("SetNextWake for missing agent error = %v, want sql.ErrNoRows", err)
	}
	if got := countRows(t, st, `SELECT COUNT(*) FROM agents`); got != 0 {
		t.Fatalf("agents rows = %d, want 0 after failed schedule", got)
	}
}

func TestSetNextWake_RejectsCancelledContextAndClosedDB(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "agent-a")

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	at := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	if err := st.SetNextWake(cancelled, "agent-a", at); !errors.Is(err, context.Canceled) {
		t.Fatalf("SetNextWake with cancelled context error = %v, want context.Canceled", err)
	}
	var raw string
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COALESCE(next_wake_at, '') FROM agents WHERE id = ?`, "agent-a").Scan(&raw); err != nil {
		t.Fatalf("reading next_wake_at after cancellation: %v", err)
	}
	if raw != "" {
		t.Fatalf("next_wake_at = %q, want unchanged empty value after cancellation", raw)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("closing store: %v", err)
	}
	if err := st.SetNextWake(ctx, "agent-a", at); err == nil {
		t.Fatal("SetNextWake on closed store succeeded, want an error")
	}
}
