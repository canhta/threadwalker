package llm

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/canhta/threadz/internal/logging"
)

// safeError retains errors.Is/As compatibility without returning provider or
// storage diagnostics which may contain URLs, payloads or local data.
type safeError struct {
	operation, reason string
	cause             error
}

func (e *safeError) Error() string { return e.operation + ": " + e.reason }
func (e *safeError) Unwrap() error { return e.cause }
func failure(operation, reason string, cause error) error {
	return &safeError{operation, reason, cause}
}
func errorReason(err error) string {
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, ErrBudget) {
		return "budget_limit"
	}
	if errors.Is(err, ErrStepLimit) {
		return "step_limit"
	}
	var safe *safeError
	if errors.As(err, &safe) {
		return safe.reason
	}
	return "invalid_response"
}
func (c *Client) event(ctx context.Context, level slog.Level, event string, agentID string, wakeID int64, request, attempt int, started time.Time, outcome, reason string, usage Usage, status, steps int) {
	elapsed := int64(0)
	if !started.IsZero() {
		elapsed = c.now().Sub(started).Milliseconds()
	}
	logging.Emit(ctx, c.logger, level, logging.Record{Event: event, AgentID: agentID, Phase: c.phase, WakeID: wakeID, Request: request, ProviderID: c.providerID, ProfileID: c.profileID, Model: c.config.Model, Protocol: c.config.Protocol, Operation: "chat_completion", Attempt: attempt, HTTPStatus: status, DurationMS: elapsed, Outcome: outcome, Reason: reason, TokensIn: usage.TokensIn, TokensOut: usage.TokensOut, CostUSD: usage.CostUSD, Steps: steps})
}
