package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// The memory index is loaded into every wake, so its size is the per-wake
// prompt cost: at most MaxIndexLines lines and MaxIndexBytes bytes of live
// entries. Topic notes are read on demand and are not counted.
const (
	MaxIndexLines     = 60
	MaxIndexBytes     = 6 << 10
	MaxEntryRunes     = 500
	MaxEntryLines     = 5
	MaxTopicRunes     = 64
	maxTopicDescRunes = 200
	// The topic list shown each wake is title plus description only.
	MaxTopicList      = 40
	MaxTopicListRunes = 2000
)

var (
	errEntryTooLong   = errors.New("memory entry too long")
	errSupersedeCap   = errors.New("supersede cap reached")
	errSupersedeOrder = errors.New("memory entries must be two live entries, the superseded one older")
	errRestoreEntry   = errors.New("journal row is not a restorable archive")
	errRestoreExpired = errors.New("memory entry has expired")
)

// Journal change kinds.
const (
	ChangeAdded      = "added"
	ChangeOverflow   = "overflow"
	ChangeExpired    = "expired"
	ChangeSuperseded = "superseded"
	ChangeRestored   = "restored"
)

// MemoryActor says which wake and phase made a memory change; the operator
// CLI uses the zero value with phase "operator".
type MemoryActor struct {
	WakeID int64
	Phase  string
}

// MemoryEntry is one live index entry as the wake shows it.
type MemoryEntry struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
}

// JournalEntry is one row of the memory journal.
type JournalEntry struct {
	ID      int64
	EntryID int64
	Change  string
	OldText string
	NewText string
	WakeID  int64
	Phase   string
	At      string
}

// An entry ending " · until YYYY-MM-DD" stops loading the day after that date.
var untilPattern = regexp.MustCompile(` · until (\d{4}-\d{2}-\d{2})$`)

func memoryExpired(text string, now time.Time) bool {
	m := untilPattern.FindStringSubmatch(text)
	if m == nil {
		return false
	}
	day, err := time.Parse(time.DateOnly, m[1])
	return err == nil && now.UTC().After(day.AddDate(0, 0, 1).Add(-time.Nanosecond))
}

func journal(ctx context.Context, tx *sql.Tx, agentID string, entryID int64, change, oldText, newText string, by MemoryActor, stamp string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO memory_journal (agent_id, entry_id, change, old_text, new_text, wake_id, phase, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		agentID, entryID, change, oldText, newText, by.WakeID, by.Phase, stamp)
	return err
}

// AddMemoryEntry appends one entry to the agent's index; text identical to a
// live entry is dropped. When the live entries exceed the line or byte budget,
// the oldest move to the archive in the same transaction as the insert, so an
// entry is never in neither place. Every change is journaled. Archived entries
// are kept, never loaded into a wake.
func (s *Store) AddMemoryEntry(ctx context.Context, agentID, text string, at time.Time, by MemoryActor) error {
	return s.insertMemory(ctx, agentID, text, at, by, ChangeAdded)
}

func (s *Store) insertMemory(ctx context.Context, agentID, text string, at time.Time, by MemoryActor, change string) error {
	const operation = "adding memory entry"

	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("%s for agent %q: text is empty", operation, agentID)
	}
	if utf8.RuneCountInString(text) > MaxEntryRunes || entryLines(text) > MaxEntryLines {
		return fmt.Errorf("%s for agent %q: %w", operation, agentID, errEntryTooLong)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%s for agent %q: beginning transaction: %w", operation, agentID, err)
	}
	defer func() { _ = tx.Rollback() }()

	var duplicates int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_index WHERE agent_id = ? AND archived_at IS NULL AND text = ?`, agentID, text).Scan(&duplicates); err != nil {
		return fmt.Errorf("%s for agent %q: checking duplicates: %w", operation, agentID, err)
	}
	if duplicates > 0 {
		return nil
	}

	stamp := formatStoreTimestamp(at)
	result, err := tx.ExecContext(ctx,
		`INSERT INTO memory_index (agent_id, text, created_at) SELECT id, ?, ? FROM agents WHERE id = ?`, text, stamp, agentID)
	if err != nil {
		return fmt.Errorf("%s for agent %q: inserting entry: %w", operation, agentID, err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return fmt.Errorf("%s for agent %q: agent not found: %w", operation, agentID, sql.ErrNoRows)
	}
	newID, _ := result.LastInsertId()
	if err := journal(ctx, tx, agentID, newID, change, "", text, by, stamp); err != nil {
		return fmt.Errorf("%s for agent %q: journaling entry: %w", operation, agentID, err)
	}

	rows, err := tx.QueryContext(ctx, `SELECT id, text FROM memory_index WHERE agent_id = ? AND archived_at IS NULL ORDER BY id`, agentID)
	if err != nil {
		return fmt.Errorf("%s for agent %q: reading index: %w", operation, agentID, err)
	}
	type live struct {
		id    int64
		text  string
		lines int
		bytes int
	}
	var entries []live
	lines, size := 0, 0
	for rows.Next() {
		var e live
		if err := rows.Scan(&e.id, &e.text); err != nil {
			rows.Close()
			return fmt.Errorf("%s for agent %q: scanning entry: %w", operation, agentID, err)
		}
		e.lines, e.bytes = entryLines(e.text), len(e.text)
		lines, size = lines+e.lines, size+e.bytes
		entries = append(entries, e)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("%s for agent %q: reading index: %w", operation, agentID, err)
	}
	// The new entry is last and always fits alone, so the loop never archives it.
	for _, e := range entries {
		if lines <= MaxIndexLines && size <= MaxIndexBytes {
			break
		}
		if err := archiveMemory(ctx, tx, agentID, e.id, e.text, "", ChangeOverflow, by, stamp); err != nil {
			return fmt.Errorf("%s for agent %q: archiving entry %d: %w", operation, agentID, e.id, err)
		}
		lines, size = lines-e.lines, size-e.bytes
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%s for agent %q: committing transaction: %w", operation, agentID, err)
	}
	return nil
}

func archiveMemory(ctx context.Context, tx *sql.Tx, agentID string, id int64, text, newText, change string, by MemoryActor, stamp string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE memory_index SET archived_at = ? WHERE id = ?`, stamp, id); err != nil {
		return err
	}
	return journal(ctx, tx, agentID, id, change, text, newText, by, stamp)
}

// ExpireMemory archives the live entries whose expiry date has passed.
func (s *Store) ExpireMemory(ctx context.Context, agentID string, now time.Time, by MemoryActor) error {
	const operation = "expiring memory entries"
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%s for agent %q: beginning transaction: %w", operation, agentID, err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT id, text FROM memory_index WHERE agent_id = ? AND archived_at IS NULL AND text LIKE '% · until %'`, agentID)
	if err != nil {
		return fmt.Errorf("%s for agent %q: reading index: %w", operation, agentID, err)
	}
	var expired []MemoryEntry
	for rows.Next() {
		var e MemoryEntry
		if err := rows.Scan(&e.ID, &e.Text); err != nil {
			rows.Close()
			return fmt.Errorf("%s for agent %q: scanning entry: %w", operation, agentID, err)
		}
		if memoryExpired(e.Text, now) {
			expired = append(expired, e)
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("%s for agent %q: reading index: %w", operation, agentID, err)
	}
	stamp := formatStoreTimestamp(now)
	for _, e := range expired {
		if err := archiveMemory(ctx, tx, agentID, e.ID, e.Text, "", ChangeExpired, by, stamp); err != nil {
			return fmt.Errorf("%s for agent %q: archiving entry %d: %w", operation, agentID, e.ID, err)
		}
	}
	return tx.Commit()
}

// SupersedeMemory archives the older live entry because the newer live entry
// replaces it. One pass may supersede at most a fifth of the entries it started
// with (at least one), counted per wake.
func (s *Store) SupersedeMemory(ctx context.Context, agentID string, oldID, newID int64, at time.Time, by MemoryActor) error {
	const operation = "superseding memory entry"
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%s for agent %q: beginning transaction: %w", operation, agentID, err)
	}
	defer func() { _ = tx.Rollback() }()
	var oldText, newText string
	for _, e := range []struct {
		id   int64
		into *string
	}{{oldID, &oldText}, {newID, &newText}} {
		err := tx.QueryRowContext(ctx, `SELECT text FROM memory_index WHERE id = ? AND agent_id = ? AND archived_at IS NULL`, e.id, agentID).Scan(e.into)
		if errors.Is(err, sql.ErrNoRows) || oldID >= newID {
			return fmt.Errorf("%s for agent %q: %w", operation, agentID, errSupersedeOrder)
		}
		if err != nil {
			return fmt.Errorf("%s for agent %q: reading entry: %w", operation, agentID, err)
		}
	}
	var live, done int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM memory_index WHERE agent_id = ? AND archived_at IS NULL),
		(SELECT COUNT(*) FROM memory_journal WHERE agent_id = ? AND wake_id = ? AND change = ?)`, agentID, agentID, by.WakeID, ChangeSuperseded).Scan(&live, &done); err != nil {
		return fmt.Errorf("%s for agent %q: counting entries: %w", operation, agentID, err)
	}
	if done >= max(1, (live+done)/5) {
		return fmt.Errorf("%s for agent %q: %w", operation, agentID, errSupersedeCap)
	}
	if err := archiveMemory(ctx, tx, agentID, oldID, oldText, newText, ChangeSuperseded, by, formatStoreTimestamp(at)); err != nil {
		return fmt.Errorf("%s for agent %q: archiving entry %d: %w", operation, agentID, oldID, err)
	}
	return tx.Commit()
}

// RestoreMemory puts the entry an archiving journal row removed back into the
// index as a new live entry, journaled as restored. An entry whose expiry date
// has passed is refused.
func (s *Store) RestoreMemory(ctx context.Context, agentID string, journalID int64, now time.Time) error {
	var change, text string
	err := s.db.QueryRowContext(ctx, `SELECT change, old_text FROM memory_journal WHERE id = ? AND agent_id = ?`, journalID, agentID).Scan(&change, &text)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && change != ChangeOverflow && change != ChangeExpired && change != ChangeSuperseded) {
		return fmt.Errorf("restoring memory entry for agent %q: journal row %d: %w", agentID, journalID, errRestoreEntry)
	}
	if err != nil {
		return fmt.Errorf("restoring memory entry for agent %q: reading journal row %d: %w", agentID, journalID, err)
	}
	if memoryExpired(text, now) {
		return fmt.Errorf("restoring memory entry for agent %q: journal row %d: %w", agentID, journalID, errRestoreExpired)
	}
	return s.insertMemory(ctx, agentID, text, now, MemoryActor{Phase: "operator"}, ChangeRestored)
}

// MemoryJournal returns the agent's newest journal rows, newest first.
func (s *Store) MemoryJournal(ctx context.Context, agentID string, limit int) ([]JournalEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, entry_id, change, old_text, new_text, wake_id, phase, created_at FROM memory_journal WHERE agent_id = ? ORDER BY id DESC LIMIT ?`, agentID, limit)
	if err != nil {
		return nil, fmt.Errorf("reading memory journal for agent %q: %w", agentID, err)
	}
	defer rows.Close()
	var out []JournalEntry
	for rows.Next() {
		var j JournalEntry
		if err := rows.Scan(&j.ID, &j.EntryID, &j.Change, &j.OldText, &j.NewText, &j.WakeID, &j.Phase, &j.At); err != nil {
			return nil, fmt.Errorf("reading memory journal for agent %q: scanning row: %w", agentID, err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// inMode is the SQL condition that a row stamped by alias.at was written in the
// mode bound as the given placeholder. Rows carry no mode of their own, so it is
// the mode of the agent's latest action at or before the row, else of its first
// action, else the asked mode (an agent that never acted keeps all rows). A row
// written after a mode change and before the next action takes the old mode.
func inMode(alias, at, param string) string {
	row := "(SELECT m.dry_run FROM actions m WHERE m.agent_id = " + alias + ".agent_id AND julianday(m.posted_at) %s julianday(" + alias + "." + at + ") ORDER BY julianday(m.posted_at) %s, m.id %s LIMIT 1)"
	return "COALESCE(" + fmt.Sprintf(row, "<=", "DESC", "DESC") + ", " + fmt.Sprintf(row, ">", "ASC", "ASC") + ", " + param + ") = " + param
}

// MemoryIndex returns the agent's live index entries written in the given mode
// (dry-run or live), oldest first.
func (s *Store) MemoryIndex(ctx context.Context, agentID string, dryRun bool) ([]MemoryEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT x.id, x.text FROM memory_index x WHERE x.agent_id = ?1 AND x.archived_at IS NULL AND `+inMode("x", "created_at", "?2")+` ORDER BY x.id`, agentID, dryRun)
	if err != nil {
		return nil, fmt.Errorf("reading memory index for agent %q: %w", agentID, err)
	}
	defer rows.Close()
	entries := []MemoryEntry{}
	for rows.Next() {
		var e MemoryEntry
		if err := rows.Scan(&e.ID, &e.Text); err != nil {
			return nil, fmt.Errorf("reading memory index for agent %q: scanning entry: %w", agentID, err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading memory index for agent %q: %w", agentID, err)
	}
	return entries, nil
}

func entryLines(text string) int { return strings.Count(text, "\n") + 1 }

// TopicSummary is one topic note's name and description.
type TopicSummary struct {
	Topic       string `json:"topic"`
	Description string `json:"description,omitempty"`
}

// TopicList returns the agent's topic notes written in the given mode, most recently written first, cut
// at MaxTopicList topics and MaxTopicListRunes characters of names and
// descriptions. Bodies are never included.
func (s *Store) TopicList(ctx context.Context, agentID string, dryRun bool) ([]TopicSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT x.topic, x.description FROM notes x WHERE x.agent_id = ?1 AND x.topic <> '' AND `+inMode("x", "created_at", "?2")+` ORDER BY julianday(x.created_at) DESC, x.id DESC LIMIT ?3`, agentID, dryRun, MaxTopicList)
	if err != nil {
		return nil, fmt.Errorf("listing topics for agent %q: %w", agentID, err)
	}
	defer rows.Close()
	topics := []TopicSummary{}
	runes := 0
	for rows.Next() {
		var t TopicSummary
		if err := rows.Scan(&t.Topic, &t.Description); err != nil {
			return nil, fmt.Errorf("listing topics for agent %q: scanning topic: %w", agentID, err)
		}
		runes += utf8.RuneCountInString(t.Topic) + utf8.RuneCountInString(t.Description)
		if runes > MaxTopicListRunes {
			break
		}
		topics = append(topics, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing topics for agent %q: %w", agentID, err)
	}
	return topics, nil
}
