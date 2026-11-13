package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// PlaybookTx is the harness-only view of the playbook outcome tables inside one
// writer transaction. Every method runs on that transaction's connection.
type PlaybookTx struct{ conn *sql.Conn }

// PlaybookOutcomes runs fn in one writer transaction; outcome markers, counters
// and decisions commit together or not at all.
func (s *Store) PlaybookOutcomes(ctx context.Context, fn func(PlaybookTx) error) error {
	return s.write(ctx, func(conn *sql.Conn) error { return fn(PlaybookTx{conn}) })
}

// PlaybookObservation is one completed 24h measurement of a tagged action that
// has no recorded outcome for the tagged hypothesis yet. Times are stored text.
type PlaybookObservation struct {
	Action, Hypothesis, Revision int64
	Kind, Posted, Measured       string
}

// PlaybookCounters is the current revision of a hypothesis and its counters.
// Kind is the action kind of its first counted action, empty before there is one.
type PlaybookCounters struct {
	Revision, N  int64
	Status, Kind string
}

// PlaybookLift is one counted action's lifts under its hypothesis's current
// revision. A hypothesis without counted actions has one row with Counted false.
type PlaybookLift struct {
	Hypothesis               int64
	Status, Kind             string
	Counted                  bool
	ReachLift, ResonanceLift float64
}

// PlaybookDecision is a terminal decision.
type PlaybookDecision struct {
	Hypothesis, Revision, N int64
	Status, Reason          string
	DecidedAt               time.Time
}

// PlaybookOutcome is the idempotency marker of one action and hypothesis.
type PlaybookOutcome struct {
	Action, Hypothesis, Revision int64
	MeasuredAt, Reason           string
	Counted                      bool
}

type playbookQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func playbookLifts(ctx context.Context, q playbookQuerier, agentID string) ([]PlaybookLift, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.id,p.status,p.kind,po.action_id IS NOT NULL,COALESCE(o.reach_lift,0),COALESCE(o.resonance_lift,0) FROM playbook p LEFT JOIN playbook_outcomes po ON po.agent_id=p.agent_id AND po.hypothesis_id=p.id AND po.revision=p.revision AND po.counted=1 LEFT JOIN action_outcomes o ON o.agent_id=po.agent_id AND o.action_id=po.action_id WHERE p.agent_id=? ORDER BY p.id,po.action_id`, agentID)
	if err != nil {
		return nil, fmt.Errorf("reading playbook lifts: %w", err)
	}
	defer rows.Close()
	var out []PlaybookLift
	for rows.Next() {
		var l PlaybookLift
		if err = rows.Scan(&l.Hypothesis, &l.Status, &l.Kind, &l.Counted, &l.ReachLift, &l.ResonanceLift); err != nil {
			return nil, fmt.Errorf("reading playbook lift: %w", err)
		}
		out = append(out, l)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("reading playbook lifts: %w", err)
	}
	return out, nil
}

// PlaybookLifts returns the lifts the outcome model stored for every counted
// action of the agent's hypotheses, by hypothesis and action.
func (s *Store) PlaybookLifts(ctx context.Context, agentID string) ([]PlaybookLift, error) {
	return playbookLifts(ctx, s.db, agentID)
}

// Lifts is PlaybookLifts inside the writer transaction.
func (t PlaybookTx) Lifts(ctx context.Context, agentID string) ([]PlaybookLift, error) {
	return playbookLifts(ctx, t.conn, agentID)
}

func (t PlaybookTx) AgentStatus(ctx context.Context, agentID string) (string, error) {
	var status string
	if err := t.conn.QueryRowContext(ctx, `SELECT status FROM agents WHERE id=?`, agentID).Scan(&status); err != nil {
		return "", fmt.Errorf("reading playbook agent: %w", err)
	}
	return status, nil
}

func (t PlaybookTx) TestingRevision(ctx context.Context, agentID string, hypothesisID int64) (int64, error) {
	var revision int64
	if err := t.conn.QueryRowContext(ctx, `SELECT revision FROM playbook WHERE agent_id=? AND id=? AND status='testing'`, agentID, hypothesisID).Scan(&revision); err != nil {
		return 0, fmt.Errorf("binding testing revision: %w", err)
	}
	return revision, nil
}

func (t PlaybookTx) SnapshotRevision(ctx context.Context, agentID string, actionID, hypothesisID, revision int64) error {
	if _, err := t.conn.ExecContext(ctx, `INSERT INTO action_hypotheses(agent_id,action_id,hypothesis_id,revision) VALUES(?,?,?,?)`, agentID, actionID, hypothesisID, revision); err != nil {
		return fmt.Errorf("snapshotting testing revision: %w", err)
	}
	return nil
}

// Observations returns up to 1000 unprocessed measurements, oldest first.
func (t PlaybookTx) Observations(ctx context.Context, agentID string) ([]PlaybookObservation, error) {
	rows, err := t.conn.QueryContext(ctx, `SELECT h.action_id,h.hypothesis_id,h.revision,a.kind,a.posted_at,m.measured_at FROM action_hypotheses h JOIN actions a ON a.agent_id=h.agent_id AND a.id=h.action_id JOIN metric_jobs j ON j.agent_id=a.agent_id AND j.action_id=a.id AND j.window_hours=24 JOIN metrics m ON m.agent_id=j.agent_id AND m.action_id=j.action_id AND m.measured_at=j.completed_at JOIN action_outcomes o ON o.agent_id=a.agent_id AND o.action_id=a.id WHERE h.agent_id=? AND a.dry_run=0 AND trim(a.thread_url)<>'' AND NOT EXISTS(SELECT 1 FROM playbook_outcomes o WHERE o.agent_id=h.agent_id AND o.action_id=h.action_id AND o.hypothesis_id=h.hypothesis_id) ORDER BY substr(m.measured_at,1,19),substr(CASE WHEN substr(m.measured_at,20,1)='.' THEN substr(m.measured_at,21,length(m.measured_at)-21) ELSE '' END || '000000000',1,9),h.action_id,h.hypothesis_id LIMIT 1000`, agentID)
	if err != nil {
		return nil, fmt.Errorf("reading playbook observations: %w", err)
	}
	defer rows.Close()
	out := []PlaybookObservation{}
	for rows.Next() {
		var o PlaybookObservation
		if err = rows.Scan(&o.Action, &o.Hypothesis, &o.Revision, &o.Kind, &o.Posted, &o.Measured); err != nil {
			return nil, fmt.Errorf("reading playbook observation: %w", err)
		}
		out = append(out, o)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("reading playbook observations: %w", err)
	}
	return out, nil
}

func (t PlaybookTx) Counters(ctx context.Context, agentID string, hypothesisID int64) (PlaybookCounters, error) {
	var c PlaybookCounters
	err := t.conn.QueryRowContext(ctx, `SELECT revision,status,n,kind FROM playbook WHERE agent_id=? AND id=?`, agentID, hypothesisID).Scan(&c.Revision, &c.Status, &c.N, &c.Kind)
	if err != nil {
		return c, fmt.Errorf("reading outcome hypothesis: %w", err)
	}
	return c, nil
}

// Count stores the new counters and status of a still-testing revision and
// binds it to kind; decidedAt is nil while it stays testing.
func (t PlaybookTx) Count(ctx context.Context, agentID string, hypothesisID, revision, n int64, kind, status string, decidedAt *time.Time) error {
	var decided any
	if decidedAt != nil {
		decided = decidedAt.UTC().Format(time.RFC3339Nano)
	}
	if _, err := t.conn.ExecContext(ctx, `UPDATE playbook SET n=?,kind=?,status=?,decided_at=? WHERE agent_id=? AND id=? AND revision=? AND status='testing'`, n, kind, status, decided, agentID, hypothesisID, revision); err != nil {
		return fmt.Errorf("counting playbook outcome: %w", err)
	}
	return nil
}

func (t PlaybookTx) RecordDecision(ctx context.Context, agentID string, d PlaybookDecision) error {
	if _, err := t.conn.ExecContext(ctx, `INSERT INTO playbook_decisions(agent_id,hypothesis_id,revision,status,n,reason,decided_at) VALUES(?,?,?,?,?,?,?)`, agentID, d.Hypothesis, d.Revision, d.Status, d.N, d.Reason, d.DecidedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("recording playbook decision: %w", err)
	}
	return nil
}

func (t PlaybookTx) RecordOutcome(ctx context.Context, agentID string, o PlaybookOutcome) error {
	if _, err := t.conn.ExecContext(ctx, `INSERT INTO playbook_outcomes(agent_id,action_id,hypothesis_id,revision,measured_at,reason,counted) VALUES(?,?,?,?,?,?,?)`, agentID, o.Action, o.Hypothesis, o.Revision, o.MeasuredAt, o.Reason, o.Counted); err != nil {
		return fmt.Errorf("recording playbook outcome: %w", err)
	}
	return nil
}
