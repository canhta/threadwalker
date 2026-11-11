package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type OwnerMessage struct {
	ID         int64  `json:"id"`
	AgentID    string `json:"agent_id"`
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	AnsweredID int64  `json:"answered_id,omitempty"`
}

// QueueOwner is local-only. Event keys make control replies, notices and digests
// idempotent across restarts; ordinary tool messages use a NULL event key.
func (s *Store) QueueOwner(ctx context.Context, agentID, kind, text, key string, at time.Time) (int64, error) {
	if text == "" || len(text) > 12000 {
		return 0, errors.New("queueing owner message: invalid text size")
	}
	var event any
	if key != "" {
		event = key
	}
	r, err := s.db.ExecContext(ctx, `INSERT INTO owner_msgs(agent_id,direction,text,at,kind,event_key) SELECT ?,'out',?,?,?,? WHERE EXISTS(SELECT 1 FROM agents WHERE id=?) ON CONFLICT(agent_id,event_key) DO NOTHING`, agentID, text, formatStoreTimestamp(at), kind, event, agentID)
	if err != nil {
		return 0, fmt.Errorf("queueing owner message: %w", err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("checking owner queue: %w", err)
	}
	if n == 0 {
		var id int64
		if key != "" {
			err = s.db.QueryRowContext(ctx, `SELECT id FROM owner_msgs WHERE agent_id=? AND event_key=?`, agentID, key).Scan(&id)
		} else {
			err = sql.ErrNoRows
		}
		if err != nil {
			return 0, fmt.Errorf("finding owner queue entry: %w", err)
		}
		return id, nil
	}
	id, err := r.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("identifying owner message: %w", err)
	}
	return id, nil
}

func (s *Store) PendingOwner(ctx context.Context, ids []string) ([]OwnerMessage, error) {
	out := []OwnerMessage{}
	for _, id := range ids {
		rows, err := s.db.QueryContext(ctx, `SELECT id,agent_id,kind,text FROM owner_msgs WHERE agent_id=? AND direction='out' AND delivery='queued' ORDER BY id LIMIT 100`, id)
		if err != nil {
			return nil, fmt.Errorf("reading owner outbox: %w", err)
		}
		for rows.Next() {
			var m OwnerMessage
			if err = rows.Scan(&m.ID, &m.AgentID, &m.Kind, &m.Text); err != nil {
				rows.Close()
				return nil, fmt.Errorf("reading owner outbox row: %w", err)
			}
			out = append(out, m)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("iterating owner outbox: %w", err)
		}
	}
	return out, nil
}

// Claim before HTTP: sending rows are never retried after crashes or uncertain
// network outcomes. Telegram has no sendMessage idempotency key.
func (s *Store) ClaimOwner(ctx context.Context, id int64, botKey string) (bool, error) {
	r, err := s.db.ExecContext(ctx, `UPDATE owner_msgs SET delivery='sending',bot_key=? WHERE id=? AND delivery='queued'`, botKey, id)
	if err != nil {
		return false, fmt.Errorf("claiming owner delivery: %w", err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("checking owner delivery claim: %w", err)
	}
	return n == 1, nil
}
func (s *Store) FinishOwner(ctx context.Context, id, messageID int64, sent bool) error {
	status := "failed"
	if sent {
		status = "sent"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE owner_msgs SET delivery=?,message_id=? WHERE id=? AND delivery='sending'`, status, messageID, id)
	if err != nil {
		return fmt.Errorf("finishing owner delivery: %w", err)
	}
	return nil
}

// AnswerOwner accepts exactly one answer to a delivered question in this bot's
// configured agent group. It never stores unknown replies or transport profiles.
func (s *Store) AnswerOwner(ctx context.Context, ids []string, botKey string, messageID int64, key, text string, at time.Time) (bool, error) {
	if len(ids) == 0 || text == "" || len(text) > 12000 {
		return false, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := []any{messageID, botKey}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,agent_id FROM owner_msgs WHERE message_id=? AND bot_key=? AND direction='out' AND kind='question' AND delivery='sent' AND agent_id IN (`+marks+`)`, args...)
	if err != nil {
		return false, fmt.Errorf("finding owner question: %w", err)
	}
	var id int64
	var agentID string
	count := 0
	for rows.Next() {
		if err = rows.Scan(&id, &agentID); err != nil {
			rows.Close()
			return false, fmt.Errorf("reading owner question: %w", err)
		}
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, fmt.Errorf("iterating owner questions: %w", err)
	}
	if count != 1 {
		return false, nil
	}
	r, err := s.db.ExecContext(ctx, `INSERT INTO owner_msgs(agent_id,direction,text,answered_id,at,kind,event_key,delivery) SELECT ?,'in',?,?,?,'answer',?,'received' WHERE NOT EXISTS(SELECT 1 FROM owner_msgs WHERE answered_id=? AND direction='in') ON CONFLICT(agent_id,event_key) DO NOTHING`, agentID, text, id, formatStoreTimestamp(at), key, id)
	if err != nil {
		return false, fmt.Errorf("saving owner answer: %w", err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("checking owner answer write: %w", err)
	}
	return n == 1, nil
}
func (s *Store) OwnerAnswers(ctx context.Context, id string, limit int) ([]OwnerMessage, error) {
	if limit < 1 || limit > 20 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,agent_id,kind,text,answered_id FROM owner_msgs WHERE agent_id=? AND direction='in' AND kind='answer' ORDER BY id DESC LIMIT ?`, id, limit)
	if err != nil {
		return nil, fmt.Errorf("reading owner answers: %w", err)
	}
	defer rows.Close()
	out := []OwnerMessage{}
	for rows.Next() {
		var m OwnerMessage
		if err = rows.Scan(&m.ID, &m.AgentID, &m.Kind, &m.Text, &m.AnsweredID); err != nil {
			return nil, fmt.Errorf("reading owner answer: %w", err)
		}
		out = append(out, m)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating owner answers: %w", err)
	}
	return out, nil
}
func (s *Store) OwnerOffset(ctx context.Context, agentID, key string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(next_update),0) FROM owner_poll WHERE bot_key=?`, key).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading owner poll offset: %w", err)
	}
	return n, nil
}
func (s *Store) SetOwnerOffset(ctx context.Context, agentID, key string, n int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO owner_poll(agent_id,bot_key,next_update) VALUES(?,?,?) ON CONFLICT(agent_id,bot_key) DO UPDATE SET next_update=MAX(next_update,excluded.next_update)`, agentID, key, n)
	if err != nil {
		return fmt.Errorf("saving owner poll offset: %w", err)
	}
	return nil
}

// OwnerControl atomically commits a state change and its deduplicated receipt.
// Killed agents cannot be resumed; only operator database recovery can revive one.
func (s *Store) OwnerControl(ctx context.Context, id, command, key, text string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning owner control: %w", err)
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, `INSERT INTO owner_msgs(agent_id,direction,text,at,kind,event_key) VALUES(?,'out',?,?,'command',?) ON CONFLICT(agent_id,event_key) DO NOTHING`, id, text, formatStoreTimestamp(at), key)
	if err != nil {
		return fmt.Errorf("recording owner control: %w", err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking owner receipt: %w", err)
	}
	if n > 0 {
		status := ""
		switch command {
		case "pause":
			status = "paused"
		case "resume":
			status = "running"
		case "kill":
			status = "killed"
		}
		if status != "" {
			if _, err = tx.ExecContext(ctx, `UPDATE agents SET status=? WHERE id=? AND status!='killed'`, status, id); err != nil {
				return fmt.Errorf("applying owner control: %w", err)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("committing owner control: %w", err)
	}
	return nil
}

type OwnerSummary struct {
	Status                                                                           string
	NextWake                                                                         time.Time
	Posts, Replies, Views, Likes, Responses, Reposts, PlaybookChanges, MetricSamples int
	CostUSD                                                                          float64
}

// PauseOwnerAgent is an automatic harness control, independent of the model
// guard. It cannot revive killed agents or create an unconfigured agent.
func (s *Store) PauseOwnerAgent(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET status='paused' WHERE id=? AND status='running'`, id)
	if err != nil {
		return fmt.Errorf("pausing owner agent: %w", err)
	}
	return nil
}

func (s *Store) OwnerSummary(ctx context.Context, id string, from, to time.Time) (OwnerSummary, error) {
	var out OwnerSummary
	state, err := s.AgentState(ctx, id)
	if err != nil {
		return out, err
	}
	out.Status = state.Status
	out.NextWake = state.NextWake
	usage, err := s.LLMUsageForDay(ctx, id, from.UTC().Format("2006-01-02"))
	if err != nil {
		return out, err
	}
	out.CostUSD = usage.CostUSD
	counts, err := s.CountActions(ctx, id, from, to)
	out.Posts, out.Replies = counts.Posts, counts.Replies
	if err != nil {
		return out, fmt.Errorf("counting owner summary actions: %w", err)
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(m.views),0),COALESCE(SUM(m.likes),0),COALESCE(SUM(m.replies),0),COALESCE(SUM(m.reposts),0) FROM metrics m JOIN actions a ON a.id=m.action_id AND a.agent_id=m.agent_id WHERE m.agent_id=? AND julianday(m.measured_at)>=julianday(?) AND julianday(m.measured_at)<julianday(?) AND m.measured_at=(SELECT MAX(m2.measured_at) FROM metrics m2 WHERE m2.agent_id=m.agent_id AND m2.action_id=m.action_id AND julianday(m2.measured_at)<julianday(?))`, id, formatStoreTimestamp(from), formatStoreTimestamp(to), formatStoreTimestamp(to)).Scan(&out.MetricSamples, &out.Views, &out.Likes, &out.Responses, &out.Reposts)
	if err != nil {
		return out, fmt.Errorf("counting owner summary metrics: %w", err)
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM playbook WHERE agent_id=? AND ((julianday(created_at)>=julianday(?) AND julianday(created_at)<julianday(?)) OR (julianday(decided_at)>=julianday(?) AND julianday(decided_at)<julianday(?)))`, id, formatStoreTimestamp(from), formatStoreTimestamp(to), formatStoreTimestamp(from), formatStoreTimestamp(to)).Scan(&out.PlaybookChanges)
	if err != nil {
		return out, fmt.Errorf("counting owner playbook changes: %w", err)
	}
	return out, nil
}

func (s *Store) OwnerEventID(ctx context.Context, id, key string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM owner_msgs WHERE agent_id=? AND event_key=?`, id, key).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("reading owner event identity: %w", err)
	}
	return n, nil
}
