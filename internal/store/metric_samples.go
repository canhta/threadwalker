package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// MetricSample is an own-action outcome DTO. It carries no URLs, text, author
// metadata or inferred engagement when a measurement is absent.
type MetricSample struct {
	ActionID   int64     `json:"action_id"`
	MeasuredAt time.Time `json:"measured_at"`
	Views      int64     `json:"views"`
	Likes      int64     `json:"likes"`
	Replies    int64     `json:"replies"`
	Reposts    int64     `json:"reposts"`
}

func (s *Store) RecentMetricSamples(ctx context.Context, agentID string, limit int) ([]MetricSample, error) {
	if agentID == "" || limit < 1 || limit > 100 {
		return nil, errors.New("reading recent metrics: agent and limit 1..100 required")
	}
	// Store timestamps are UTC RFC3339Nano. Separate seconds and zero-padded
	// fractions to preserve ordering even when trailing fractional zeros vary.
	rows, err := s.db.QueryContext(ctx, `SELECT m.action_id,m.measured_at,m.views,m.likes,m.replies,m.reposts FROM metrics m JOIN actions a ON a.id=m.action_id AND a.agent_id=m.agent_id WHERE m.agent_id=? AND a.dry_run=0 AND a.kind IN ('post','reply') AND trim(a.thread_url)<>'' ORDER BY substr(m.measured_at,1,19) DESC, substr(CASE WHEN substr(m.measured_at,20,1)='.' THEN substr(m.measured_at,21,length(m.measured_at)-21) ELSE '' END || '000000000',1,9) DESC,m.action_id DESC LIMIT ?`, agentID, limit)
	if err != nil {
		return nil, fmt.Errorf("reading recent metrics: %w", err)
	}
	defer rows.Close()
	out := []MetricSample{}
	for rows.Next() {
		var m MetricSample
		var at string
		if err = rows.Scan(&m.ActionID, &at, &m.Views, &m.Likes, &m.Replies, &m.Reposts); err != nil {
			return nil, fmt.Errorf("reading metric sample: %w", err)
		}
		m.MeasuredAt, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, errors.New("reading metric sample: invalid timestamp")
		}
		out = append(out, m)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("reading recent metrics: %w", err)
	}
	return out, nil
}
