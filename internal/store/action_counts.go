package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ActionCounts includes dry-run and real action intents, including unknown
// outcomes. Its interval is exactly half-open [from,to).
type ActionCounts struct {
	Posts   int
	Replies int
}

func (s *Store) CountActions(ctx context.Context, agentID string, from, to time.Time) (ActionCounts, error) {
	var counts ActionCounts
	if agentID == "" || to.Before(from) {
		return counts, errors.New("counting actions: invalid scope or interval")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT kind,posted_at FROM actions WHERE agent_id=?`, agentID)
	if err != nil {
		return counts, fmt.Errorf("counting actions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind, stamp string
		if err := rows.Scan(&kind, &stamp); err != nil {
			return counts, fmt.Errorf("reading action count: %w", err)
		}
		at, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return counts, errors.New("counting actions: invalid stored timestamp")
		}
		if !at.Before(from) && at.Before(to) {
			if kind == "post" {
				counts.Posts++
			} else if kind == "reply" {
				counts.Replies++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return counts, fmt.Errorf("counting actions: %w", err)
	}
	return counts, nil
}
