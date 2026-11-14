package store

import (
	"context"
	"fmt"
	"time"
)

// RecordWebhookEvent stores one webhook event by ids only. fresh is false when
// Meta redelivered an event already recorded.
func (s *Store) RecordWebhookEvent(ctx context.Context, agentID, field, objectID string, at time.Time) (fresh bool, err error) {
	r, err := s.db.ExecContext(ctx, `INSERT INTO webhook_events(agent_id,field,object_id,at) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, agentID, field, objectID, formatStoreTimestamp(at))
	if err != nil {
		return false, fmt.Errorf("recording webhook event: %w", err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("recording webhook event: %w", err)
	}
	return n == 1, nil
}

// WakeEarly moves a running agent's next wake forward to at, but never closer
// than minGap after its latest wake started. settle then adjusts the time, for
// example out of quiet hours. It never postpones a wake.
func (s *Store) WakeEarly(ctx context.Context, agentID string, at time.Time, minGap time.Duration, settle func(time.Time) (time.Time, error)) error {
	var last *string
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(started_at) FROM wakes WHERE agent_id=?`, agentID).Scan(&last); err != nil {
		return fmt.Errorf("waking early: reading last wake: %w", err)
	}
	if last != nil {
		started, err := time.Parse(time.RFC3339Nano, *last)
		if err != nil {
			return fmt.Errorf("waking early: invalid wake timestamp")
		}
		if floor := started.Add(minGap); floor.After(at) {
			at = floor
		}
	}
	at, err := settle(at)
	if err != nil {
		return fmt.Errorf("waking early: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE agents SET next_wake_at=? WHERE id=? AND status='running' AND (next_wake_at IS NULL OR julianday(next_wake_at)>julianday(?))`, formatStoreTimestamp(at), agentID, formatStoreTimestamp(at))
	if err != nil {
		return fmt.Errorf("waking early: %w", err)
	}
	return nil
}

// WakeOnWebhook is the one early-wake path: a webhook reply or mention that
// arrived since the latest wake started moves the next wake up with WakeEarly.
func (s *Store) WakeOnWebhook(ctx context.Context, agentID string, at time.Time, minGap time.Duration, settle func(time.Time) (time.Time, error)) error {
	var met bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM webhook_events WHERE agent_id=?1 AND julianday(at)>COALESCE((SELECT MAX(julianday(started_at)) FROM wakes WHERE agent_id=?1),0))`, agentID).Scan(&met)
	if err != nil {
		return fmt.Errorf("checking webhook events: %w", err)
	}
	if !met {
		return nil
	}
	return s.WakeEarly(ctx, agentID, at, minGap, settle)
}
