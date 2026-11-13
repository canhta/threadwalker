package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// GetSecret returns one agent's sealed secret and its expiry.
func (s *Store) GetSecret(ctx context.Context, agentID, name string) ([]byte, time.Time, bool, error) {
	var sealed []byte
	var expires string
	err := s.db.QueryRowContext(ctx, `SELECT sealed, expires_at FROM secrets WHERE agent_id=? AND name=?`, agentID, name).Scan(&sealed, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("reading secret slot: %w", err)
	}
	at, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("reading secret slot: %w", err)
	}
	return sealed, at, true, nil
}

// PutSecret replaces one agent's sealed secret.
func (s *Store) PutSecret(ctx context.Context, agentID, name string, sealed []byte, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO secrets(agent_id, name, sealed, expires_at) VALUES(?,?,?,?) ON CONFLICT(agent_id, name) DO UPDATE SET sealed=excluded.sealed, expires_at=excluded.expires_at`, agentID, name, sealed, expires.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("writing secret slot: %w", err)
	}
	return nil
}

// RecordFollowers stores the agent's latest own follower count.
func (s *Store) RecordFollowers(ctx context.Context, agentID string, followers int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO account_followers(agent_id, followers, at) VALUES(?,?,?) ON CONFLICT(agent_id) DO UPDATE SET followers=excluded.followers, at=excluded.at`, agentID, followers, at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("recording followers: %w", err)
	}
	return nil
}

// LatestFollowers returns the last recorded follower count, if any.
func (s *Store) LatestFollowers(ctx context.Context, agentID string) (int64, bool, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT followers FROM account_followers WHERE agent_id=?`, agentID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("reading followers: %w", err)
	}
	return n, true, nil
}
