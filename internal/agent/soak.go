package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/canhta/threadwalker/internal/guard"
	"github.com/canhta/threadwalker/internal/store"
	"github.com/canhta/threadwalker/internal/tools"
)

type WakeEvidence struct {
	ID        int64   `json:"id"`
	StartedAt string  `json:"started_at"`
	EndedAt   string  `json:"ended_at"`
	EndReason string  `json:"end_reason"`
	Steps     int     `json:"steps"`
	TokensIn  int64   `json:"tokens_in"`
	TokensOut int64   `json:"tokens_out"`
	CostUSD   float64 `json:"estimated_cost_usd"`
}
type SoakEvidence struct {
	Version          string         `json:"version"`
	Synthetic        bool           `json:"synthetic"`
	TimingKind       string         `json:"timing_kind"`
	LogicalHours     int            `json:"logical_hours"`
	CompletedWakes   int            `json:"completed_wakes"`
	InterruptedWakes int            `json:"interrupted_wakes"`
	DryRunActions    int            `json:"dry_run_actions"`
	Restarts         int            `json:"restarts"`
	Wakes            []WakeEvidence `json:"wakes"`
}

// RunOfflineSoak exercises at least 48 logical hours using the real wire loop,
// guarded tools and SQLite. Its database must be empty; it is synthetic evidence,
// never a measurement of elapsed live runtime. The simulated crash occurs after
// a guarded dry-run post, before wake completion, then reopens the same database.
func RunOfflineSoak(ctx context.Context, path string, hours int, logger *slog.Logger) (SoakEvidence, error) {
	evidence := SoakEvidence{Version: "v1", Synthetic: true, TimingKind: "logical_time", LogicalHours: hours}
	if hours < 48 || hours > 720 || path == ":memory:" {
		return evidence, errors.New("offline soak: file database and 48..720 logical hours required")
	}
	st, err := store.Open(ctx, path)
	if err != nil {
		return evidence, err
	}
	defer func() {
		if st != nil {
			st.Close()
		}
	}()
	var count int
	if err = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM agents`).Scan(&count); err != nil {
		return evidence, err
	}
	if count != 0 {
		return evidence, errors.New("offline soak: isolated empty database required")
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	start := now
	end := start.Add(time.Duration(hours) * time.Hour)
	tmpl := OfflineTemplate()
	// Synthetic stress fixture: one post/hour exercises lifecycle and recovery.
	// Its explicit 24/day quota leaves real example-template limits unchanged.
	tmpl.Limits.PostsPerDay = 24
	fixture := NewOfflineFixture(func() time.Time { return now })
	create := func() (*Runtime, error) {
		return New(Options{Store: st, Template: tmpl, Now: func() time.Time { return now }, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends, Logger: logger})
	}
	runtime, err := create()
	if err != nil {
		return evidence, err
	}
	if err = runtime.Start(ctx); err != nil {
		return evidence, err
	}
	for !now.After(end) {
		if err = ctx.Err(); err != nil {
			return evidence, err
		}
		if evidence.Restarts == 0 && !now.Before(start.Add(time.Duration(hours/2)*time.Hour)) {
			id, err := st.BeginWake(ctx, tmpl.ID, now, now.Add(time.Hour))
			if err != nil {
				return evidence, err
			}
			runtime.event(ctx, "wake.started", "started", "due", id, 0)
			g, err := guard.New(guard.Options{Store: st, Template: tmpl, WakeID: id, Now: func() time.Time { return now }, Logger: logger})
			if err != nil {
				return evidence, err
			}
			d, err := tools.NewDispatcher(g, tools.Options{Store: st, Template: tmpl, WakeID: id, Now: func() time.Time { return now }, Logger: logger, FetchTrends: fixture.Trends})
			if err != nil {
				return evidence, err
			}
			call := SyntheticCall("post", `{"text":"Synthetic crash-boundary dry-run action."}`)
			call.ID = "synthetic-crash"
			if _, err = d.Dispatch(ctx, call); err != nil {
				return evidence, err
			}
			if err = st.Close(); err != nil {
				return evidence, err
			}
			st, err = store.Open(ctx, path)
			if err != nil {
				return evidence, err
			}
			runtime, err = create()
			if err != nil {
				return evidence, err
			}
			if err = runtime.Start(ctx); err != nil {
				return evidence, err
			}
			evidence.Restarts++
			state, err := st.AgentState(ctx, tmpl.ID)
			if err != nil {
				return evidence, err
			}
			if !state.NextWake.After(now) {
				return evidence, errors.New("offline soak: recovery failed to advance scheduling")
			}
			now = state.NextWake
			continue
		}
		if _, err = runtime.Wake(ctx); err != nil {
			return evidence, err
		}
		state, err := st.AgentState(ctx, tmpl.ID)
		if err != nil {
			return evidence, err
		}
		if !state.NextWake.After(now) {
			return evidence, errors.New("offline soak: schedule failed to advance")
		}
		now = state.NextWake
	}
	rows, err := st.DB().QueryContext(ctx, `SELECT id,started_at,ended_at,end_reason,steps,tokens_in,tokens_out,cost_usd FROM wakes ORDER BY id`)
	if err != nil {
		return evidence, err
	}
	for rows.Next() {
		var w WakeEvidence
		if err = rows.Scan(&w.ID, &w.StartedAt, &w.EndedAt, &w.EndReason, &w.Steps, &w.TokensIn, &w.TokensOut, &w.CostUSD); err != nil {
			rows.Close()
			return evidence, err
		}
		evidence.Wakes = append(evidence.Wakes, w)
		switch w.EndReason {
		case "completed":
			evidence.CompletedWakes++
		case "interrupted":
			evidence.InterruptedWakes++
		default:
			rows.Close()
			return evidence, errors.New("offline soak: unexpected wake stop")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return evidence, err
	}
	if err = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM actions WHERE dry_run=1`).Scan(&evidence.DryRunActions); err != nil {
		return evidence, err
	}
	if evidence.CompletedWakes != hours || evidence.InterruptedWakes != 1 || evidence.DryRunActions != hours+1 {
		return evidence, fmt.Errorf("offline soak: wake/action counts failed")
	}
	return evidence, nil
}
