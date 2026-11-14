package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ReservationTx is the guard's single writer transaction: the check, the
// action intent and the contact counters commit together or not at all.
type ReservationTx struct{ conn *sql.Conn }

// Reserve runs fn in one BEGIN IMMEDIATE transaction, serialized across
// stores and processes. A nil return commits, so a refusal is recorded by
// returning nil after MarkBlocked.
func (s *Store) Reserve(ctx context.Context, fn func(ReservationTx) error) error {
	return s.write(ctx, func(conn *sql.Conn) error { return fn(ReservationTx{conn}) })
}

// ReservedAction is one recorded action intent with its stored timestamp text.
type ReservedAction struct {
	Agent, Kind, Text, PostedAt, TargetUsername, TargetURL, ThreadURL string
	// Browser marks a reply sent through the browser session.
	Browser bool
}

// Actions lists every agent's action intents of one mode (dry-run or live),
// oldest first, with the later parts of a series joined to the text: dry-run rows never touched the account, so live checks ignore them.
func (t ReservationTx) Actions(ctx context.Context, dryRun bool) ([]ReservedAction, error) {
	rows, err := t.conn.QueryContext(ctx, `SELECT agent_id,kind,text||COALESCE(char(10)||(SELECT group_concat(text,char(10)) FROM (SELECT text FROM action_parts WHERE action_id=actions.id ORDER BY part)),''),posted_at,target_username,target_url,thread_url,browser FROM actions WHERE dry_run=? AND failed=0 ORDER BY id`, dryRun)
	if err != nil {
		return nil, fmt.Errorf("reading action intents: %w", err)
	}
	defer rows.Close()
	var out []ReservedAction
	for rows.Next() {
		var a ReservedAction
		if err = rows.Scan(&a.Agent, &a.Kind, &a.Text, &a.PostedAt, &a.TargetUsername, &a.TargetURL, &a.ThreadURL, &a.Browser); err != nil {
			return nil, fmt.Errorf("reading action intents: %w", err)
		}
		out = append(out, a)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("reading action intents: %w", err)
	}
	return out, nil
}

// AgentWakeOpen reports whether the agent is running and the wake is still open.
func (t ReservationTx) AgentWakeOpen(ctx context.Context, agentID string, wakeID int64) (running, open bool, err error) {
	err = t.conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=? AND status='running'),EXISTS(SELECT 1 FROM wakes WHERE id=? AND agent_id=? AND ended_at IS NULL)`, agentID, wakeID, agentID).Scan(&running, &open)
	if err != nil {
		err = fmt.Errorf("reading agent and wake state: %w", err)
	}
	return
}

// ResetReplyCounts zeroes the materialized counters, keeping every permanent flag.
func (t ReservationTx) ResetReplyCounts(ctx context.Context, agentID string) error {
	if _, err := t.conn.ExecContext(ctx, `UPDATE contacts SET reply_count_7d=0 WHERE agent_id=?`, agentID); err != nil {
		return fmt.Errorf("resetting reply counters: %w", err)
	}
	return nil
}

// SetReplyCount stores a user's recomputed reply count.
func (t ReservationTx) SetReplyCount(ctx context.Context, agentID, username string, n int) error {
	if _, err := t.conn.ExecContext(ctx, `INSERT INTO contacts(agent_id,username,reply_count_7d) VALUES(?,?,?) ON CONFLICT(agent_id,username) DO UPDATE SET reply_count_7d=excluded.reply_count_7d`, agentID, username, n); err != nil {
		return fmt.Errorf("storing reply counter: %w", err)
	}
	return nil
}

// AddReply counts one more reservation against a user.
func (t ReservationTx) AddReply(ctx context.Context, agentID, username string) error {
	if _, err := t.conn.ExecContext(ctx, `INSERT INTO contacts(agent_id,username,reply_count_7d) VALUES(?,?,1) ON CONFLICT(agent_id,username) DO UPDATE SET reply_count_7d=contacts.reply_count_7d+1`, agentID, username); err != nil {
		return fmt.Errorf("counting reply: %w", err)
	}
	return nil
}

// DoNotContact reports a recorded do-not-contact flag.
func (t ReservationTx) DoNotContact(ctx context.Context, agentID, username string) (bool, error) {
	var dnc bool
	if err := t.conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM contacts WHERE agent_id=? AND username=? AND do_not_contact=1)`, agentID, username).Scan(&dnc); err != nil {
		return false, fmt.Errorf("reading do-not-contact: %w", err)
	}
	return dnc, nil
}

// NewIntent is one action intent to reserve.
type NewIntent struct {
	AgentID, Kind, Text, TargetURL, TargetUsername string
	// Hook is the hook type of the first line, or empty. Parts are parts 2.. of a series.
	Hook  string
	Parts []string
	// Rationale is the one-line pre-publish check of a post: trend, hook, why a reader replies.
	Rationale string
	// TrendRef is the trend a post anchored to; AnswersNote the open owner note it answers, or 0.
	TrendRef    string
	AnswersNote int64
	// Angle and Fact are the agent's brief to the writer; LessonIDs are the
	// active lessons it says it applied, validated by LessonsFit.
	Angle, Fact string
	LessonIDs   []int64
	// CriticJSON is the pre-publish critic verdict, or empty.
	CriticJSON string
	// RootURL is the canonical URL of the root post of the conversation a reply
	// belongs to; empty for a post.
	RootURL         string
	DryRun, Browser bool
	At              time.Time
	// TargetScoreJSON describes a reply's target; empty when it was never read.
	// TopicHeat and the parent figures are the posting context; nil when not
	// observed.
	TargetScoreJSON                 string
	ParentReplies, ParentAgeMinutes *int64
	TopicHeat                       *float64
}

// InsertIntent records the reservation and returns its action id.
func (t ReservationTx) InsertIntent(ctx context.Context, n NewIntent) (int64, error) {
	res, err := t.conn.ExecContext(ctx, `INSERT INTO actions(agent_id,kind,text,target_url,target_username,dry_run,posted_at,thread_url,target_score_json,topic_heat,followers_at_post,parent_replies,parent_age_minutes,hook,rationale,browser,trend_ref,answers_note,critic_json,root_url,angle,fact) VALUES(?,?,?,?,?,?,?,?,?,?,(SELECT followers FROM account_followers WHERE agent_id=?),?,?,?,?,?,?,?,?,?,?,?)`, n.AgentID, n.Kind, n.Text, n.TargetURL, n.TargetUsername, n.DryRun, n.At.Format(time.RFC3339Nano), "", n.TargetScoreJSON, n.TopicHeat, n.AgentID, n.ParentReplies, n.ParentAgeMinutes, n.Hook, n.Rationale, n.Browser, n.TrendRef, n.AnswersNote, n.CriticJSON, n.RootURL, CapRunes(n.Angle, MaxTraceText), CapRunes(n.Fact, MaxTraceText))
	if err != nil {
		return 0, fmt.Errorf("reserving action intent: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("reserving action intent: %w", err)
	}
	for i, text := range n.Parts {
		if _, err = t.conn.ExecContext(ctx, `INSERT INTO action_parts(agent_id,action_id,part,text) VALUES(?,?,?,?)`, n.AgentID, id, i+2, text); err != nil {
			return 0, fmt.Errorf("reserving series part: %w", err)
		}
	}
	for _, lesson := range n.LessonIDs {
		if _, err = t.conn.ExecContext(ctx, `INSERT INTO action_lessons(action_id,lesson_id) VALUES(?,?)`, id, lesson); err != nil {
			return 0, fmt.Errorf("recording applied lesson: %w", err)
		}
	}
	return id, nil
}

// MarkBlocked records why an audited attempt was refused.
func (t ReservationTx) MarkBlocked(ctx context.Context, auditID int64, reason string) error {
	if _, err := t.conn.ExecContext(ctx, `UPDATE tool_calls SET blocked_by=? WHERE id=?`, reason, auditID); err != nil {
		return fmt.Errorf("recording refusal: %w", err)
	}
	return nil
}

// ClearBlocked empties the refusal on an audited attempt that went on to succeed.
func (s *Store) ClearBlocked(ctx context.Context, auditID int64) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE tool_calls SET blocked_by='' WHERE id=?`, auditID); err != nil {
		return fmt.Errorf("clearing refusal: %w", err)
	}
	return nil
}

// AgentWakeOpen is the non-transactional state read behind the first audit row.
func (s *Store) AgentWakeOpen(ctx context.Context, agentID string, wakeID int64) (running, open bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=? AND status='running'), EXISTS(SELECT 1 FROM wakes WHERE id=? AND agent_id=? AND ended_at IS NULL)`, agentID, wakeID, agentID).Scan(&running, &open)
	if err != nil {
		err = fmt.Errorf("reading agent and wake state: %w", err)
	}
	return
}

// RecordToolAttempt commits the audit row before any effect runs.
func (s *Store) RecordToolAttempt(ctx context.Context, agentID string, wakeID int64, tool, metadataJSON, blockedBy string, at time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO tool_calls(agent_id,wake_id,tool,args_json,blocked_by,at) VALUES(?,?,?,?,?,?)`, agentID, wakeID, tool, metadataJSON, blockedBy, at.Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("recording tool attempt: %w", err)
	}
	return res.LastInsertId()
}

// RecordToolResult stores the outcome of an audited attempt.
func (s *Store) RecordToolResult(ctx context.Context, id int64, resultJSON string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE tool_calls SET result_json=? WHERE id=?`, resultJSON, id); err != nil {
		return fmt.Errorf("recording tool result: %w", err)
	}
	return nil
}

// RecentPost is an own post of the last day: the trend it anchored to and its text.
type RecentPost struct{ TrendRef, Text string }

// RecentPosts lists the agent's own posts of one mode published since the given time.
func (t ReservationTx) RecentPosts(ctx context.Context, agentID string, dryRun bool, since time.Time) ([]RecentPost, error) {
	rows, err := t.conn.QueryContext(ctx, `SELECT trend_ref,text FROM actions WHERE agent_id=? AND kind='post' AND failed=0 AND dry_run=? AND julianday(posted_at)>julianday(?)`, agentID, dryRun, since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("reading recent posts: %w", err)
	}
	defer rows.Close()
	var out []RecentPost
	for rows.Next() {
		var p RecentPost
		if err = rows.Scan(&p.TrendRef, &p.Text); err != nil {
			return nil, fmt.Errorf("reading recent post: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// OpenOwnerNotes is OpenOwnerNotes inside the reservation transaction.
func (t ReservationTx) OpenOwnerNotes(ctx context.Context, agentID string, now time.Time) ([]OwnerMessage, error) {
	return openOwnerNotes(ctx, t.conn, agentID, now)
}
