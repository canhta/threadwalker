package store

import (
	"context"
	"fmt"
	"time"
)

// RefusedToolTTL is how long a tool the Meta app may not use stays hidden.
const RefusedToolTTL = 24 * time.Hour

// RefuseTool records that a read tool returned limited_access.
func (s *Store) RefuseTool(ctx context.Context, agentID, tool string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO refused_tools(agent_id,tool,at) VALUES(?,?,?) ON CONFLICT(agent_id,tool) DO UPDATE SET at=excluded.at`, agentID, tool, formatStoreTimestamp(at))
	if err != nil {
		return fmt.Errorf("recording refused tool: %w", err)
	}
	return nil
}

// RefusedTools lists the tools refused within RefusedToolTTL of now.
func (s *Store) RefusedTools(ctx context.Context, agentID string, now time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tool FROM refused_tools WHERE agent_id=? AND julianday(at)>julianday(?) ORDER BY tool`, agentID, formatStoreTimestamp(now.Add(-RefusedToolTTL)))
	if err != nil {
		return nil, fmt.Errorf("reading refused tools: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var tool string
		if err = rows.Scan(&tool); err != nil {
			return nil, fmt.Errorf("reading refused tool: %w", err)
		}
		out = append(out, tool)
	}
	return out, rows.Err()
}
