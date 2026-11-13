package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Transcript kinds. A wake's transcript is these entries in sequence order.
const (
	TranscriptContext    = "context"
	TranscriptAssistant  = "assistant"
	TranscriptToolResult = "tool_result"
	TranscriptUsage      = "usage"
	TranscriptEnd        = "end"
	TranscriptTruncated  = "truncated"
)

const (
	// MaxTranscriptEntries and MaxTranscriptBytes bound one wake's transcript.
	// The entry that crosses a bound is replaced by one truncated marker.
	MaxTranscriptEntries = 400
	MaxTranscriptBytes   = 2 << 20
)

// TranscriptEntry is one redacted record of a wake.
type TranscriptEntry struct {
	Seq  int
	Kind string
	Body string
	At   time.Time
}

// AppendTranscript stores one entry of a wake's transcript. Callers redact the
// body first. Once the wake's bounds are reached it keeps a single truncated
// marker and drops everything else, except the end entry.
func (s *Store) AppendTranscript(ctx context.Context, agentID string, wakeID int64, kind, body string, at time.Time) error {
	return s.write(ctx, func(conn *sql.Conn) error {
		var n, size, truncated int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(LENGTH(body)),0),COALESCE(SUM(kind=?),0) FROM wake_transcripts WHERE wake_id=?`, TranscriptTruncated, wakeID).Scan(&n, &size, &truncated); err != nil {
			return fmt.Errorf("reading transcript size: %w", err)
		}
		if kind != TranscriptEnd && (n >= MaxTranscriptEntries-2 || size+len(body) > MaxTranscriptBytes) {
			if truncated > 0 {
				return nil
			}
			kind, body = TranscriptTruncated, ""
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO wake_transcripts(agent_id,wake_id,seq,kind,body,at) VALUES(?,?,?,?,?,?)`, agentID, wakeID, n+1, kind, body, formatTime(at)); err != nil {
			return fmt.Errorf("appending transcript: %w", err)
		}
		return nil
	})
}

// Transcript returns a wake's entries in order for one agent.
func (s *Store) Transcript(ctx context.Context, agentID string, wakeID int64) ([]TranscriptEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq,kind,body,at FROM wake_transcripts WHERE agent_id=? AND wake_id=? ORDER BY seq`, agentID, wakeID)
	if err != nil {
		return nil, fmt.Errorf("reading transcript: %w", err)
	}
	defer rows.Close()
	var out []TranscriptEntry
	for rows.Next() {
		var e TranscriptEntry
		var at string
		if err = rows.Scan(&e.Seq, &e.Kind, &e.Body, &at); err != nil {
			return nil, fmt.Errorf("reading transcript entry: %w", err)
		}
		if e.At, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("reading transcript time: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// WakeRow is one wake as the operator lists it.
type WakeRow struct {
	ID        int64
	Kind      string
	StartedAt time.Time
	EndedAt   time.Time
	Steps     int
	EndReason string
	CostUSD   float64
}

// Wakes lists an agent's newest wakes started at or after since, newest first.
func (s *Store) Wakes(ctx context.Context, agentID string, since time.Time, limit int) ([]WakeRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT w.id,w.kind,w.started_at,COALESCE(w.ended_at,''),w.steps,w.end_reason,w.cost_usd
		FROM wakes w WHERE w.agent_id=? AND julianday(w.started_at)>=julianday(?) ORDER BY w.id DESC LIMIT ?`, agentID, formatTime(since), limit)
	if err != nil {
		return nil, fmt.Errorf("listing wakes: %w", err)
	}
	defer rows.Close()
	var out []WakeRow
	for rows.Next() {
		var w WakeRow
		var started, ended string
		if err = rows.Scan(&w.ID, &w.Kind, &started, &ended, &w.Steps, &w.EndReason, &w.CostUSD); err != nil {
			return nil, fmt.Errorf("reading wake row: %w", err)
		}
		if w.StartedAt, err = parseTime(started); err != nil {
			return nil, fmt.Errorf("reading wake start: %w", err)
		}
		if ended != "" {
			if w.EndedAt, err = parseTime(ended); err != nil {
				return nil, fmt.Errorf("reading wake end: %w", err)
			}
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// GuardDecision is one audited tool call of a wake and the guard that refused it, if any.
type GuardDecision struct {
	Tool      string
	BlockedBy string
	At        time.Time
}

// GuardDecisions lists a wake's audited tool calls in order.
func (s *Store) GuardDecisions(ctx context.Context, agentID string, wakeID int64) ([]GuardDecision, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tool,blocked_by,at FROM tool_calls WHERE agent_id=? AND wake_id=? ORDER BY id`, agentID, wakeID)
	if err != nil {
		return nil, fmt.Errorf("reading guard decisions: %w", err)
	}
	defer rows.Close()
	var out []GuardDecision
	for rows.Next() {
		var d GuardDecision
		var at string
		if err = rows.Scan(&d.Tool, &d.BlockedBy, &at); err != nil {
			return nil, fmt.Errorf("reading guard decision: %w", err)
		}
		if d.At, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("reading guard decision time: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
