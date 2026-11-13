package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ActionScope names whose actions a reader sees: one agent, one mode. Readers
// that feed the agent context or the KPI take it, so live and dry-run rows never mix.
type ActionScope struct {
	AgentID string
	DryRun  bool
}

// ActionCounts counts action intents of one mode (dry-run or live), including
// unknown outcomes. Its interval is exactly half-open [from,to).
type ActionCounts struct {
	Posts   int
	Replies int
}

func (s *Store) CountActions(ctx context.Context, scope ActionScope, from, to time.Time) (ActionCounts, error) {
	var counts ActionCounts
	if scope.AgentID == "" || to.Before(from) {
		return counts, errors.New("counting actions: invalid scope or interval")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT kind,posted_at FROM actions WHERE agent_id=? AND dry_run=? AND failed=0`, scope.AgentID, scope.DryRun)
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

// UnpublishedPost is a live post intent with no thread URL and no media id.
type UnpublishedPost struct {
	ID   int64
	Text string
}

// UnpublishedPosts lists the live post intents that never recorded a publish.
func (s *Store) UnpublishedPosts(ctx context.Context, agentID string) ([]UnpublishedPost, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,text FROM actions WHERE agent_id=? AND dry_run=0 AND kind='post' AND failed=0 AND thread_url='' AND media_id=''`, agentID)
	if err != nil {
		return nil, fmt.Errorf("reading unpublished posts: %w", err)
	}
	defer rows.Close()
	var out []UnpublishedPost
	for rows.Next() {
		var p UnpublishedPost
		if err := rows.Scan(&p.ID, &p.Text); err != nil {
			return nil, fmt.Errorf("reading unpublished post: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// MarkActionFailed marks an intent proven unpublished, so it no longer counts
// toward the daily limits. A published action (with a media id) is untouched.
func (s *Store) MarkActionFailed(ctx context.Context, agentID string, id int64) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE actions SET failed=1 WHERE id=? AND agent_id=? AND dry_run=0 AND thread_url='' AND media_id=''`, id, agentID); err != nil {
		return fmt.Errorf("marking action failed: %w", err)
	}
	return nil
}
