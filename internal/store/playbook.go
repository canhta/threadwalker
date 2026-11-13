package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxTestingHypotheses bounds the number of hypotheses an agent may test at
	// once. Amendments do not consume another slot.
	MaxTestingHypotheses = 15
	// MaxHypothesisTextRunes bounds one hypothesis in Unicode codepoints.
	MaxHypothesisTextRunes = 1000
)

// ErrPlaybookInput reports a rejected caller-supplied value: an unknown or
// non-running agent, a blank or oversized text, an invalid timestamp, or an
// invalid limit.
var ErrPlaybookInput = errors.New("playbook input rejected")

// ErrPlaybookCap reports that the agent already has MaxTestingHypotheses
// testing entries.
var ErrPlaybookCap = errors.New("playbook testing cap reached")

// ErrPlaybookImmutable reports a target that is missing, owned by another
// agent, or no longer testing. Its message never contains entry text or ids.
var ErrPlaybookImmutable = errors.New("playbook entry immutable")

// errPlaybookCorrupt marks persisted rows that the CRUD layer must not
// normalise or hand to callers: bad status, revision, counters or timestamps.
var errPlaybookCorrupt = errors.New("playbook state malformed")

// PlaybookEntry is the agent-facing view of one hypothesis. Status, counters
// and Revision are code-owned; callers cannot set them.
type PlaybookEntry struct {
	ID        int64      `json:"id"`
	AgentID   string     `json:"agent_id"`
	Text      string     `json:"text"`
	Status    string     `json:"status"`
	Revision  int64      `json:"revision"`
	N         int64      `json:"n"`
	CreatedAt time.Time  `json:"created_at"`
	DecidedAt *time.Time `json:"decided_at"`
	// KnowledgeID is the curated knowledge entry the hypothesis cites, or "".
	KnowledgeID string `json:"knowledge_id,omitempty"`
}

func playbookAgentID(agentID string) (string, error) {
	if strings.TrimSpace(agentID) == "" {
		return "", fmt.Errorf("%w: missing agent", ErrPlaybookInput)
	}
	return agentID, nil
}

func playbookText(text string) (string, error) {
	if !utf8.ValidString(text) {
		return "", fmt.Errorf("%w: text is not valid UTF-8", ErrPlaybookInput)
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", fmt.Errorf("%w: text is blank", ErrPlaybookInput)
	}
	if utf8.RuneCountInString(trimmed) > MaxHypothesisTextRunes {
		return "", fmt.Errorf("%w: text exceeds %d codepoints", ErrPlaybookInput, MaxHypothesisTextRunes)
	}
	return trimmed, nil
}

// playbookRunningAgent requires a known, currently running agent. Paused and
// killed agents may still read their playbook but cannot create new state.
func playbookRunningAgent(ctx context.Context, conn *sql.Conn, agentID string) error {
	var status string
	err := conn.QueryRowContext(ctx, `SELECT status FROM agents WHERE id = ?`, agentID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: unknown agent", ErrPlaybookInput)
	}
	if err != nil {
		return fmt.Errorf("reading playbook agent: %w", err)
	}
	switch status {
	case "running":
		return nil
	case "paused", "killed":
		return fmt.Errorf("%w: agent is %s", ErrPlaybookInput, status)
	default:
		return fmt.Errorf("reading playbook agent: %w: unknown agent status", errPlaybookCorrupt)
	}
}

func playbookStatusValid(status string) bool {
	return status == "testing" || status == "established" || status == "rejected"
}

type playbookScanner interface {
	Scan(dest ...any) error
}

// scanPlaybookEntry reads one persisted entry strictly. It never normalises a
// malformed status, revision, counter or timestamp into a plausible value.
func scanPlaybookEntry(scanner playbookScanner) (PlaybookEntry, error) {
	var entry PlaybookEntry
	var created string
	var decided sql.NullString
	if err := scanner.Scan(&entry.ID, &entry.AgentID, &entry.Text, &entry.Status, &entry.Revision, &entry.N, &created, &decided, &entry.KnowledgeID); err != nil {
		return PlaybookEntry{}, err
	}
	if entry.ID <= 0 {
		return PlaybookEntry{}, fmt.Errorf("%w: invalid entry id", errPlaybookCorrupt)
	}
	if entry.AgentID == "" {
		return PlaybookEntry{}, fmt.Errorf("%w: missing entry agent", errPlaybookCorrupt)
	}
	if !playbookStatusValid(entry.Status) {
		return PlaybookEntry{}, fmt.Errorf("%w: invalid entry status", errPlaybookCorrupt)
	}
	if entry.Revision < 1 {
		return PlaybookEntry{}, fmt.Errorf("%w: invalid entry revision", errPlaybookCorrupt)
	}
	if entry.N < 0 {
		return PlaybookEntry{}, fmt.Errorf("%w: invalid entry counters", errPlaybookCorrupt)
	}
	createdAt, err := parseTime(created)
	if err != nil {
		return PlaybookEntry{}, fmt.Errorf("%w: invalid created_at", errPlaybookCorrupt)
	}
	entry.CreatedAt = createdAt

	if entry.Status == "testing" {
		if decided.Valid {
			return PlaybookEntry{}, fmt.Errorf("%w: testing entry has decided_at", errPlaybookCorrupt)
		}
		entry.DecidedAt = nil
		return entry, nil
	}
	if !decided.Valid {
		return PlaybookEntry{}, fmt.Errorf("%w: decided entry lacks decided_at", errPlaybookCorrupt)
	}
	decidedAt, err := parseTime(decided.String)
	if err != nil {
		return PlaybookEntry{}, fmt.Errorf("%w: invalid decided_at", errPlaybookCorrupt)
	}
	entry.DecidedAt = &decidedAt
	return entry, nil
}

const playbookSelectColumns = `id, agent_id, text, status, revision, n, created_at, decided_at, knowledge_id`

// InitializePlaybook seeds one agent's playbook exactly once. It validates the
// whole request before writing, then records a persistent per-agent marker in
// the same transaction as the seed rows. Manual pre-existing rows are never
// reseeded or overwritten: they only receive the marker after a capacity check.
func (s *Store) InitializePlaybook(ctx context.Context, agentID string, texts []string, at time.Time) (bool, error) {
	const operation = "initializing playbook"

	agentID, err := playbookAgentID(agentID)
	if err != nil {
		return false, fmt.Errorf("%s: %w", operation, err)
	}
	if !validTime(at) {
		return false, fmt.Errorf("%s: %w: invalid timestamp", operation, ErrPlaybookInput)
	}
	if len(texts) > MaxTestingHypotheses {
		return false, fmt.Errorf("%s: %w", operation, ErrPlaybookCap)
	}
	trimmed := make([]string, 0, len(texts))
	for _, text := range texts {
		value, err := playbookText(text)
		if err != nil {
			return false, fmt.Errorf("%s: %w", operation, err)
		}
		trimmed = append(trimmed, value)
	}

	created := false
	err = s.write(ctx, func(conn *sql.Conn) error {
		if err := playbookRunningAgent(ctx, conn, agentID); err != nil {
			return fmt.Errorf("%s for agent %q: %w", operation, agentID, err)
		}

		var marker int
		err := conn.QueryRowContext(ctx,
			`SELECT 1 FROM playbook_initializations WHERE agent_id = ?`, agentID).Scan(&marker)
		switch {
		case err == nil:
			// Idempotent: the marker is authoritative even if rows changed or
			// disappeared since the first initialization.
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("%s for agent %q: reading initialization marker: %w", operation, agentID, err)
		}

		var total, testing int
		if err := conn.QueryRowContext(ctx,
			`SELECT COUNT(*), COALESCE(SUM(CASE WHEN status = 'testing' THEN 1 ELSE 0 END), 0)
			 FROM playbook WHERE agent_id = ?`, agentID).Scan(&total, &testing); err != nil {
			return fmt.Errorf("%s for agent %q: counting existing entries: %w", operation, agentID, err)
		}
		if testing > MaxTestingHypotheses {
			return fmt.Errorf("%s for agent %q: %w", operation, agentID, ErrPlaybookCap)
		}

		if total == 0 {
			created = true
			for _, text := range trimmed {
				if _, err := conn.ExecContext(ctx,
					`INSERT INTO playbook (agent_id, text, status, revision, n, created_at)
					 VALUES (?, ?, 'testing', 1, 0, ?)`,
					agentID, text, formatTime(at)); err != nil {
					return fmt.Errorf("%s for agent %q: inserting entry: %w", operation, agentID, err)
				}
			}
		} else {
			// Preexisting manual state wins; only the marker is added.
			created = true
		}

		if _, err := conn.ExecContext(ctx,
			`INSERT INTO playbook_initializations (agent_id, initialized_at) VALUES (?, ?)`,
			agentID, formatTime(at)); err != nil {
			return fmt.Errorf("%s for agent %q: recording initialization: %w", operation, agentID, err)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return created, nil
}

// PlaybookEntries returns up to limit of one agent's entries, testing first,
// then established, then rejected, and by id inside each status. A known agent
// with no entries yields a non-nil empty slice.
func (s *Store) PlaybookEntries(ctx context.Context, agentID string, limit int) ([]PlaybookEntry, error) {
	const operation = "listing playbook"

	agentID, err := playbookAgentID(agentID)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("%s: %w: limit must be between 1 and 100", operation, ErrPlaybookInput)
	}

	var marker int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE id = ?`, agentID).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%s: %w: unknown agent", operation, ErrPlaybookInput)
	}
	if err != nil {
		return nil, fmt.Errorf("%s for agent %q: checking agent: %w", operation, agentID, err)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT `+playbookSelectColumns+`
		FROM playbook
		WHERE agent_id = ?
		ORDER BY CASE status
			WHEN 'testing' THEN 0
			WHEN 'established' THEN 1
			WHEN 'rejected' THEN 2
			ELSE 3
		END, id
		LIMIT ?`, agentID, limit)
	if err != nil {
		return nil, fmt.Errorf("%s for agent %q: querying entries: %w", operation, agentID, err)
	}
	defer rows.Close()

	entries := make([]PlaybookEntry, 0)
	for rows.Next() {
		entry, err := scanPlaybookEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("%s for agent %q: reading entry: %w", operation, agentID, err)
		}
		if entry.AgentID != agentID {
			return nil, fmt.Errorf("%s for agent %q: %w: entry has another agent", operation, agentID, errPlaybookCorrupt)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s for agent %q: reading entries: %w", operation, agentID, err)
	}
	return entries, nil
}

// ProposePlaybook adds a testing entry when entryID is zero, or amends the text
// of the caller's own testing entry when entryID is positive. Amendments bump
// the revision and reset code-owned evidence. Identical text is a no-op.
// knowledgeID, when not empty, records the curated knowledge entry the
// hypothesis cites; an amendment without one keeps the earlier citation, and
// changing only the citation keeps the revision and counters.
// Status, counters and deletion are unreachable from this API.
func (s *Store) ProposePlaybook(ctx context.Context, agentID string, entryID int64, text string, at time.Time, knowledgeID string) (PlaybookEntry, error) {
	const operation = "proposing playbook entry"

	agentID, err := playbookAgentID(agentID)
	if err != nil {
		return PlaybookEntry{}, fmt.Errorf("%s: %w", operation, err)
	}
	if entryID < 0 {
		return PlaybookEntry{}, fmt.Errorf("%s: %w: entry id must not be negative", operation, ErrPlaybookInput)
	}
	if !validTime(at) {
		return PlaybookEntry{}, fmt.Errorf("%s: %w: invalid timestamp", operation, ErrPlaybookInput)
	}
	text, err = playbookText(text)
	if err != nil {
		return PlaybookEntry{}, fmt.Errorf("%s: %w", operation, err)
	}

	var result PlaybookEntry
	err = s.write(ctx, func(conn *sql.Conn) error {
		if err := playbookRunningAgent(ctx, conn, agentID); err != nil {
			return fmt.Errorf("%s for agent %q: %w", operation, agentID, err)
		}

		if entryID == 0 {
			var testing int
			if err := conn.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM playbook WHERE agent_id = ? AND status = 'testing'`, agentID).Scan(&testing); err != nil {
				return fmt.Errorf("%s for agent %q: counting testing entries: %w", operation, agentID, err)
			}
			if testing >= MaxTestingHypotheses {
				return fmt.Errorf("%s for agent %q: %w", operation, agentID, ErrPlaybookCap)
			}
			inserted, err := conn.ExecContext(ctx,
				`INSERT INTO playbook (agent_id, text, status, revision, n, created_at, knowledge_id)
				 VALUES (?, ?, 'testing', 1, 0, ?, ?)`,
				agentID, text, formatTime(at), knowledgeID)
			if err != nil {
				return fmt.Errorf("%s for agent %q: inserting entry: %w", operation, agentID, err)
			}
			entryID, err = inserted.LastInsertId()
			if err != nil {
				return fmt.Errorf("%s for agent %q: reading inserted id: %w", operation, agentID, err)
			}
		} else {
			existing, err := scanPlaybookEntry(conn.QueryRowContext(ctx,
				`SELECT `+playbookSelectColumns+` FROM playbook WHERE id = ? AND agent_id = ?`,
				entryID, agentID))
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%s for agent %q: %w", operation, agentID, ErrPlaybookImmutable)
			}
			if err != nil {
				if errors.Is(err, errPlaybookCorrupt) {
					return fmt.Errorf("%s for agent %q: %w", operation, agentID, err)
				}
				return fmt.Errorf("%s for agent %q: reading entry: %w", operation, agentID, err)
			}
			if existing.Status != "testing" {
				return fmt.Errorf("%s for agent %q: %w", operation, agentID, ErrPlaybookImmutable)
			}
			textChanged := existing.Text != text
			if !textChanged && (knowledgeID == "" || knowledgeID == existing.KnowledgeID) {
				result = existing
				return nil
			}
			reset := ""
			if textChanged {
				reset = ", revision = revision + 1, n = 0, kind = ''"
			}
			updated, err := conn.ExecContext(ctx, `
				UPDATE playbook
				SET text = ?, knowledge_id = COALESCE(NULLIF(?, ''), knowledge_id)`+reset+`
				WHERE id = ? AND agent_id = ? AND status = 'testing' AND revision = ?`,
				text, knowledgeID, entryID, agentID, existing.Revision)
			if err != nil {
				return fmt.Errorf("%s for agent %q: amending entry: %w", operation, agentID, err)
			}
			changed, err := updated.RowsAffected()
			if err != nil {
				return fmt.Errorf("%s for agent %q: checking amendment: %w", operation, agentID, err)
			}
			if changed != 1 {
				return fmt.Errorf("%s for agent %q: entry changed concurrently", operation, agentID)
			}
		}

		entry, err := scanPlaybookEntry(conn.QueryRowContext(ctx,
			`SELECT `+playbookSelectColumns+` FROM playbook WHERE id = ? AND agent_id = ?`,
			entryID, agentID))
		if err != nil {
			return fmt.Errorf("%s for agent %q: reading result entry: %w", operation, agentID, err)
		}
		result = entry
		return nil
	})
	if err != nil {
		return PlaybookEntry{}, err
	}
	return result, nil
}
