package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Outcome labels are code-owned; no tool writes them.
const (
	LabelViral    = "viral"
	LabelFlop     = "flop"
	LabelOrdinary = "ordinary"
)

// MeasuredAction is a live confirmed own action with a completed 24h sample
// that has views. A nil pointer is an unavailable figure, never zero.
type MeasuredAction struct {
	ActionID                       int64
	Kind                           string
	PostedAt, MeasuredAt           time.Time
	Views, Likes, Replies, Reposts int64
	Quotes, Shares                 *int64
	TopicHeat                      *float64
	FollowersAtPost, Followers24h  *int64
	ParentReplies, ParentAgeMins   *int64
	Scored                         bool
}

// MeasuredActions lists the agent's measured live actions, oldest post first.
func (s *Store) MeasuredActions(ctx context.Context, agentID string) ([]MeasuredAction, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,a.kind,a.posted_at,m.measured_at,m.views,m.likes,m.replies,m.reposts,m.quotes,m.shares,a.topic_heat,a.followers_at_post,m.followers,a.parent_replies,a.parent_age_minutes,o.action_id IS NOT NULL
		FROM actions a
		JOIN metric_jobs j ON j.agent_id=a.agent_id AND j.action_id=a.id AND j.window_hours=24
		JOIN metrics m ON m.agent_id=j.agent_id AND m.action_id=j.action_id AND m.measured_at=j.completed_at
		LEFT JOIN action_outcomes o ON o.agent_id=a.agent_id AND o.action_id=a.id
		WHERE a.agent_id=? AND a.dry_run=0 AND trim(a.thread_url)<>'' AND m.views>0
		ORDER BY julianday(a.posted_at),a.id`, agentID)
	if err != nil {
		return nil, fmt.Errorf("reading measured actions: %w", err)
	}
	defer rows.Close()
	var out []MeasuredAction
	for rows.Next() {
		var m MeasuredAction
		var posted, measured string
		if err = rows.Scan(&m.ActionID, &m.Kind, &posted, &measured, &m.Views, &m.Likes, &m.Replies, &m.Reposts, &m.Quotes, &m.Shares, &m.TopicHeat, &m.FollowersAtPost, &m.Followers24h, &m.ParentReplies, &m.ParentAgeMins, &m.Scored); err != nil {
			return nil, fmt.Errorf("reading measured action: %w", err)
		}
		if m.PostedAt, err = time.Parse(time.RFC3339Nano, posted); err != nil {
			return nil, errors.New("measured action: invalid timestamp")
		}
		if m.MeasuredAt, err = time.Parse(time.RFC3339Nano, measured); err != nil {
			return nil, errors.New("measured action: invalid timestamp")
		}
		out = append(out, m)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("reading measured actions: %w", err)
	}
	return out, nil
}

// OutcomeModel is one stored fit of the expected-value model.
type OutcomeModel struct {
	Version          int64
	FittedAt         time.Time
	Rows             int
	CoefficientsJSON string
}

// LatestOutcomeModel returns the newest stored fit; ok is false before the first.
func (s *Store) LatestOutcomeModel(ctx context.Context, agentID string) (m OutcomeModel, ok bool, err error) {
	var fitted string
	err = s.db.QueryRowContext(ctx, `SELECT version,fitted_at,rows,coefficients_json FROM outcome_models WHERE agent_id=? ORDER BY version DESC LIMIT 1`, agentID).Scan(&m.Version, &fitted, &m.Rows, &m.CoefficientsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return m, false, nil
	}
	if err != nil {
		return m, false, fmt.Errorf("reading outcome model: %w", err)
	}
	if m.FittedAt, err = time.Parse(time.RFC3339Nano, fitted); err != nil {
		return m, false, errors.New("outcome model: invalid timestamp")
	}
	return m, true, nil
}

// SaveOutcomeModel stores the next version of the fit.
func (s *Store) SaveOutcomeModel(ctx context.Context, agentID string, m OutcomeModel) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO outcome_models(agent_id,version,fitted_at,rows,coefficients_json) VALUES(?,?,?,?,?)`, agentID, m.Version, m.FittedAt.UTC().Format(time.RFC3339Nano), m.Rows, m.CoefficientsJSON); err != nil {
		return fmt.Errorf("saving outcome model: %w", err)
	}
	return nil
}

// Outcome is the code-owned result of one measured action: its rates, lifts
// and label. AmplificationRate and FollowersPer1k are nil when unavailable.
type Outcome struct {
	ActionID                               int64
	MeasuredAt                             time.Time
	ModelVersion                           int64
	Views                                  int64
	ConversationRate, ApplauseRate         float64
	AmplificationRate, FollowersPer1k      *float64
	ReachLift, ResonanceLift, ApplauseLift float64
	Label                                  string
}

// InsertOutcome stores the outcome of an action once; it never changes.
func (s *Store) InsertOutcome(ctx context.Context, agentID string, o Outcome) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO action_outcomes(agent_id,action_id,measured_at,model_version,views,conversation_rate,amplification_rate,applause_rate,followers_per_1k,reach_lift,resonance_lift,applause_lift,label) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		agentID, o.ActionID, o.MeasuredAt.UTC().Format(time.RFC3339Nano), o.ModelVersion, o.Views, o.ConversationRate, o.AmplificationRate, o.ApplauseRate, o.FollowersPer1k, o.ReachLift, o.ResonanceLift, o.ApplauseLift, o.Label)
	if err != nil {
		return fmt.Errorf("storing outcome: %w", err)
	}
	return nil
}

// OutcomesSince lists the outcomes measured at or after since, oldest first.
func (s *Store) OutcomesSince(ctx context.Context, agentID string, since time.Time) ([]Outcome, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT action_id,measured_at,model_version,views,conversation_rate,amplification_rate,applause_rate,followers_per_1k,reach_lift,resonance_lift,label FROM action_outcomes WHERE agent_id=? AND julianday(measured_at)>=julianday(?) ORDER BY julianday(measured_at),action_id`, agentID, since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("reading outcomes: %w", err)
	}
	defer rows.Close()
	var out []Outcome
	for rows.Next() {
		var o Outcome
		var at string
		if err = rows.Scan(&o.ActionID, &at, &o.ModelVersion, &o.Views, &o.ConversationRate, &o.AmplificationRate, &o.ApplauseRate, &o.FollowersPer1k, &o.ReachLift, &o.ResonanceLift, &o.Label); err != nil {
			return nil, fmt.Errorf("reading outcome: %w", err)
		}
		if o.MeasuredAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, errors.New("outcome: invalid timestamp")
		}
		out = append(out, o)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("reading outcomes: %w", err)
	}
	return out, nil
}

// OutcomeSummary is an own action's code-owned result as the agent reads it,
// lifts rounded to two decimals.
type OutcomeSummary struct {
	ActionID       int64    `json:"action_id"`
	Label          string   `json:"label"`
	ReachLift      float64  `json:"reach_lift"`
	ResonanceLift  float64  `json:"resonance_lift"`
	ApplauseLift   *float64 `json:"applause_lift,omitempty"`
	FollowersPer1k *float64 `json:"followers_per_1k,omitempty"`
}

// RecentOutcomes returns the newest limit outcomes, newest first.
func (s *Store) RecentOutcomes(ctx context.Context, agentID string, limit int) ([]OutcomeSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT action_id,label,round(reach_lift,2),round(resonance_lift,2),round(applause_lift,2),round(followers_per_1k,2) FROM action_outcomes WHERE agent_id=? ORDER BY julianday(measured_at) DESC,action_id DESC LIMIT ?`, agentID, limit)
	if err != nil {
		return nil, fmt.Errorf("reading recent outcomes: %w", err)
	}
	defer rows.Close()
	out := []OutcomeSummary{}
	for rows.Next() {
		var o OutcomeSummary
		if err = rows.Scan(&o.ActionID, &o.Label, &o.ReachLift, &o.ResonanceLift, &o.ApplauseLift, &o.FollowersPer1k); err != nil {
			return nil, fmt.Errorf("reading recent outcome: %w", err)
		}
		out = append(out, o)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("reading recent outcomes: %w", err)
	}
	return out, nil
}

// ActionText is what an own action said, where and when.
type ActionText struct {
	ID              int64
	Text, TargetURL string
	PostedAt        time.Time
	// Hook, Rationale and Parts (the later parts of a series) are empty for actions without them.
	Hook, Rationale string
	Parts           int
}

// RepliedSince returns the target URLs the agent replied to since a time.
func (s *Store) RepliedSince(ctx context.Context, agentID string, dryRun bool, since time.Time) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT target_url FROM actions WHERE agent_id=? AND kind='reply' AND dry_run=? AND target_url<>'' AND posted_at>=?`, agentID, dryRun, since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("reading replied targets: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var u string
		if err = rows.Scan(&u); err != nil {
			return nil, fmt.Errorf("reading replied target: %w", err)
		}
		out[u] = true
	}
	return out, rows.Err()
}

// RecentPostTexts returns the texts of the agent's newest limit posts of one
// mode (dry-run or live), newest first.
func (s *Store) RecentPostTexts(ctx context.Context, agentID string, dryRun bool, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT text FROM actions WHERE agent_id=? AND kind='post' AND dry_run=? ORDER BY id DESC LIMIT ?`, agentID, dryRun, limit)
	if err != nil {
		return nil, fmt.Errorf("reading recent posts: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var t string
		if err = rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("reading recent post: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ActionTextsSince lists the agent's live actions posted at or after since, oldest first.
func (s *Store) ActionTextsSince(ctx context.Context, agentID string, since time.Time) ([]ActionText, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,text,target_url,posted_at,hook,rationale,(SELECT COUNT(*) FROM action_parts WHERE action_id=actions.id) FROM actions WHERE agent_id=? AND dry_run=0 AND julianday(posted_at)>=julianday(?) ORDER BY julianday(posted_at),id`, agentID, since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("reading action texts: %w", err)
	}
	defer rows.Close()
	var out []ActionText
	for rows.Next() {
		var a ActionText
		var at string
		if err = rows.Scan(&a.ID, &a.Text, &a.TargetURL, &at, &a.Hook, &a.Rationale, &a.Parts); err != nil {
			return nil, fmt.Errorf("reading action text: %w", err)
		}
		if a.PostedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, errors.New("action text: invalid timestamp")
		}
		out = append(out, a)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("reading action texts: %w", err)
	}
	return out, nil
}

// BestOwn returns the text of up to 3 live, published actions of kind since
// the cutoff that did best: viral first, then the highest reach lift above 0.
func (s *Store) BestOwn(ctx context.Context, agentID, kind string, since time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.text FROM actions a JOIN action_outcomes o ON o.agent_id=a.agent_id AND o.action_id=a.id WHERE a.agent_id=? AND a.kind=? AND a.dry_run=0 AND a.failed=0 AND julianday(a.posted_at)>=julianday(?) AND (o.label='viral' OR (o.label!='flop' AND o.reach_lift>0)) ORDER BY o.label='viral' DESC,o.reach_lift DESC LIMIT 3`, agentID, kind, since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("reading best own actions: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var t string
		if err = rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("reading best own action: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
