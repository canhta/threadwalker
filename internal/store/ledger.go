package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
)

// LLMUsage is the token and cost usage reported by a single LLM response.
type LLMUsage struct {
	TokensIn  int64
	TokensOut int64
	CostUSD   float64
	// TokensCached is the cached share of TokensIn.
	TokensCached int64
	// Phase overrides the wake kind as the ledger phase; only "critic" is honoured.
	Phase string
}

// RecordLLMUsage atomically adds usage to the agent's per-phase ledger for day and to the
// wake identified by wakeID. The caller supplies day as a UTC YYYY-MM-DD string.
// A missing wake, or one owned by another agent, leaves both tables unchanged.
func (s *Store) RecordLLMUsage(ctx context.Context, agentID string, wakeID int64, day string, usage LLMUsage) error {
	if err := usage.validate(); err != nil {
		return fmt.Errorf("recording LLM usage for agent %q on %s: %w", agentID, day, err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("recording LLM usage for agent %q on %s: beginning transaction: %w", agentID, day, err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var wakeAgentID, wakeKind string
	err = tx.QueryRowContext(ctx, `SELECT agent_id,kind FROM wakes WHERE id = ?`, wakeID).Scan(&wakeAgentID, &wakeKind)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("recording LLM usage for agent %q on %s: wake %d not found: %w", agentID, day, wakeID, sql.ErrNoRows)
	}
	if err != nil {
		return fmt.Errorf("recording LLM usage for agent %q on %s: looking up wake %d: %w", agentID, day, wakeID, err)
	}
	if wakeAgentID != agentID {
		return fmt.Errorf("recording LLM usage for agent %q on %s: wake %d belongs to agent %q", agentID, day, wakeID, wakeAgentID)
	}
	phase := "agent"
	if usage.Phase == "critic" {
		wakeKind = "critic"
	}
	switch wakeKind {
	case "critic":
		phase = "critic"
	case "dream":
		phase = "dream"
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO ledger_phases (agent_id, day, phase, tokens_in, tokens_out, cost_usd, tokens_cached)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id, day, phase) DO UPDATE SET
			tokens_in = ledger_phases.tokens_in + excluded.tokens_in,
			tokens_out = ledger_phases.tokens_out + excluded.tokens_out,
			cost_usd = ledger_phases.cost_usd + excluded.cost_usd,
			tokens_cached = ledger_phases.tokens_cached + excluded.tokens_cached
	`, agentID, day, phase, usage.TokensIn, usage.TokensOut, usage.CostUSD, usage.TokensCached)
	if err != nil {
		return fmt.Errorf("recording LLM usage for agent %q on %s: updating ledger: %w", agentID, day, err)
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE wakes
		SET tokens_in = tokens_in + ?,
			tokens_out = tokens_out + ?,
			cost_usd = cost_usd + ?
		WHERE id = ? AND agent_id = ?
	`, usage.TokensIn, usage.TokensOut, usage.CostUSD, wakeID, agentID)
	if err != nil {
		return fmt.Errorf("recording LLM usage for agent %q on %s: updating wake %d: %w", agentID, day, wakeID, err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("recording LLM usage for agent %q on %s: checking wake %d update: %w", agentID, day, wakeID, err)
	}
	if updated != 1 {
		return fmt.Errorf("recording LLM usage for agent %q on %s: wake %d not found", agentID, day, wakeID)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("recording LLM usage for agent %q on %s: committing transaction: %w", agentID, day, err)
	}
	return nil
}

// LLMUsageForDay returns the ledger totals for an agent on day. A day with no
// recorded usage returns zero usage and a nil error.
func (s *Store) LLMUsageForDay(ctx context.Context, agentID string, day string) (LLMUsage, error) {
	var usage LLMUsage
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(tokens_in),0), COALESCE(SUM(tokens_out),0), COALESCE(SUM(cost_usd),0), COALESCE(SUM(tokens_cached),0)
		FROM ledger_phases
		WHERE agent_id = ? AND day = ?
	`, agentID, day).Scan(&usage.TokensIn, &usage.TokensOut, &usage.CostUSD, &usage.TokensCached)
	if err != nil {
		return LLMUsage{}, fmt.Errorf("reading LLM usage for agent %q on %s: %w", agentID, day, err)
	}
	return usage, nil
}

func (u LLMUsage) validate() error {
	if u.TokensIn < 0 {
		return fmt.Errorf("tokens in must not be negative: %d", u.TokensIn)
	}
	if u.TokensOut < 0 {
		return fmt.Errorf("tokens out must not be negative: %d", u.TokensOut)
	}
	if u.TokensCached < 0 || u.TokensCached > u.TokensIn {
		return fmt.Errorf("tokens cached must be within tokens in: %d of %d", u.TokensCached, u.TokensIn)
	}
	if u.CostUSD < 0 {
		return fmt.Errorf("cost must not be negative: %v", u.CostUSD)
	}
	if math.IsNaN(u.CostUSD) {
		return errors.New("cost must not be NaN")
	}
	if math.IsInf(u.CostUSD, 0) {
		return fmt.Errorf("cost must be finite: %v", u.CostUSD)
	}
	return nil
}
