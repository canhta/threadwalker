package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/tools"
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
	evidence := SoakEvidence{Synthetic: true, TimingKind: "logical_time", LogicalHours: hours}
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
	count, err := st.AgentCount(ctx)
	if err != nil {
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
		return New(Options{Store: st, Template: tmpl, Now: func() time.Time { return now }, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends, FetchStudy: fixture.Study, Logger: logger})
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
			runtime.event(ctx, "", "wake.started", "started", "due", id, 0)
			embedder, err := runtime.embedder(guard.PhaseAgent, 0)
			if err != nil {
				return evidence, err
			}
			d, err := tools.NewDispatcher(tools.Options{Scope: guard.Scope{Store: st, Template: tmpl, WakeID: id, Now: func() time.Time { return now }, Logger: logger}, Threads: threads.NewMemory(), FetchTrends: fixture.Trends, FetchStudy: fixture.Study, Embedder: embedder})
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
	records, err := st.WakeRecords(ctx, "")
	if err != nil {
		return evidence, err
	}
	for _, w := range records {
		evidence.Wakes = append(evidence.Wakes, WakeEvidence{ID: w.ID, StartedAt: w.StartedAt, EndedAt: w.EndedAt, EndReason: w.EndReason, Steps: w.Steps, TokensIn: w.TokensIn, TokensOut: w.TokensOut, CostUSD: w.CostUSD})
		switch w.EndReason {
		case "completed":
			evidence.CompletedWakes++
		case "interrupted":
			evidence.InterruptedWakes++
		default:
			return evidence, errors.New("offline soak: unexpected wake stop")
		}
	}
	tally, err := st.ActionTally(ctx, "")
	if err != nil {
		return evidence, err
	}
	evidence.DryRunActions = tally.DryRun
	if evidence.CompletedWakes != hours || evidence.InterruptedWakes != 1 || evidence.DryRunActions != hours+1 {
		return evidence, fmt.Errorf("offline soak: wake/action counts failed")
	}
	return evidence, nil
}
