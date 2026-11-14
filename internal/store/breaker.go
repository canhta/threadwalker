package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// MeasuredEngagement judges the scope's newest n non-failed actions posted at
// or after since that have at least one metric sample. It returns how many it
// found, the sum of likes plus replies in each one's newest sample, and the id
// of the newest of them.
func (s *Store) MeasuredEngagement(ctx context.Context, scope ActionScope, since time.Time, n int) (count, engagement int, newest int64, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,m.likes+m.replies FROM actions a JOIN metrics m ON m.action_id=a.id AND m.agent_id=a.agent_id WHERE a.agent_id=? AND a.dry_run=? AND a.failed=0 AND julianday(a.posted_at)>=julianday(?) AND m.measured_at=(SELECT MAX(m2.measured_at) FROM metrics m2 WHERE m2.agent_id=m.agent_id AND m2.action_id=m.action_id) ORDER BY julianday(a.posted_at) DESC,a.id DESC LIMIT ?`, scope.AgentID, scope.DryRun, formatStoreTimestamp(since), n)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("reading measured engagement: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var e int
		if err = rows.Scan(&id, &e); err != nil {
			return 0, 0, 0, fmt.Errorf("reading measured engagement row: %w", err)
		}
		if count == 0 {
			newest = id
		}
		count++
		engagement += e
	}
	return count, engagement, newest, rows.Err()
}

// LastOwnerEvent is the time of the newest queued owner message of the agent
// whose event key starts with prefix; the zero time when there is none.
func (s *Store) LastOwnerEvent(ctx context.Context, agentID, prefix string) (time.Time, error) {
	var at string
	err := s.db.QueryRowContext(ctx, `SELECT at FROM owner_msgs WHERE agent_id=? AND direction='out' AND substr(event_key,1,?)=? ORDER BY id DESC LIMIT 1`, agentID, len(prefix), prefix).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("reading owner event: %w", err)
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, errors.New("reading owner event: invalid stored timestamp")
	}
	return t, nil
}
