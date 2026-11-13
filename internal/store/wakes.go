package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type AgentState struct {
	Status   string
	NextWake time.Time
}

// EnsureAgent creates first-run state without overwriting persisted owner controls.
func (s *Store) EnsureAgent(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("initializing agent: missing id")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO agents(id,status) VALUES(?,'running') ON CONFLICT(id) DO NOTHING`, id)
	if err != nil {
		return fmt.Errorf("initializing agent: %w", err)
	}
	return nil
}

// WakeOpen reports whether the agent has a wake row that has not ended.
func (s *Store) WakeOpen(ctx context.Context, id string) (bool, error) {
	var open bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wakes WHERE agent_id=? AND ended_at IS NULL)`, id).Scan(&open); err != nil {
		return false, fmt.Errorf("reading open wake: %w", err)
	}
	return open, nil
}

func (s *Store) AgentState(ctx context.Context, id string) (AgentState, error) {
	var state AgentState
	var next sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT status,next_wake_at FROM agents WHERE id=?`, id).Scan(&state.Status, &next)
	if err != nil {
		return state, fmt.Errorf("reading agent state: %w", err)
	}
	if next.Valid {
		state.NextWake, err = time.Parse(time.RFC3339Nano, next.String)
		if err != nil {
			return state, fmt.Errorf("reading next wake: invalid timestamp")
		}
	}
	return state, nil
}

// BeginWake reserves a new session and fallback schedule atomically. No prior
// wake's messages or action intent are ever loaded into this session.
func (s *Store) BeginWake(ctx context.Context, id string, at, fallback time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("starting wake: %w", err)
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, `UPDATE agents SET next_wake_at=? WHERE id=? AND status='running' AND NOT EXISTS(SELECT 1 FROM wakes WHERE agent_id=? AND ended_at IS NULL)`, fallback.UTC().Format(time.RFC3339Nano), id, id)
	if err != nil {
		return 0, fmt.Errorf("reserving wake: %w", err)
	}
	n, err := r.RowsAffected()
	if err != nil || n != 1 {
		return 0, fmt.Errorf("reserving wake: agent unavailable")
	}
	r, err = tx.ExecContext(ctx, `INSERT INTO wakes(agent_id,started_at) VALUES(?,?)`, id, at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("inserting wake: %w", err)
	}
	wakeID, err := r.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("identifying wake: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing wake: %w", err)
	}
	return wakeID, nil
}
func (s *Store) FinishWake(ctx context.Context, id int64, at time.Time, steps int, reason string) error {
	r, err := s.db.ExecContext(ctx, `UPDATE wakes SET ended_at=?,steps=?,end_reason=? WHERE id=? AND ended_at IS NULL`, at.UTC().Format(time.RFC3339Nano), steps, reason, id)
	if err != nil {
		return fmt.Errorf("finishing wake: %w", err)
	}
	n, err := r.RowsAffected()
	if err != nil || n != 1 {
		return fmt.Errorf("finishing wake: open wake missing")
	}
	return nil
}

// RecoverWakes closes interrupted sessions, conservatively records pending tool
// outcomes as assumed sent, and advances scheduling beyond restart. It executes
// no tool and creates no replacement session or action.
func (s *Store) RecoverWakes(ctx context.Context, id string, at, future time.Time) ([]int64, error) {
	if !future.After(at) {
		return nil, fmt.Errorf("recovering wakes: future schedule required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("recovering wakes: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,kind FROM wakes WHERE agent_id=? AND ended_at IS NULL ORDER BY id`, id)
	if err != nil {
		return nil, fmt.Errorf("reading interrupted wakes: %w", err)
	}
	ids := []int64{}
	kinds := map[int64]string{}
	recoverSchedule := false
	for rows.Next() {
		var wake int64
		var kind string
		if err = rows.Scan(&wake, &kind); err != nil {
			rows.Close()
			return nil, fmt.Errorf("reading interrupted wake: %w", err)
		}
		ids = append(ids, wake)
		kinds[wake] = kind
		if kind == "agent" {
			recoverSchedule = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("reading interrupted wakes: %w", err)
	}
	for _, wake := range ids {
		outcome := `{"outcome":"unknown_assumed_sent","retry":false}`
		if kinds[wake] == "metrics" || kinds[wake] == "dream" || kinds[wake] == "study" {
			// Dream/study are read-only by guard; nothing can have been posted.
			outcome = `{"outcome":"interrupted_read","retry":true}`
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tool_calls SET result_json=? WHERE wake_id=? AND result_json=''`, outcome, wake); err != nil {
			return nil, fmt.Errorf("retiring interrupted intents: %w", err)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE wakes SET ended_at=?,end_reason='interrupted' WHERE id=?`, at.UTC().Format(time.RFC3339Nano), wake); err != nil {
			return nil, fmt.Errorf("interrupting wake: %w", err)
		}
	}
	if recoverSchedule {
		var scheduled sql.NullString
		if err = tx.QueryRowContext(ctx, `SELECT next_wake_at FROM agents WHERE id=?`, id).Scan(&scheduled); err != nil {
			return nil, fmt.Errorf("reading recovery schedule: %w", err)
		}
		next := future
		if scheduled.Valid {
			parsed, e := time.Parse(time.RFC3339Nano, scheduled.String)
			if e != nil {
				return nil, fmt.Errorf("recovering schedule: invalid timestamp")
			}
			if parsed.After(next) {
				next = parsed
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE agents SET next_wake_at=? WHERE id=?`, next.UTC().Format(time.RFC3339Nano), id); err != nil {
			return nil, fmt.Errorf("advancing recovery schedule: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing recovery: %w", err)
	}
	return ids, nil
}

// DeleteMetricWake removes a metrics wake that found nothing to measure.
func (s *Store) DeleteMetricWake(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM wakes WHERE id=? AND kind='metrics' AND ended_at IS NULL`, id); err != nil {
		return fmt.Errorf("deleting metric wake: %w", err)
	}
	return nil
}
