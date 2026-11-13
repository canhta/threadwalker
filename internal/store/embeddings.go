package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// EmbeddingSources are the tables whose text is embedded: the agent's own
// notes and its own actions. Threads content and trend stories never are.
const (
	EmbeddingNote   = "note"
	EmbeddingAction = "action"
)

// PendingEmbedding is a note or action whose current text has no stored vector.
type PendingEmbedding struct {
	Source string
	ID     int64
	Text   string
}

// EmbeddingRow is one vector with the hash of the text it was computed from.
// The text itself stays in its source table.
type EmbeddingRow struct {
	Source   string
	ID       int64
	Text     string
	TextHash string
	Vector   []float32
}

// TextHash keys an embedding by the text it encodes.
func TextHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// PendingEmbeddings returns up to limit of the agent's notes and actions that
// have no vector, by source then id. Rows with blank text are never embedded.
func (s *Store) PendingEmbeddings(ctx context.Context, agentID string, limit int) ([]PendingEmbedding, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT source, id, text FROM (
			SELECT 'note' AS source, n.id AS id, n.text AS text FROM notes n
			WHERE n.agent_id = ?1 AND trim(n.text) <> ''
				AND NOT EXISTS (SELECT 1 FROM embeddings e WHERE e.source = 'note' AND e.source_id = n.id)
			UNION ALL
			SELECT 'action', a.id, a.text FROM actions a
			WHERE a.agent_id = ?1 AND trim(a.text) <> ''
				AND NOT EXISTS (SELECT 1 FROM embeddings e WHERE e.source = 'action' AND e.source_id = a.id)
		) ORDER BY source, id LIMIT ?2`, agentID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing pending embeddings for agent %q: %w", agentID, err)
	}
	defer rows.Close()
	var pending []PendingEmbedding
	for rows.Next() {
		var p PendingEmbedding
		if err := rows.Scan(&p.Source, &p.ID, &p.Text); err != nil {
			return nil, fmt.Errorf("listing pending embeddings for agent %q: %w", agentID, err)
		}
		pending = append(pending, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing pending embeddings for agent %q: %w", agentID, err)
	}
	return pending, nil
}

// VectorsByHash returns the stored vector for each text hash that has one, so
// identical text is embedded once.
func (s *Store) VectorsByHash(ctx context.Context, hashes []string) (map[string][]float32, error) {
	found := make(map[string][]float32, len(hashes))
	for _, hash := range hashes {
		var blob []byte
		err := s.db.QueryRowContext(ctx, `SELECT vector FROM embeddings WHERE text_hash = ? LIMIT 1`, hash).Scan(&blob)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading embedding by hash: %w", err)
		}
		found[hash] = decodeVector(blob)
	}
	return found, nil
}

// SaveEmbeddings stores each vector whose source row still holds the text it
// was computed from; a row rewritten meanwhile stays pending.
func (s *Store) SaveEmbeddings(ctx context.Context, rows []EmbeddingRow) error {
	return s.write(ctx, func(conn *sql.Conn) error {
		for _, r := range rows {
			if _, err := conn.ExecContext(ctx, `
				INSERT OR REPLACE INTO embeddings (source, source_id, text_hash, vector)
				SELECT ?1, ?2, ?3, ?4 WHERE EXISTS (
					SELECT 1 FROM notes WHERE ?1 = 'note' AND id = ?2 AND text = ?5
					UNION ALL
					SELECT 1 FROM actions WHERE ?1 = 'action' AND id = ?2 AND text = ?5)`,
				r.Source, r.ID, r.TextHash, encodeVector(r.Vector), r.Text); err != nil {
				return fmt.Errorf("saving embedding for %s %d: %w", r.Source, r.ID, err)
			}
		}
		return nil
	})
}

// RecordUnattributedLLMUsage adds usage that belongs to no wake, such as the
// startup embedding backfill, to the agent's ledger under the agent phase.
func (s *Store) RecordUnattributedLLMUsage(ctx context.Context, agentID, day string, usage LLMUsage) error {
	if err := usage.validate(); err != nil {
		return fmt.Errorf("recording LLM usage for agent %q on %s: %w", agentID, day, err)
	}
	return s.write(ctx, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO ledger_phases (agent_id, day, phase, tokens_in, tokens_out, cost_usd) VALUES (?1, ?2, 'agent', ?3, ?4, ?5)
			ON CONFLICT(agent_id, day, phase) DO UPDATE SET tokens_in = tokens_in + excluded.tokens_in,
				tokens_out = tokens_out + excluded.tokens_out, cost_usd = cost_usd + excluded.cost_usd`,
			agentID, day, usage.TokensIn, usage.TokensOut, usage.CostUSD); err != nil {
			return fmt.Errorf("recording LLM usage for agent %q on %s: updating ledger: %w", agentID, day, err)
		}
		return nil
	})
}

// rrfK is the reciprocal rank fusion constant of Cormack et al. (SIGIR 2009).
const rrfK = 60

// snippetRunes bounds the text shown for a hit that matched by meaning only.
const snippetRunes = 160

type rankedHit struct {
	MemoryHit
}

// vectorHits ranks the agent's embedded rows by cosine similarity to query.
// Rows whose vector has another length are skipped.
func (s *Store) vectorHits(ctx context.Context, agentID string, query []float32, from, to string) ([]rankedHit, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT 'note:' || n.id, 'note', n.text, n.created_at, e.vector
		FROM embeddings e JOIN notes n ON e.source = 'note' AND n.id = e.source_id
		WHERE n.agent_id = ?1
			AND (?2 = '' OR julianday(n.created_at) >= julianday(?2))
			AND (?3 = '' OR julianday(n.created_at) <= julianday(?3))
		UNION ALL
		SELECT 'action:' || a.id, a.kind, a.text, a.posted_at, e.vector
		FROM embeddings e JOIN actions a ON e.source = 'action' AND a.id = e.source_id
		WHERE a.agent_id = ?1
			AND (?2 = '' OR julianday(a.posted_at) >= julianday(?2))
			AND (?3 = '' OR julianday(a.posted_at) <= julianday(?3))`, agentID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type scored struct {
		hit   rankedHit
		score float64
	}
	var all []scored
	for rows.Next() {
		var h rankedHit
		var text, at string
		var blob []byte
		if err := rows.Scan(&h.Ref, &h.Kind, &text, &at, &blob); err != nil {
			return nil, err
		}
		vec := decodeVector(blob)
		if len(vec) != len(query) {
			continue
		}
		if h.At, err = parseStoreTimestamp(at); err != nil {
			return nil, fmt.Errorf("parsing %s time: %w", h.Ref, err)
		}
		h.Snippet = textPrefix(text)
		all = append(all, scored{h, cosine(query, vec)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].score > all[j].score })
	if len(all) > maxAgentNotesSearchLimit {
		all = all[:maxAgentNotesSearchLimit]
	}
	out := make([]rankedHit, len(all))
	for i, a := range all {
		out[i] = a.hit
	}
	return out, nil
}

// fuse merges rankings by reciprocal rank fusion. A hit in several rankings
// keeps the first ranking's snippet; ties go to the newest hit.
func fuse(limit int, rankings ...[]rankedHit) []MemoryHit {
	type entry struct {
		hit   MemoryHit
		score float64
	}
	byRef := map[string]*entry{}
	var order []*entry
	for _, ranking := range rankings {
		for rank, h := range ranking {
			e := byRef[h.Ref]
			if e == nil {
				e = &entry{hit: h.MemoryHit}
				byRef[h.Ref] = e
				order = append(order, e)
			}
			e.score += 1 / float64(rrfK+rank+1)
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].score != order[j].score {
			return order[i].score > order[j].score
		}
		return order[i].hit.At.After(order[j].hit.At)
	})
	if len(order) > limit {
		order = order[:limit]
	}
	hits := make([]MemoryHit, len(order))
	for i, e := range order {
		hits[i] = e.hit
	}
	return hits
}

func textPrefix(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= snippetRunes {
		return text
	}
	return string([]rune(text)[:snippetRunes]) + "…"
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / math.Sqrt(na*nb)
}

func encodeVector(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

func decodeVector(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}
