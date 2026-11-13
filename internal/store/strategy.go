package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxStrategyRunes bounds the strategy shown in every wake.
	MaxStrategyRunes = 2000
	maxCitations     = 8
	// ExploreWindow is the number of newest own actions the explore budget is measured over.
	ExploreWindow = 20
)

// ErrStrategyInput reports a strategy revision that is blank, oversized or
// does not cite evidence the agent owns.
var ErrStrategyInput = errors.New("strategy revision rejected")

// StrategyRevision is one version of the agent's own strategy. Lift compares
// the mean combined lift (reach and resonance) of measured actions before and after it.
type StrategyRevision struct {
	Version   int64     `json:"version"`
	Text      string    `json:"text"`
	Citations []string  `json:"citations"`
	CreatedAt time.Time `json:"created_at"`
	Lift      Lift      `json:"lift"`
}

// Lift is the before/after comparison for one revision. Before is the span
// since the previous revision (or all earlier actions); After runs until the
// next revision or now. Scores are (likes+replies+reposts)/views capped at 1,
// from live actions with a completed 24 hour measurement.
type Lift struct {
	BeforeN    int64   `json:"before_n"`
	BeforeMean float64 `json:"before_mean"`
	AfterN     int64   `json:"after_n"`
	AfterMean  float64 `json:"after_mean"`
	Change     float64 `json:"change"`
}

// WriteStrategy stores the next version. Every revision must cite at least one
// of the agent's own actions (action:<id>) or notes (note:<id>).
func (s *Store) WriteStrategy(ctx context.Context, agentID, text string, citations []string, at time.Time) (int64, error) {
	const operation = "writing strategy"
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > MaxStrategyRunes {
		return 0, fmt.Errorf("%s for agent %q: %w: text must be 1..%d characters", operation, agentID, ErrStrategyInput, MaxStrategyRunes)
	}
	if len(citations) == 0 || len(citations) > maxCitations {
		return 0, fmt.Errorf("%s for agent %q: %w: cite 1..%d of your own action or note ids", operation, agentID, ErrStrategyInput, maxCitations)
	}
	for _, c := range citations {
		kind, raw, _ := strings.Cut(c, ":")
		id, err := strconv.ParseInt(raw, 10, 64)
		table := map[string]string{"action": "actions", "note": "notes"}[kind]
		if err != nil || id < 1 || table == "" {
			return 0, fmt.Errorf("%s for agent %q: %w: citation %q is not action:<id> or note:<id>", operation, agentID, ErrStrategyInput, c)
		}
		var owned bool
		if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM `+table+` WHERE id = ? AND agent_id = ?)`, id, agentID).Scan(&owned); err != nil {
			return 0, fmt.Errorf("%s for agent %q: checking citation: %w", operation, agentID, err)
		}
		if !owned {
			return 0, fmt.Errorf("%s for agent %q: %w: citation %q is not yours", operation, agentID, ErrStrategyInput, c)
		}
	}
	encoded, _ := json.Marshal(citations)
	var version int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO strategy_revisions (agent_id, version, text, citations_json, created_at)
		SELECT id, COALESCE((SELECT MAX(version) FROM strategy_revisions WHERE agent_id = agents.id), 0) + 1, ?, ?, ? FROM agents WHERE id = ?
		RETURNING version`, text, string(encoded), formatStoreTimestamp(at), agentID).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("%s for agent %q: inserting revision: %w", operation, agentID, err)
	}
	return version, nil
}

// StrategyHistory returns every revision, oldest first, each with its lift.
func (s *Store) StrategyHistory(ctx context.Context, agentID string) ([]StrategyRevision, error) {
	const operation = "reading strategy history"
	rows, err := s.db.QueryContext(ctx, `SELECT version, text, citations_json, created_at FROM strategy_revisions WHERE agent_id = ? ORDER BY version`, agentID)
	if err != nil {
		return nil, fmt.Errorf("%s for agent %q: %w", operation, agentID, err)
	}
	var out []StrategyRevision
	for rows.Next() {
		var r StrategyRevision
		var citations, at string
		if err = rows.Scan(&r.Version, &r.Text, &citations, &at); err != nil {
			rows.Close()
			return nil, fmt.Errorf("%s for agent %q: scanning: %w", operation, agentID, err)
		}
		if err = json.Unmarshal([]byte(citations), &r.Citations); err != nil {
			rows.Close()
			return nil, fmt.Errorf("%s for agent %q: citations: %w", operation, agentID, err)
		}
		if r.CreatedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			rows.Close()
			return nil, fmt.Errorf("%s for agent %q: timestamp: %w", operation, agentID, err)
		}
		out = append(out, r)
	}
	if err = rows.Close(); err != nil {
		return nil, fmt.Errorf("%s for agent %q: %w", operation, agentID, err)
	}
	for i := range out {
		start := time.Time{}
		if i > 0 {
			start = out[i-1].CreatedAt
		}
		end := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
		if i+1 < len(out) {
			end = out[i+1].CreatedAt
		}
		before, after := [2]time.Time{start, out[i].CreatedAt}, [2]time.Time{out[i].CreatedAt, end}
		l := &out[i].Lift
		if l.BeforeN, l.BeforeMean, err = s.meanScore(ctx, agentID, before); err != nil {
			return nil, err
		}
		if l.AfterN, l.AfterMean, err = s.meanScore(ctx, agentID, after); err != nil {
			return nil, err
		}
		if l.BeforeN > 0 && l.AfterN > 0 {
			l.Change = l.AfterMean - l.BeforeMean
		}
	}
	return out, nil
}

func (s *Store) meanScore(ctx context.Context, agentID string, span [2]time.Time) (n int64, mean float64, err error) {
	var avg *float64
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), AVG((o.reach_lift + o.resonance_lift) / 2)
		FROM actions a
		JOIN action_outcomes o ON o.agent_id = a.agent_id AND o.action_id = a.id
		WHERE a.agent_id = ? AND a.dry_run = 0 AND trim(a.thread_url) <> ''
		AND julianday(a.posted_at) >= julianday(?) AND julianday(a.posted_at) < julianday(?)`,
		agentID, span[0].UTC().Format(time.RFC3339Nano), span[1].UTC().Format(time.RFC3339Nano)).Scan(&n, &avg)
	if err != nil {
		return 0, 0, fmt.Errorf("measuring strategy lift for agent %q: %w", agentID, err)
	}
	if avg != nil {
		mean = *avg
	}
	return n, mean, nil
}

// RevertStrategy restores an earlier revision's text as a new revision, so the
// history only grows. to == 0 means the revision before the newest. The copy
// keeps the source's citations, which the agent owns.
func (s *Store) RevertStrategy(ctx context.Context, agentID string, to int64, at time.Time) (int64, error) {
	const operation = "reverting strategy"
	var latest int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM strategy_revisions WHERE agent_id = ?`, agentID).Scan(&latest); err != nil {
		return 0, fmt.Errorf("%s for agent %q: %w", operation, agentID, err)
	}
	if to == 0 {
		to = latest - 1
	}
	var version int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO strategy_revisions (agent_id, version, text, citations_json, created_at)
		SELECT agent_id, ?, text, citations_json, ? FROM strategy_revisions WHERE agent_id = ? AND version = ? AND version < ?
		RETURNING version`, latest+1, formatStoreTimestamp(at), agentID, to, latest).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%s for agent %q: %w: no revision %d", operation, agentID, ErrStrategyInput, to)
	}
	if err != nil {
		return 0, fmt.Errorf("%s for agent %q: %w", operation, agentID, err)
	}
	return version, nil
}
