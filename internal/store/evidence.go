package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// WakeRecord is one wake row as stored, for eval and soak evidence.
type WakeRecord struct {
	ID                  int64
	AgentID             string
	StartedAt, EndedAt  string
	EndReason           string
	Steps               int
	TokensIn, TokensOut int64
	CostUSD             float64
}

// WakeRecords lists wakes oldest first; an empty agentID selects every agent.
func (s *Store) WakeRecords(ctx context.Context, agentID string) ([]WakeRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,agent_id,started_at,COALESCE(ended_at,''),end_reason,steps,tokens_in,tokens_out,cost_usd FROM wakes WHERE ?1='' OR agent_id=?1 ORDER BY id`, agentID)
	if err != nil {
		return nil, fmt.Errorf("reading wake records: %w", err)
	}
	defer rows.Close()
	var out []WakeRecord
	for rows.Next() {
		var w WakeRecord
		if err = rows.Scan(&w.ID, &w.AgentID, &w.StartedAt, &w.EndedAt, &w.EndReason, &w.Steps, &w.TokensIn, &w.TokensOut, &w.CostUSD); err != nil {
			return nil, fmt.Errorf("reading wake records: %w", err)
		}
		out = append(out, w)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("reading wake records: %w", err)
	}
	return out, nil
}

// WakeEndReason returns the stored end reason of one wake.
func (s *Store) WakeEndReason(ctx context.Context, wakeID int64) (string, error) {
	var reason string
	if err := s.db.QueryRowContext(ctx, `SELECT end_reason FROM wakes WHERE id=?`, wakeID).Scan(&reason); err != nil {
		return "", fmt.Errorf("reading wake %d end reason: %w", wakeID, err)
	}
	return reason, nil
}

// WakeTotals sums wake usage; an empty agentID or zero wakeID selects all.
type WakeTotals struct {
	Steps               int
	TokensIn, TokensOut int64
	CostUSD             float64
	Completed, Budget   int
}

func (s *Store) WakeTotals(ctx context.Context, agentID string, wakeID int64) (t WakeTotals, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(steps),0),COALESCE(SUM(tokens_in),0),COALESCE(SUM(tokens_out),0),COALESCE(SUM(cost_usd),0),COUNT(CASE WHEN end_reason='completed' THEN 1 END),COUNT(CASE WHEN end_reason='budget' THEN 1 END) FROM wakes WHERE (?1='' OR agent_id=?1) AND (?2=0 OR id=?2)`, agentID, wakeID).
		Scan(&t.Steps, &t.TokensIn, &t.TokensOut, &t.CostUSD, &t.Completed, &t.Budget)
	if err != nil {
		err = fmt.Errorf("summing wake usage: %w", err)
	}
	return
}

// MetricsWakeCount counts maintenance wakes of kind metrics.
func (s *Store) MetricsWakeCount(ctx context.Context) (n int, err error) {
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wakes WHERE kind='metrics'`).Scan(&n); err != nil {
		err = fmt.Errorf("counting metric wakes: %w", err)
	}
	return
}

// AgentCount counts agent rows.
func (s *Store) AgentCount(ctx context.Context) (n int, err error) {
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents`).Scan(&n); err != nil {
		err = fmt.Errorf("counting agents: %w", err)
	}
	return
}

// SetAgentStatus stores an agent's status.
func (s *Store) SetAgentStatus(ctx context.Context, agentID, status string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE agents SET status=? WHERE id=?`, status, agentID); err != nil {
		return fmt.Errorf("setting agent status: %w", err)
	}
	return nil
}

// ToolCallNames lists audited tool names oldest first; an empty agentID or
// zero wakeID selects all.
func (s *Store) ToolCallNames(ctx context.Context, agentID string, wakeID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tool FROM tool_calls WHERE (?1='' OR agent_id=?1) AND (?2=0 OR wake_id=?2) ORDER BY id`, agentID, wakeID)
	if err != nil {
		return nil, fmt.Errorf("reading tool call audit: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("reading tool call audit: %w", err)
		}
		out = append(out, name)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("reading tool call audit: %w", err)
	}
	return out, nil
}

// LastBlockedBy returns the guard reason on the newest audited tool call.
func (s *Store) LastBlockedBy(ctx context.Context) (string, error) {
	var blocked string
	if err := s.db.QueryRowContext(ctx, `SELECT blocked_by FROM tool_calls ORDER BY id DESC LIMIT 1`).Scan(&blocked); err != nil {
		return "", fmt.Errorf("reading newest tool call: %w", err)
	}
	return blocked, nil
}

// ActionTally counts action rows; an empty agentID selects every agent.
type ActionTally struct{ Total, Confirmed, DryRun int }

func (s *Store) ActionTally(ctx context.Context, agentID string) (t ActionTally, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(thread_url!=''),0),COALESCE(SUM(dry_run),0) FROM actions WHERE ?1='' OR agent_id=?1`, agentID).Scan(&t.Total, &t.Confirmed, &t.DryRun)
	if err != nil {
		err = fmt.Errorf("counting actions: %w", err)
	}
	return
}

// StopRequestContacts counts do-not-contact users recorded from a stop request.
func (s *Store) StopRequestContacts(ctx context.Context, agentID string) (n int, err error) {
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM contacts WHERE agent_id=? AND do_not_contact=1 AND reason='stop_request'`, agentID).Scan(&n); err != nil {
		err = fmt.Errorf("counting stop-request contacts: %w", err)
	}
	return
}

// PlaybookOutcomeCount counts recorded outcome markers.
func (s *Store) PlaybookOutcomeCount(ctx context.Context, agentID string) (n int, err error) {
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM playbook_outcomes WHERE agent_id=?`, agentID).Scan(&n); err != nil {
		err = fmt.Errorf("counting playbook outcomes: %w", err)
	}
	return
}

// MetricTally counts stored metric samples, completed measurement jobs and
// samples that belong to another agent or to one of the excluded actions.
type MetricTally struct{ Samples, CompletedJobs, Foreign int }

func (s *Store) MetricTally(ctx context.Context, agentID string, excludedActions ...int64) (t MetricTally, err error) {
	args := []any{agentID}
	marks := "NULL"
	if len(excludedActions) > 0 {
		marks = strings.TrimSuffix(strings.Repeat("?,", len(excludedActions)), ",")
		for _, id := range excludedActions {
			args = append(args, id)
		}
	}
	err = s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM metrics),(SELECT COUNT(*) FROM metric_jobs WHERE completed_at IS NOT NULL),(SELECT COUNT(*) FROM metrics WHERE agent_id<>? OR action_id IN (`+marks+`))`, args...).Scan(&t.Samples, &t.CompletedJobs, &t.Foreign)
	if err != nil {
		err = fmt.Errorf("counting metrics: %w", err)
	}
	return
}

// SeededAction is a synthetic action row; ID zero lets the database assign it.
type SeededAction struct {
	ID                  int64
	AgentID, Kind, Text string
	ThreadURL, MediaID  string
	DryRun              bool
	PostedAt            time.Time
}

// SeedAction inserts a synthetic action row and returns its id.
func (s *Store) SeedAction(ctx context.Context, a SeededAction) (int64, error) {
	var id any
	if a.ID != 0 {
		id = a.ID
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO actions(id,agent_id,kind,text,dry_run,posted_at,thread_url,media_id) VALUES(?,?,?,?,?,?,?,?)`, id, a.AgentID, a.Kind, a.Text, a.DryRun, a.PostedAt.Format(time.RFC3339Nano), a.ThreadURL, a.MediaID)
	if err != nil {
		return 0, fmt.Errorf("seeding action: %w", err)
	}
	return res.LastInsertId()
}

// SeededPost is a synthetic post with a finished 24h measurement and a stored
// ordinary outcome whose reach and resonance lift are both Lift.
type SeededPost struct {
	Text, URL          string
	PostedAt, Measured time.Time
	Likes              int
	Lift               float64
}

// SeedMeasuredPosts inserts posts with a metric sample and completed 1h, 6h
// and 24h measurement jobs in one transaction.
func (s *Store) SeedMeasuredPosts(ctx context.Context, agentID string, posts []SeededPost) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("seeding measured posts: %w", err)
	}
	defer tx.Rollback()
	for _, p := range posts {
		res, err := tx.ExecContext(ctx, `INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url) VALUES(?,'post',?,0,?,?)`, agentID, p.Text, p.PostedAt.Format(time.RFC3339Nano), p.URL)
		if err != nil {
			return fmt.Errorf("seeding measured posts: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("seeding measured posts: %w", err)
		}
		measured := p.Measured.Format(time.RFC3339Nano)
		if _, err = tx.ExecContext(ctx, `INSERT INTO metrics(agent_id,action_id,measured_at,views,likes) VALUES(?,?,?,100,?)`, agentID, id, measured, p.Likes); err != nil {
			return fmt.Errorf("seeding measured posts: %w", err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO action_outcomes(agent_id,action_id,measured_at,model_version,views,conversation_rate,applause_rate,reach_lift,resonance_lift,label) VALUES(?,?,?,0,100,0,?,?,?,'ordinary')`, agentID, id, measured, float64(p.Likes)/100, p.Lift, p.Lift); err != nil {
			return fmt.Errorf("seeding measured posts: %w", err)
		}
		for _, h := range []int{1, 6, 24} {
			due := p.PostedAt.Add(time.Duration(h) * time.Hour).Format("2006-01-02T15:04:05.000000000Z")
			if _, err = tx.ExecContext(ctx, `INSERT INTO metric_jobs(agent_id,action_id,window_hours,due_at,not_before,completed_at) VALUES(?,?,?,?,?,?)`, agentID, id, h, due, due, measured); err != nil {
				return fmt.Errorf("seeding measured posts: %w", err)
			}
		}
	}
	return tx.Commit()
}

// FailMetricInserts makes every metric insert abort while on, to exercise the
// processor's persistence-failure path.
func (s *Store) FailMetricInserts(ctx context.Context, on bool) error {
	q := `DROP TRIGGER IF EXISTS synthetic_metric_failure`
	if on {
		q = `CREATE TRIGGER IF NOT EXISTS synthetic_metric_failure BEFORE INSERT ON metrics BEGIN SELECT RAISE(ABORT,'PRIVATE METRIC FAILURE'); END`
	}
	if _, err := s.db.ExecContext(ctx, q); err != nil {
		return fmt.Errorf("toggling metric insert failure: %w", err)
	}
	return nil
}

// AgentMetricSample is one stored metric sample with its owning agent.
type AgentMetricSample struct {
	AgentID string
	MetricSample
}

// AllMetricSamples lists every stored sample ordered by time, agent and action.
func (s *Store) AllMetricSamples(ctx context.Context) ([]AgentMetricSample, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id,action_id,measured_at,views,likes,replies,reposts FROM metrics ORDER BY measured_at,agent_id,action_id`)
	if err != nil {
		return nil, fmt.Errorf("reading metric samples: %w", err)
	}
	defer rows.Close()
	var out []AgentMetricSample
	for rows.Next() {
		var m AgentMetricSample
		var at string
		if err = rows.Scan(&m.AgentID, &m.ActionID, &at, &m.Views, &m.Likes, &m.Replies, &m.Reposts); err != nil {
			return nil, fmt.Errorf("reading metric samples: %w", err)
		}
		if m.MeasuredAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, fmt.Errorf("reading metric samples: invalid measured timestamp: %w", err)
		}
		out = append(out, m)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("reading metric samples: %w", err)
	}
	return out, nil
}

// ReadOnlyProbe tries an UPDATE and a schema change and reports whether the
// store rejected each; a store from OpenReadOnly must reject both.
func (s *Store) ReadOnlyProbe(ctx context.Context) (updateRejected, schemaRejected bool) {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET status='killed'`)
	updateRejected = err != nil
	_, err = s.db.ExecContext(ctx, `CREATE TABLE forbidden(id)`)
	schemaRejected = err != nil
	return
}

// AgentStatus returns an agent's stored status without parsing its schedule.
func (s *Store) AgentStatus(ctx context.Context, agentID string) (status string, err error) {
	if err = s.db.QueryRowContext(ctx, `SELECT status FROM agents WHERE id=?`, agentID).Scan(&status); err != nil {
		err = fmt.Errorf("reading agent status: %w", err)
	}
	return
}
