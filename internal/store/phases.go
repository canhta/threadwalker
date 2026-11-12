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
	WakeStudy = "study"
)

// ErrPhaseAlreadyRan reports a dream or study slot already recorded for its
// local-day identity. Callers treat it as a no-op, never as a retry.
var ErrPhaseAlreadyRan = errors.New("phase already ran")

// LastPhaseWake returns the latest start time for one phase kind.
func (s *Store) LastPhaseWake(ctx context.Context, agentID, kind string) (time.Time, bool, error) {
	if agentID == "" || (kind != WakeDream && kind != WakeStudy) {
		return time.Time{}, false, errors.New("reading phase wake: agent and dream/study kind required")
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

// CountPhaseWakes returns the number of phase wakes that started inside the
// half-open interval.
func (s *Store) CountPhaseWakes(ctx context.Context, agentID, kind string, start, end time.Time) (int, error) {
	if agentID == "" || (kind != WakeDream && kind != WakeStudy) || !end.After(start) {
		return 0, errors.New("counting phase wakes: agent, dream/study kind and interval required")
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wakes WHERE agent_id=? AND kind=? AND julianday(started_at)>=julianday(?) AND julianday(started_at)<julianday(?)`, agentID, kind, start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano)).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting phase wakes: %w", err)
	}
	return count, nil
}

// BeginPhaseWake inserts one dream/study session without touching the agent's
// normal next wake. phaseKey is a local-day identity (dream) or local slot
// identity (study); the partial unique index makes a second run impossible even
// across concurrent schedulers and restarts.
func (s *Store) BeginPhaseWake(ctx context.Context, agentID, kind, phaseKey string, at time.Time) (int64, error) {
	if agentID == "" || (kind != WakeDream && kind != WakeStudy) || phaseKey == "" || len(phaseKey) > 64 {
		return 0, errors.New("starting phase wake: agent, dream/study kind and bounded key required")
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
		if phase != "agent" && phase != "study" && phase != "dream" {
			return LLMUsage{}, errors.New("reading phase usage: unknown phase")
		}
		seen[phase] = true
	}
	// Wake+study spend is the daily total minus dream. Deriving it from the
	// total also keeps older rows written before phase splitting counted.
	if len(seen) == 2 && seen["agent"] && seen["study"] {
		total, err := s.LLMUsageForDay(ctx, agentID, day)
		if err != nil {
			return LLMUsage{}, err
		}
		dream, err := s.LLMUsageForDayPhase(ctx, agentID, day, "dream")
		if err != nil {
			return LLMUsage{}, err
		}
		return LLMUsage{TokensIn: total.TokensIn - dream.TokensIn, TokensOut: total.TokensOut - dream.TokensOut, CostUSD: total.CostUSD - dream.CostUSD}, nil
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

// LLMUsageForDayPhase sums one phase.
func (s *Store) LLMUsageForDayPhase(ctx context.Context, agentID, day, phase string) (LLMUsage, error) {
	return s.LLMUsageForDayPhases(ctx, agentID, day, phase)
}

// LLMUsageByPhaseForDay returns each phase's totals; absent phases are zero.
func (s *Store) LLMUsageByPhaseForDay(ctx context.Context, agentID, day string) (map[string]LLMUsage, error) {
	out := map[string]LLMUsage{"agent": {}, "study": {}, "dream": {}}
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
		if phase != "agent" && phase != "study" && phase != "dream" {
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
	Phase      string    `json:"phase"`
	Source     string    `json:"source"`
	Title      string    `json:"title"`
	URL        string    `json:"url"`
	Score      int       `json:"score"`
}

// RecordTrendStories stores at most limit ranked stories and trims this agent's
// rows to a fixed recent window so a long-running agent cannot grow unbounded.
func (s *Store) RecordTrendStories(ctx context.Context, agentID, phase string, stories []TrendStory, now time.Time, limit int) error {
	if agentID == "" || (phase != "agent" && phase != "study") || limit < 1 || limit > 50 {
		return errors.New("recording trend stories: agent, phase and limit 1..50 required")
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
		if _, err = tx.ExecContext(ctx, `INSERT INTO trend_stories(agent_id,observed_at,phase,source,title,url,rank,score) VALUES(?,?,?,?,?,?,?,?)`, agentID, observed.UTC().Format(time.RFC3339Nano), phase, story.Source, story.Title, story.URL, index, story.Score); err != nil {
			return fmt.Errorf("recording trend stories: %w", err)
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM trend_stories WHERE agent_id=? AND (julianday(observed_at)<julianday(?) OR id NOT IN (SELECT id FROM trend_stories WHERE agent_id=? ORDER BY substr(observed_at,1,19) DESC,id DESC LIMIT 500))`, agentID, now.Add(-7*24*time.Hour).UTC().Format(time.RFC3339Nano), agentID); err != nil {
		return fmt.Errorf("trimming trend stories: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("recording trend stories: %w", err)
	}
	return nil
}

// RecentStudyTrends returns this agent's latest study-phase stories inside the
// age window. Agent-phase fetches are excluded from wake context.
func (s *Store) RecentStudyTrends(ctx context.Context, agentID string, since time.Time, limit int) ([]TrendStory, error) {
	return s.recentTrendStories(ctx, agentID, since, limit, "study")
}

// RecentTrendStories returns the latest stories from any phase, used by the
// deterministic opportunistic-study spike check.
func (s *Store) RecentTrendStories(ctx context.Context, agentID string, since time.Time, limit int) ([]TrendStory, error) {
	return s.recentTrendStories(ctx, agentID, since, limit, "")
}

func (s *Store) recentTrendStories(ctx context.Context, agentID string, since time.Time, limit int, phase string) ([]TrendStory, error) {
	if agentID == "" || limit < 1 || limit > 500 {
		return nil, errors.New("reading trend stories: agent and limit 1..500 required")
	}
	query := `SELECT observed_at,phase,source,title,url,score FROM trend_stories WHERE agent_id=? AND julianday(observed_at)>=julianday(?)`
	args := []any{agentID, since.UTC().Format(time.RFC3339Nano)}
	if phase != "" {
		query += ` AND phase=?`
		args = append(args, phase)
	}
	query += ` ORDER BY substr(observed_at,1,19) DESC,id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading trend stories: %w", err)
	}
	defer rows.Close()
	out := []TrendStory{}
	for rows.Next() {
		var story TrendStory
		var at string
		if err = rows.Scan(&at, &story.Phase, &story.Source, &story.Title, &story.URL, &story.Score); err != nil {
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

// ThreadsObservation is one bounded public post observation. Nil counters mean
// the current page mapping did not expose that count.
type ThreadsObservation struct {
	PostURL    string    `json:"post_url"`
	Author     string    `json:"author"`
	TextHash   string    `json:"text_hash"`
	Snippet    string    `json:"snippet"`
	Replies    *int64    `json:"replies,omitempty"`
	Likes      *int64    `json:"likes,omitempty"`
	Reposts    *int64    `json:"reposts,omitempty"`
	Kind       string    `json:"kind"`
	Keyword    string    `json:"keyword"`
	ObservedAt time.Time `json:"observed_at"`
}

// RecordThreadsObservations stores a bounded sample and trims this agent's rows.
func (s *Store) RecordThreadsObservations(ctx context.Context, agentID string, observations []ThreadsObservation, now time.Time) error {
	if agentID == "" {
		return errors.New("recording Threads observations: agent required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("recording Threads observations: %w", err)
	}
	defer tx.Rollback()
	for _, o := range observations {
		if o.PostURL == "" || o.Author == "" || len(o.PostURL) > 2000 || len(o.Author) > 128 || len(o.Snippet) > 512 || len(o.TextHash) != 64 || (o.Kind != "feed" && o.Kind != "search") {
			return errors.New("recording Threads observations: invalid observation")
		}
		observed := o.ObservedAt
		if observed.IsZero() {
			observed = now
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO threads_observations(agent_id,post_url,author,text_hash,snippet,replies,likes,reposts,kind,keyword,observed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, agentID, o.PostURL, o.Author, o.TextHash, o.Snippet, o.Replies, o.Likes, o.Reposts, o.Kind, o.Keyword, observed.UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("recording Threads observations: %w", err)
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM threads_observations WHERE agent_id=? AND (julianday(observed_at)<julianday(?) OR id NOT IN (SELECT id FROM threads_observations WHERE agent_id=? ORDER BY substr(observed_at,1,19) DESC,id DESC LIMIT 2000))`, agentID, now.Add(-14*24*time.Hour).UTC().Format(time.RFC3339Nano), agentID); err != nil {
		return fmt.Errorf("trimming Threads observations: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("recording Threads observations: %w", err)
	}
	return nil
}

// RecentThreadsObservations returns observations in the age window, newest first.
func (s *Store) RecentThreadsObservations(ctx context.Context, agentID string, since time.Time, limit int) ([]ThreadsObservation, error) {
	if agentID == "" || limit < 1 || limit > 2000 {
		return nil, errors.New("reading Threads observations: agent and limit 1..2000 required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT post_url,author,text_hash,snippet,replies,likes,reposts,kind,keyword,observed_at FROM threads_observations WHERE agent_id=? AND julianday(observed_at)>=julianday(?) ORDER BY substr(observed_at,1,19) DESC,id DESC LIMIT ?`, agentID, since.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, fmt.Errorf("reading Threads observations: %w", err)
	}
	defer rows.Close()
	out := []ThreadsObservation{}
	for rows.Next() {
		var o ThreadsObservation
		var replies, likes, reposts sql.NullInt64
		var at string
		if err = rows.Scan(&o.PostURL, &o.Author, &o.TextHash, &o.Snippet, &replies, &likes, &reposts, &o.Kind, &o.Keyword, &at); err != nil {
			return nil, fmt.Errorf("reading Threads observations: %w", err)
		}
		if replies.Valid {
			value := replies.Int64
			o.Replies = &value
		}
		if likes.Valid {
			value := likes.Int64
			o.Likes = &value
		}
		if reposts.Valid {
			value := reposts.Int64
			o.Reposts = &value
		}
		if o.ObservedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, errors.New("reading Threads observations: invalid timestamp")
		}
		out = append(out, o)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("reading Threads observations: %w", err)
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
}

// RecentOwnActions returns this agent's own actions since the given time.
func (s *Store) RecentOwnActions(ctx context.Context, agentID string, since time.Time, limit int) ([]OwnAction, error) {
	if agentID == "" || limit < 1 || limit > 100 {
		return nil, errors.New("reading own actions: agent and limit 1..100 required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,kind,text,posted_at,CASE WHEN length(thread_url)<=512 THEN thread_url ELSE '' END,dry_run FROM actions WHERE agent_id=? AND julianday(posted_at)>=julianday(?) ORDER BY substr(posted_at,1,19) DESC,id DESC LIMIT ?`, agentID, since.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, fmt.Errorf("reading own actions: %w", err)
	}
	defer rows.Close()
	out := []OwnAction{}
	for rows.Next() {
		var action OwnAction
		var at string
		if err = rows.Scan(&action.ID, &action.Kind, &at, &action.Text, &action.ThreadURL, &action.DryRun); err != nil {
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
