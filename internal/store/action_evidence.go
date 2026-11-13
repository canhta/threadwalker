package store

import (
	"context"
	"fmt"
)

// ActionEvidence is what an agent's own records can back a claim with: the
// counters measured on one action and every URL the agent has acted on.
// Owned is false when the action does not exist or belongs to another agent.
type ActionEvidence struct {
	Owned    bool
	Counters map[int64]bool
	URLs     map[string]bool
}

func (s *Store) ActionEvidence(ctx context.Context, agentID string, actionID int64) (ActionEvidence, error) {
	ev := ActionEvidence{Counters: map[int64]bool{}, URLs: map[string]bool{}}
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM actions WHERE id = ? AND agent_id = ?)`, actionID, agentID).Scan(&ev.Owned); err != nil {
		return ev, fmt.Errorf("reading evidence for agent %q action %d: %w", agentID, actionID, err)
	}
	if !ev.Owned {
		return ev, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT views, likes, replies, reposts FROM metrics WHERE agent_id = ? AND action_id = ?`, agentID, actionID)
	if err != nil {
		return ev, fmt.Errorf("reading counters for agent %q action %d: %w", agentID, actionID, err)
	}
	for rows.Next() {
		var c [4]int64
		if err := rows.Scan(&c[0], &c[1], &c[2], &c[3]); err != nil {
			rows.Close()
			return ev, fmt.Errorf("reading counters for agent %q action %d: scanning: %w", agentID, actionID, err)
		}
		for _, v := range c {
			ev.Counters[v] = true
		}
	}
	if err := rows.Close(); err != nil {
		return ev, fmt.Errorf("reading counters for agent %q action %d: %w", agentID, actionID, err)
	}
	urls, err := s.db.QueryContext(ctx, `SELECT thread_url, target_url FROM actions WHERE agent_id = ?`, agentID)
	if err != nil {
		return ev, fmt.Errorf("reading action urls for agent %q: %w", agentID, err)
	}
	defer urls.Close()
	for urls.Next() {
		var thread, target string
		if err := urls.Scan(&thread, &target); err != nil {
			return ev, fmt.Errorf("reading action urls for agent %q: scanning: %w", agentID, err)
		}
		ev.URLs[thread], ev.URLs[target] = true, true
	}
	if err := urls.Err(); err != nil {
		return ev, fmt.Errorf("reading action urls for agent %q: %w", agentID, err)
	}
	return ev, nil
}
