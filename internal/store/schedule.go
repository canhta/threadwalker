package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SetNextWake persists the agent's next wake time in UTC. It only writes
// next_wake_at and leaves every other agent field untouched. It performs no
// clock or clamping policy; the runtime owns the cadence. The agent must already exist.
func (s *Store) SetNextWake(ctx context.Context, agentID string, at time.Time) error {
	const operation = "setting next wake"

	result, err := s.db.ExecContext(ctx,
		`UPDATE agents SET next_wake_at = ? WHERE id = ?`,
		formatStoreTimestamp(at), agentID)
	if err != nil {
		return fmt.Errorf("%s for agent %q: updating next_wake_at: %w", operation, agentID, err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s for agent %q: checking update: %w", operation, agentID, err)
	}
	if updated == 0 {
		return fmt.Errorf("%s for agent %q: agent not found: %w", operation, agentID, sql.ErrNoRows)
	}
	return nil
}
