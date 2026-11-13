package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxKnowledgeCandidates bounds the candidate techniques kept per agent.
	MaxKnowledgeCandidates = 50
	// MaxCandidateRunes bounds one candidate technique in Unicode codepoints.
	MaxCandidateRunes = 500
)

// ErrCandidateInput reports a rejected candidate: blank or oversized text, a
// source that is not an http(s) URL, a duplicate source, or a full list.
var ErrCandidateInput = errors.New("knowledge candidate rejected")

// KnowledgeCandidate is a technique the agent found while studying. Its level
// is always practitioner and it is never part of the curated knowledge file.
type KnowledgeCandidate struct {
	ID        int64
	Technique string
	SourceURL string
	CreatedAt time.Time
}

// AddKnowledgeCandidate stores one candidate technique with its source URL.
func (s *Store) AddKnowledgeCandidate(ctx context.Context, agentID, technique, sourceURL string, at time.Time) (int64, error) {
	technique = strings.TrimSpace(technique)
	u, err := url.Parse(sourceURL)
	switch {
	case technique == "" || !utf8.ValidString(technique) || utf8.RuneCountInString(technique) > MaxCandidateRunes:
		return 0, fmt.Errorf("adding knowledge candidate: %w: technique must be 1..%d characters", ErrCandidateInput, MaxCandidateRunes)
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		return 0, fmt.Errorf("adding knowledge candidate: %w: source must be an http(s) URL", ErrCandidateInput)
	case !validTime(at):
		return 0, fmt.Errorf("adding knowledge candidate: %w: invalid timestamp", ErrCandidateInput)
	}
	var id int64
	err = s.write(ctx, func(conn *sql.Conn) error {
		if err := playbookRunningAgent(ctx, conn, agentID); err != nil {
			return fmt.Errorf("%w: %v", ErrCandidateInput, err)
		}
		var n int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_candidates WHERE agent_id = ?`, agentID).Scan(&n); err != nil {
			return err
		}
		if n >= MaxKnowledgeCandidates {
			return fmt.Errorf("%w: list is full", ErrCandidateInput)
		}
		res, err := conn.ExecContext(ctx,
			`INSERT INTO knowledge_candidates (agent_id, technique, source_url, created_at) VALUES (?, ?, ?, ?)`,
			agentID, technique, sourceURL, formatTime(at))
		if err != nil {
			return fmt.Errorf("%w: source already submitted", ErrCandidateInput)
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("adding knowledge candidate: %w", err)
	}
	return id, nil
}
