package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Wake kinds are phase identities used by scheduling, guards and accounting.
const (
	WakeAgent = "agent"
	WakeDream = "dream"
)

// ErrPhaseAlreadyRan reports a dream already recorded for its local-day
// identity. Callers treat it as a no-op, never as a retry.
var ErrPhaseAlreadyRan = errors.New("phase already ran")

// LastPhaseWake returns the latest start time for one phase kind.
func (s *Store) LastPhaseWake(ctx context.Context, agentID, kind string) (time.Time, bool, error) {
	if agentID == "" || kind != WakeDream {
		return time.Time{}, false, errors.New("reading phase wake: agent and dream kind required")
	}
	var raw sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT MAX(started_at) FROM wakes WHERE agent_id=? AND kind=?`, agentID, kind).Scan(&raw)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("reading %s wake: %w", kind, err)
	}
	if !raw.Valid {
		return time.Time{}, false, nil
	}
	at, err := time.Parse(time.RFC3339Nano, raw.String)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("reading %s wake: invalid timestamp", kind)
	}
	return at, true, nil
}

// BeginPhaseWake inserts one dream session without touching the agent's normal
// next wake. phaseKey is the local-day identity; the partial unique index makes
// a second run impossible even across concurrent schedulers and restarts.
func (s *Store) BeginPhaseWake(ctx context.Context, agentID, kind, phaseKey string, at time.Time) (int64, error) {
	if agentID == "" || kind != WakeDream || phaseKey == "" || len(phaseKey) > 64 {
		return 0, errors.New("starting phase wake: agent, dream kind and bounded key required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("starting %s wake: %w", kind, err)
	}
	defer tx.Rollback()
	var existing bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wakes WHERE agent_id=? AND kind=? AND phase_key=?)`, agentID, kind, phaseKey).Scan(&existing); err != nil {
		return 0, fmt.Errorf("starting %s wake: %w", kind, err)
	}
	if existing {
		return 0, ErrPhaseAlreadyRan
	}
	var running bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=? AND status='running')`, agentID).Scan(&running); err != nil {
		return 0, fmt.Errorf("starting %s wake: %w", kind, err)
	}
	if !running {
		return 0, fmt.Errorf("starting %s wake: agent unavailable", kind)
	}
	var open bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wakes WHERE agent_id=? AND ended_at IS NULL)`, agentID).Scan(&open); err != nil {
		return 0, fmt.Errorf("starting %s wake: %w", kind, err)
	}
	if open {
		return 0, fmt.Errorf("starting %s wake: wake already open", kind)
	}
	r, err := tx.ExecContext(ctx, `INSERT INTO wakes(agent_id,started_at,kind,phase_key) VALUES(?,?,?,?)`, agentID, at.UTC().Format(time.RFC3339Nano), kind, phaseKey)
	if err != nil {
		var unique bool
		unique = isUniqueViolation(err)
		if unique {
			return 0, ErrPhaseAlreadyRan
		}
		return 0, fmt.Errorf("starting %s wake: %w", kind, err)
	}
	id, err := r.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("starting %s wake: %w", kind, err)
	}
	if err = tx.Commit(); err != nil {
		return 0, fmt.Errorf("starting %s wake: %w", kind, err)
	}
	return id, nil
}

// isUniqueViolation keeps the driver-specific text out of caller diagnostics.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// LLMUsageForDayPhases sums the named phase rows. A repeated phase counts once.
// Unknown phases are rejected so a typo cannot silently under-count spend.
func (s *Store) LLMUsageForDayPhases(ctx context.Context, agentID, day string, phases ...string) (LLMUsage, error) {
	if agentID == "" || day == "" || len(phases) == 0 {
		return LLMUsage{}, errors.New("reading phase usage: agent, day and phase required")
	}
	seen := map[string]bool{}
	for _, phase := range phases {
		if phase != "agent" && phase != "dream" && phase != "critic" {
			return LLMUsage{}, errors.New("reading phase usage: unknown phase")
		}
		seen[phase] = true
	}
	args := []any{agentID, day}
	placeholders := ""
	for phase := range seen {
		if placeholders != "" {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, phase)
	}
	var usage LLMUsage
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(tokens_in),0),COALESCE(SUM(tokens_out),0),COALESCE(SUM(cost_usd),0) FROM ledger_phases WHERE agent_id=? AND day=? AND phase IN (`+placeholders+`)`, args...).Scan(&usage.TokensIn, &usage.TokensOut, &usage.CostUSD)
	if err != nil {
		return LLMUsage{}, fmt.Errorf("reading phase usage: %w", err)
	}
	return usage, nil
}

// LLMUsageByPhaseForDay returns each phase's totals; absent phases are zero.
func (s *Store) LLMUsageByPhaseForDay(ctx context.Context, agentID, day string) (map[string]LLMUsage, error) {
	out := map[string]LLMUsage{"agent": {}, "dream": {}, "critic": {}}
	rows, err := s.db.QueryContext(ctx, `SELECT phase,tokens_in,tokens_out,cost_usd FROM ledger_phases WHERE agent_id=? AND day=?`, agentID, day)
	if err != nil {
		return nil, fmt.Errorf("reading phase usage: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var phase string
		var usage LLMUsage
		if err = rows.Scan(&phase, &usage.TokensIn, &usage.TokensOut, &usage.CostUSD); err != nil {
			return nil, fmt.Errorf("reading phase usage: %w", err)
		}
		if phase != "agent" && phase != "dream" && phase != "critic" {
			return nil, errors.New("reading phase usage: unknown stored phase")
		}
		out[phase] = usage
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("reading phase usage: %w", err)
	}
	return out, nil
}

// TrendStory is one ranked external story observed by fetch_trends.
type TrendStory struct {
	ObservedAt time.Time `json:"observed_at"`
	Source     string    `json:"source"`
	Title      string    `json:"title"`
	URL        string    `json:"url"`
	Score      int       `json:"score"`
	// Rank is the story's position in its snapshot, 0 first.
	Rank int `json:"rank"`
}

// RecordTrendStories stores at most limit ranked stories.
func (s *Store) RecordTrendStories(ctx context.Context, agentID string, stories []TrendStory, now time.Time, limit int) error {
	if agentID == "" || limit < 1 || limit > 50 {
		return errors.New("recording trend stories: agent and limit 1..50 required")
	}
	if len(stories) > limit {
		stories = stories[:limit]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("recording trend stories: %w", err)
	}
	defer tx.Rollback()
	for index, story := range stories {
		if story.Title == "" || story.URL == "" || len(story.Title) > 1000 || len(story.URL) > 2000 {
			return errors.New("recording trend stories: invalid story")
		}
		observed := story.ObservedAt
		if observed.IsZero() {
			observed = now
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO trend_stories(agent_id,observed_at,source,title,url,rank,score) VALUES(?,?,?,?,?,?,?)`, agentID, observed.UTC().Format(time.RFC3339Nano), story.Source, story.Title, story.URL, index, story.Score); err != nil {
			return fmt.Errorf("recording trend stories: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("recording trend stories: %w", err)
	}
	return nil
}

// RecentTrendStories returns the agent's latest stories inside the age window.
func (s *Store) RecentTrendStories(ctx context.Context, agentID string, since time.Time, limit int) ([]TrendStory, error) {
	if agentID == "" || limit < 1 || limit > 500 {
		return nil, errors.New("reading trend stories: agent and limit 1..500 required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT observed_at,source,title,url,score,rank FROM trend_stories WHERE agent_id=? AND julianday(observed_at)>=julianday(?) ORDER BY substr(observed_at,1,19) DESC,id DESC LIMIT ?`, agentID, since.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, fmt.Errorf("reading trend stories: %w", err)
	}
	defer rows.Close()
	out := []TrendStory{}
	for rows.Next() {
		var story TrendStory
		var at string
		if err = rows.Scan(&at, &story.Source, &story.Title, &story.URL, &story.Score, &story.Rank); err != nil {
			return nil, fmt.Errorf("reading trend stories: %w", err)
		}
		if story.ObservedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, errors.New("reading trend stories: invalid timestamp")
		}
		out = append(out, story)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("reading trend stories: %w", err)
	}
	return out, nil
}

// IsDoNotContact reports a permanent contact flag for the given username.
func (s *Store) IsDoNotContact(ctx context.Context, agentID, username string) (bool, error) {
	normalized, err := NormalizeUsername(username)
	if err != nil {
		return false, err
	}
	var dnc bool
	err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM contacts WHERE agent_id=? AND username=? AND do_not_contact=1)`, agentID, normalized).Scan(&dnc)
	if err != nil {
		return false, fmt.Errorf("reading do-not-contact flag: %w", err)
	}
	return dnc, nil
}

// OwnAction is one of the agent's own reserved actions, without third-party data.
type OwnAction struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text"`
	PostedAt  time.Time `json:"posted_at"`
	ThreadURL string    `json:"thread_url,omitempty"`
	DryRun    bool      `json:"dry_run"`
	Hook      string    `json:"hook,omitempty"`
	// Parts counts the later parts of a series post.
	Parts int `json:"parts,omitempty"`
}

// RecentOwnActions returns the scope's own actions that did not fail since the
// given time, newest first.
func (s *Store) RecentOwnActions(ctx context.Context, scope ActionScope, since time.Time, limit int) ([]OwnAction, error) {
	if scope.AgentID == "" || limit < 1 || limit > 100 {
		return nil, errors.New("reading own actions: agent and limit 1..100 required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,kind,text,posted_at,CASE WHEN length(thread_url)<=512 THEN thread_url ELSE '' END,dry_run,hook,(SELECT COUNT(*) FROM action_parts WHERE action_id=actions.id) FROM actions WHERE agent_id=? AND dry_run=? AND failed=0 AND julianday(posted_at)>=julianday(?) ORDER BY substr(posted_at,1,19) DESC,id DESC LIMIT ?`, scope.AgentID, scope.DryRun, since.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, fmt.Errorf("reading own actions: %w", err)
	}
	defer rows.Close()
	out := []OwnAction{}
	for rows.Next() {
		var action OwnAction
		var at string
		if err = rows.Scan(&action.ID, &action.Kind, &action.Text, &at, &action.ThreadURL, &action.DryRun, &action.Hook, &action.Parts); err != nil {
			return nil, fmt.Errorf("reading own actions: %w", err)
		}
		action.Kind = oneOfKind(action.Kind)
		if action.PostedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, errors.New("reading own actions: invalid timestamp")
		}
		out = append(out, action)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("reading own actions: %w", err)
	}
	return out, nil
}

func oneOfKind(kind string) string {
	if kind == "post" || kind == "reply" {
		return kind
	}
	return "unknown"
}

// ReplyTargets returns the target URLs of the agent's own replies.
func (s *Store) ReplyTargets(ctx context.Context, agentID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT target_url FROM actions WHERE agent_id=? AND kind='reply' AND target_url<>''`, agentID)
	if err != nil {
		return nil, fmt.Errorf("reading reply targets: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var target string
		if err = rows.Scan(&target); err != nil {
			return nil, fmt.Errorf("reading reply targets: %w", err)
		}
		out = append(out, target)
	}
	return out, rows.Err()
}
