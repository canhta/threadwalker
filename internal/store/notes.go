package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxAgentNotesSearchLimit bounds how many notes one search can return so a
// single tool call cannot pull an unbounded amount of the agent's history.
const maxAgentNotesSearchLimit = 100

// WriteTopic creates or replaces the agent's named topic note. The agent must
// already exist. The note is searchable like any other note.
func (s *Store) WriteTopic(ctx context.Context, agentID, topic, description, text string, at time.Time) (int64, error) {
	const operation = "writing topic note"

	if err := validateTopic(topic); err != nil {
		return 0, fmt.Errorf("%s for agent %q: %w", operation, agentID, err)
	}
	if strings.TrimSpace(text) == "" {
		return 0, fmt.Errorf("%s for agent %q: text is empty", operation, agentID)
	}
	if utf8.RuneCountInString(description) > maxTopicDescRunes {
		return 0, fmt.Errorf("%s for agent %q: description over %d characters", operation, agentID, maxTopicDescRunes)
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO notes (agent_id, topic, description, text, created_at)
		SELECT id, ?2, ?3, ?4, ?5 FROM agents WHERE id = ?1
		ON CONFLICT (agent_id, topic) WHERE topic <> ''
		DO UPDATE SET description = excluded.description, text = excluded.text, created_at = excluded.created_at
		RETURNING id`,
		agentID, topic, description, text, formatStoreTimestamp(at)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%s for agent %q: agent not found: %w", operation, agentID, sql.ErrNoRows)
	}
	if err != nil {
		return 0, fmt.Errorf("%s for agent %q: %w", operation, agentID, err)
	}
	return id, nil
}

func validateTopic(topic string) error {
	if topic == "" || utf8.RuneCountInString(topic) > MaxTopicRunes || strings.ContainsAny(topic, "\n\r") || strings.TrimSpace(topic) != topic {
		return fmt.Errorf("topic must be 1 to %d characters on one line without surrounding spaces", MaxTopicRunes)
	}
	return nil
}

// MemoryHit is one search result over the agent's own notes and actions. Ref
// is the handle ReadMemory accepts; Snippet marks matched terms with [ ].
type MemoryHit struct {
	Ref     string    `json:"ref"`
	Kind    string    `json:"kind"`
	Snippet string    `json:"snippet"`
	At      time.Time `json:"at"`
}

// Memory is the full text behind one MemoryHit.
type Memory struct {
	Ref  string    `json:"ref"`
	Kind string    `json:"kind"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// SearchMemory searches the agent's own notes and actions. The BM25 ranking
// over the FTS5 indexes (newest first among equal scores) and the cosine
// ranking of queryVec over the agent's stored embeddings are merged by
// reciprocal rank fusion. A nil queryVec contributes no cosine ranking, so the
// result is the BM25 order. Zero since/until leave that side open. An empty or
// punctuation-only query returns no hits. Limits above the cap are capped.
func (s *Store) SearchMemory(ctx context.Context, agentID, query string, queryVec []float32, since, until time.Time, limit int) ([]MemoryHit, error) {
	const operation = "searching agent memory"

	if limit <= 0 {
		return nil, fmt.Errorf("%s for agent %q: limit must be positive: %d", operation, agentID, limit)
	}
	if limit > maxAgentNotesSearchLimit {
		limit = maxAgentNotesSearchLimit
	}
	match := ftsMatch(query)
	if match == "" {
		return []MemoryHit{}, nil
	}
	var from, to string
	if !since.IsZero() {
		from = formatStoreTimestamp(since)
	}
	if !until.IsZero() {
		to = formatStoreTimestamp(until)
	}

	lexical, err := s.bm25Hits(ctx, agentID, match, from, to)
	if err != nil {
		return nil, fmt.Errorf("%s for agent %q: %w", operation, agentID, err)
	}
	rankings := [][]rankedHit{lexical}
	if queryVec != nil {
		semantic, err := s.vectorHits(ctx, agentID, queryVec, from, to)
		if err != nil {
			return nil, fmt.Errorf("%s for agent %q: scanning embeddings: %w", operation, agentID, err)
		}
		rankings = append(rankings, semantic)
	}
	return fuse(limit, rankings...), nil
}

func (s *Store) bm25Hits(ctx context.Context, agentID, match, from, to string) ([]rankedHit, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ref, kind, snippet, at FROM (
			SELECT 'note:' || n.id AS ref, 'note' AS kind,
				snippet(notes_fts, 1, '[', ']', '…', 24) AS snippet,
				n.created_at AS at, bm25(notes_fts) AS score, n.id AS id
			FROM notes_fts JOIN notes n ON n.id = notes_fts.rowid
			WHERE notes_fts MATCH ?1 AND n.agent_id = ?2
				AND (?3 = '' OR julianday(n.created_at) >= julianday(?3))
				AND (?4 = '' OR julianday(n.created_at) <= julianday(?4))
			UNION ALL
			SELECT 'action:' || a.id, a.kind,
				snippet(actions_fts, 1, '[', ']', '…', 24),
				a.posted_at, bm25(actions_fts), a.id
			FROM actions_fts JOIN actions a ON a.id = actions_fts.rowid
			WHERE actions_fts MATCH ?1 AND a.agent_id = ?2
				AND (?3 = '' OR julianday(a.posted_at) >= julianday(?3))
				AND (?4 = '' OR julianday(a.posted_at) <= julianday(?4))
		)
		ORDER BY score, julianday(at) DESC, id DESC
		LIMIT ?5
	`, match, agentID, from, to, maxAgentNotesSearchLimit)
	if err != nil {
		return nil, fmt.Errorf("querying FTS index: %w", err)
	}
	defer rows.Close()

	hits := make([]rankedHit, 0)
	for rows.Next() {
		var hit rankedHit
		var at string
		if err := rows.Scan(&hit.Ref, &hit.Kind, &hit.Snippet, &at); err != nil {
			return nil, fmt.Errorf("scanning hit: %w", err)
		}
		if hit.At, err = parseStoreTimestamp(at); err != nil {
			return nil, fmt.Errorf("parsing %s time: %w", hit.Ref, err)
		}
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading hits: %w", err)
	}
	return hits, nil
}

// ReadMemory returns the full text of one of the agent's own notes or actions
// by the ref SearchMemory gave. A ref that is malformed, unknown or another
// agent's returns sql.ErrNoRows.
func (s *Store) ReadMemory(ctx context.Context, agentID, ref string) (Memory, error) {
	const operation = "reading agent memory"

	source, rawID, _ := strings.Cut(ref, ":")
	var key any = rawID
	var query string
	if source != "topic" {
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || id <= 0 {
			return Memory{}, fmt.Errorf("%s for agent %q: bad ref %q: %w", operation, agentID, ref, sql.ErrNoRows)
		}
		key = id
	}
	switch {
	case source == "note":
		query = `SELECT 'note', text, created_at FROM notes WHERE id = ? AND agent_id = ?`
	case source == "topic":
		query = `SELECT 'note', text, created_at FROM notes WHERE topic = ? AND topic <> '' AND agent_id = ?`
	case source == "action":
		query = `SELECT kind, text, posted_at FROM actions WHERE id = ? AND agent_id = ?`
	default:
		return Memory{}, fmt.Errorf("%s for agent %q: bad ref %q: %w", operation, agentID, ref, sql.ErrNoRows)
	}
	memory := Memory{Ref: ref}
	var at string
	if err := s.db.QueryRowContext(ctx, query, key, agentID).Scan(&memory.Kind, &memory.Text, &at); err != nil {
		return Memory{}, fmt.Errorf("%s for agent %q: reading %s: %w", operation, agentID, ref, err)
	}
	var err error
	if memory.At, err = parseStoreTimestamp(at); err != nil {
		return Memory{}, fmt.Errorf("%s for agent %q: parsing %s time: %w", operation, agentID, ref, err)
	}
	return memory, nil
}

func parseStoreTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed.UTC(), err
}

func formatStoreTimestamp(at time.Time) string {
	return at.UTC().Format(time.RFC3339Nano)
}

// ftsMatch turns free text into an FTS5 MATCH expression: every letter or digit
// run becomes one quoted term, joined with OR. Model text never reaches MATCH
// raw, so quotes, hyphens, colons and AND/OR/NOT cannot be syntax.
func ftsMatch(query string) string {
	terms := strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r)
	})
	seen := make(map[string]bool, len(terms))
	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.ToLower(term)
		if seen[term] {
			continue
		}
		seen[term] = true
		quoted = append(quoted, `"`+term+`"`)
	}
	return strings.Join(quoted, " OR ")
}
