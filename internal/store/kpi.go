package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// KPIStats is what the scoreboard shows, read from the agent's own rows.
type KPIStats struct {
	// Engagements is likes, replies, reposts and quotes on own actions posted
	// in the last 24 hours, from each action's newest metrics row.
	Engagements int64
	// Followers is the followers gained over the last 7 days: the newest account
	// follower count measured in that window minus the oldest, never negative.
	// metrics.followers is the account total at measurement time, not a delta.
	Followers int64
	// LastPublish is the newest published action; zero when there is none.
	LastPublish time.Time
	// LastPost is the newest own post (not a reply); zero when there is none.
	LastPost time.Time
	// Spend and tokens over all phases: the local day of now, and that day with the 6 before it.
	SpendToday, Spend7d   float64
	TokensToday, Tokens7d int64
	// DreamToday is the part of SpendToday the dream phase spent.
	DreamToday float64
	// WakesToday counts agent wakes started on the local day of now.
	WakesToday int
	// LastWake is when the newest agent wake started; zero when there is none.
	LastWake time.Time
}

// LastPublish is the newest published action of a kind ("" for any) in the
// dryRun mode; zero when there is none.
func (s *Store) LastPublish(ctx context.Context, agentID string, dryRun bool, kind string) (time.Time, error) {
	var last sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT posted_at FROM actions WHERE agent_id=? AND dry_run=? AND failed=0 AND (?3='' OR kind=?3) ORDER BY julianday(posted_at) DESC LIMIT 1`, agentID, dryRun, kind).Scan(&last); err != nil && err != sql.ErrNoRows {
		return time.Time{}, fmt.Errorf("reading last publish: %w", err)
	}
	if !last.Valid {
		return time.Time{}, nil
	}
	at, err := time.Parse(time.RFC3339Nano, last.String)
	if err != nil {
		return time.Time{}, fmt.Errorf("reading last publish: invalid timestamp")
	}
	return at, nil
}

// KPIStats reads the scoreboard numbers; "today" starts at dayStart, a local midnight whose date is the ledger day key. dryRun picks which actions count, as in CountActions.
func (s *Store) KPIStats(ctx context.Context, agentID string, dryRun bool, now, dayStart time.Time) (KPIStats, error) {
	var k KPIStats
	now = now.UTC()
	day := dayStart.Format(time.DateOnly)
	latest := `JOIN metrics m ON m.action_id=a.id AND m.measured_at=(SELECT measured_at FROM metrics WHERE action_id=a.id ORDER BY julianday(measured_at) DESC LIMIT 1)`
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(m.likes+m.replies+m.reposts+COALESCE(m.quotes,0)),0) FROM actions a `+latest+` WHERE a.agent_id=? AND a.dry_run=? AND a.failed=0 AND julianday(a.posted_at)>=julianday(?)`,
		agentID, dryRun, formatStoreTimestamp(now.Add(-24*time.Hour))).Scan(&k.Engagements); err != nil {
		return k, fmt.Errorf("reading engagements: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `WITH s AS (SELECT m.followers f, m.measured_at t FROM metrics m JOIN actions a ON a.id=m.action_id AND a.agent_id=m.agent_id WHERE a.agent_id=? AND a.dry_run=? AND a.failed=0 AND m.followers IS NOT NULL AND julianday(m.measured_at)>=julianday(?))
		SELECT COALESCE(MAX(0, (SELECT f FROM s ORDER BY julianday(t) DESC LIMIT 1) - (SELECT f FROM s ORDER BY julianday(t) LIMIT 1)), 0)`,
		agentID, dryRun, formatStoreTimestamp(now.Add(-7*24*time.Hour))).Scan(&k.Followers); err != nil {
		return k, fmt.Errorf("reading followers gained: %w", err)
	}
	var err error
	if k.LastPublish, err = s.LastPublish(ctx, agentID, dryRun, ""); err != nil {
		return k, err
	}
	if k.LastPost, err = s.LastPublish(ctx, agentID, dryRun, "post"); err != nil {
		return k, err
	}
	week := dayStart.AddDate(0, 0, -6).Format(time.DateOnly)
	if err := s.db.QueryRowContext(ctx, `SELECT
 COALESCE(SUM(CASE WHEN day=?2 THEN cost_usd END),0), COALESCE(SUM(CASE WHEN day=?2 THEN tokens_in+tokens_out END),0),
 COALESCE(SUM(cost_usd),0), COALESCE(SUM(tokens_in+tokens_out),0), COALESCE(SUM(CASE WHEN day=?2 AND phase='dream' THEN cost_usd END),0)
 FROM ledger_phases WHERE agent_id=?1 AND day>=?3 AND day<=?2`, agentID, day, week).Scan(&k.SpendToday, &k.TokensToday, &k.Spend7d, &k.Tokens7d, &k.DreamToday); err != nil {
		return k, fmt.Errorf("reading spend: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wakes WHERE agent_id=? AND kind='agent' AND julianday(started_at)>=julianday(?)`, agentID, formatStoreTimestamp(dayStart)).Scan(&k.WakesToday); err != nil {
		return k, fmt.Errorf("reading wakes: %w", err)
	}
	var last sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(started_at) FROM wakes WHERE agent_id=? AND kind='agent'`, agentID).Scan(&last); err != nil {
		return k, fmt.Errorf("reading last wake: %w", err)
	}
	if last.Valid {
		if k.LastWake, err = time.Parse(time.RFC3339Nano, last.String); err != nil {
			return k, fmt.Errorf("reading last wake: invalid timestamp")
		}
	}
	return k, nil
}
