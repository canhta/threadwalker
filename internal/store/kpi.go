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
	// Followers is the followers gained on own actions posted in the last 7 days.
	Followers int64
	// LastPublish is the newest published action; zero when there is none.
	LastPublish time.Time
	// Spend and tokens over all phases: the UTC day of now, and that day with the 6 before it.
	SpendToday, Spend7d   float64
	TokensToday, Tokens7d int64
	// WakesToday counts agent wakes started on the UTC day of now.
	WakesToday int
}

// KPIStats reads the scoreboard numbers. dryRun picks which actions count, as in CountActions.
func (s *Store) KPIStats(ctx context.Context, agentID string, dryRun bool, now time.Time) (KPIStats, error) {
	var k KPIStats
	now = now.UTC()
	day := now.Format(time.DateOnly)
	latest := `JOIN metrics m ON m.action_id=a.id AND m.measured_at=(SELECT measured_at FROM metrics WHERE action_id=a.id ORDER BY julianday(measured_at) DESC LIMIT 1)`
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(m.likes+m.replies+m.reposts+COALESCE(m.quotes,0)),0) FROM actions a `+latest+` WHERE a.agent_id=? AND a.dry_run=? AND a.failed=0 AND julianday(a.posted_at)>=julianday(?)`,
		agentID, dryRun, formatStoreTimestamp(now.Add(-24*time.Hour))).Scan(&k.Engagements); err != nil {
		return k, fmt.Errorf("reading engagements: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(COALESCE(m.followers,0)),0) FROM actions a `+latest+` WHERE a.agent_id=? AND a.dry_run=? AND a.failed=0 AND julianday(a.posted_at)>=julianday(?)`,
		agentID, dryRun, formatStoreTimestamp(now.Add(-7*24*time.Hour))).Scan(&k.Followers); err != nil {
		return k, fmt.Errorf("reading followers gained: %w", err)
	}
	var last sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT posted_at FROM actions WHERE agent_id=? AND dry_run=? AND failed=0 ORDER BY julianday(posted_at) DESC LIMIT 1`, agentID, dryRun).Scan(&last); err != nil && err != sql.ErrNoRows {
		return k, fmt.Errorf("reading last publish: %w", err)
	}
	if last.Valid {
		at, err := time.Parse(time.RFC3339Nano, last.String)
		if err != nil {
			return k, fmt.Errorf("reading last publish: invalid timestamp")
		}
		k.LastPublish = at
	}
	week := now.AddDate(0, 0, -6).Format(time.DateOnly)
	if err := s.db.QueryRowContext(ctx, `SELECT
 COALESCE(SUM(CASE WHEN day=?2 THEN cost_usd END),0), COALESCE(SUM(CASE WHEN day=?2 THEN tokens_in+tokens_out END),0),
 COALESCE(SUM(cost_usd),0), COALESCE(SUM(tokens_in+tokens_out),0)
 FROM ledger_phases WHERE agent_id=?1 AND day>=?3 AND day<=?2`, agentID, day, week).Scan(&k.SpendToday, &k.TokensToday, &k.Spend7d, &k.Tokens7d); err != nil {
		return k, fmt.Errorf("reading spend: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wakes WHERE agent_id=? AND kind='agent' AND julianday(started_at)>=julianday(?)`, agentID, day+"T00:00:00Z").Scan(&k.WakesToday); err != nil {
		return k, fmt.Errorf("reading wakes: %w", err)
	}
	return k, nil
}
