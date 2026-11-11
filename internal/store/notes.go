package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// maxAgentNotesSearchLimit bounds how many notes one search can return so a
// single tool call cannot pull an unbounded amount of the agent's history.
const maxAgentNotesSearchLimit = 100

// AgentNote is one note the agent wrote to itself. CreatedAt is always
// returned in UTC.
type AgentNote struct {
	ID        int64
	AgentID   string
	Text      string
	CreatedAt time.Time
}

// WriteAgentNote inserts a note and atomically updates the owning agent's
// self_note to the same text, so the next wake reads it without a separate
// search. The agent must already exist. Any failure, including the
// self_note update, rolls back both the note row and its FTS entry.
func (s *Store) WriteAgentNote(ctx context.Context, agentID string, text string, at time.Time) (int64, error) {
	const operation = "writing agent note"

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("%s for agent %q: beginning transaction: %w", operation, agentID, err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var marker int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE id = ?`, agentID).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%s for agent %q: agent not found: %w", operation, agentID, sql.ErrNoRows)
	}
	if err != nil {
		return 0, fmt.Errorf("%s for agent %q: checking agent: %w", operation, agentID, err)
	}

	result, err := tx.ExecContext(ctx,
		`INSERT INTO notes (agent_id, text, created_at) VALUES (?, ?, ?)`,
		agentID, text, formatStoreTimestamp(at))
	if err != nil {
		return 0, fmt.Errorf("%s for agent %q: inserting note: %w", operation, agentID, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("%s for agent %q: reading inserted note id: %w", operation, agentID, err)
	}

	result, err = tx.ExecContext(ctx,
		`UPDATE agents SET self_note = ? WHERE id = ?`, text, agentID)
	if err != nil {
		return 0, fmt.Errorf("%s for agent %q: updating self_note: %w", operation, agentID, err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("%s for agent %q: checking self_note update: %w", operation, agentID, err)
	}
	if updated != 1 {
		return 0, fmt.Errorf("%s for agent %q: agent not found: %w", operation, agentID, sql.ErrNoRows)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("%s for agent %q: committing transaction: %w", operation, agentID, err)
	}
	return id, nil
}

// SearchAgentNotes returns the selected agent's notes that match query through
// SQLite FTS5, newest first, with ties broken by note id. An empty query
// returns no notes. A non-positive limit is rejected; limits above the
// implementation cap are capped. query is always passed as a bound parameter,
// never interpolated into SQL.
func (s *Store) SearchAgentNotes(ctx context.Context, agentID string, query string, limit int) ([]AgentNote, error) {
	const operation = "searching agent notes"

	if limit <= 0 {
		return nil, fmt.Errorf("%s for agent %q: limit must be positive: %d", operation, agentID, limit)
	}
	if limit > maxAgentNotesSearchLimit {
		limit = maxAgentNotesSearchLimit
	}
	if strings.TrimSpace(query) == "" {
		return []AgentNote{}, nil
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT n.id, n.agent_id, n.text, n.created_at
		FROM notes_fts
		JOIN notes n ON n.id = notes_fts.rowid
		WHERE notes_fts MATCH ? AND n.agent_id = ?
		ORDER BY julianday(n.created_at) DESC, n.id DESC
		LIMIT ?
	`, query, agentID, limit)
	if err != nil {
		return nil, fmt.Errorf("%s for agent %q: querying FTS index: %w", operation, agentID, err)
	}
	defer rows.Close()

	notes := make([]AgentNote, 0)
	for rows.Next() {
		var note AgentNote
		var createdAt string
		if err := rows.Scan(&note.ID, &note.AgentID, &note.Text, &createdAt); err != nil {
			return nil, fmt.Errorf("%s for agent %q: scanning note: %w", operation, agentID, err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("%s for agent %q: parsing note %d created_at: %w", operation, agentID, note.ID, err)
		}
		note.CreatedAt = parsed.UTC()
		notes = append(notes, note)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s for agent %q: reading notes: %w", operation, agentID, err)
	}
	return notes, nil
}

func formatStoreTimestamp(at time.Time) string {
	return at.UTC().Format(time.RFC3339Nano)
}
