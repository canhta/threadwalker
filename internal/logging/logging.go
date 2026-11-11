// Package logging owns the operational event contract. Audit rows remain the
// action record. Callers supply only identifiers, counts and fixed reason codes;
// never pass errors, payloads, headers, URLs, notes or user content to this API.
package logging

import (
	"context"
	"errors"
	"io"
	"log/slog"
)

// Stable events emitted by the components implemented today. LLM tool events
// describe only the client dispatcher boundary, not a guard or audit outcome.
const (
	WakeStarted        = "wake.started"
	WakeFinished       = "wake.finished"
	WakeStopped        = "wake.stopped"
	SchedulerScheduled = "scheduler.scheduled"
	SchedulerChecked   = "scheduler.checked"
	ContextBuilt       = "context.built"
	ToolStarted        = "tool.started"
	ToolFinished       = "tool.finished"
	GuardChecked       = "guard.checked"
	ToolBlocked        = "tool.blocked"
	AuditRecorded      = "audit.recorded"
	NoteWritten        = "note.written"
	NoteSearched       = "note.searched"
	TrendsFetched      = "trends.fetched"
	CheckStarted       = "check.started"
	CheckConfigured    = "check.configured"
	CheckFinished      = "check.finished"
	LLMRunStarted      = "llm.run.started"
	LLMRunFinished     = "llm.run.finished"
	LLMRunStopped      = "llm.run.stopped"
	LLMRequestStarted  = "llm.request.started"
	LLMRequestFinished = "llm.request.finished"
	LLMRequestRetry    = "llm.request.retry"
	LLMUsageRecorded   = "llm.usage.recorded"
	LLMToolFinished    = "llm.tool.finished"
)

// New builds a standard-library handler. Empty options select info and JSON.
func New(w io.Writer, level, format string) (*slog.Logger, error) {
	var threshold slog.Level
	switch level {
	case "", "info":
		threshold = slog.LevelInfo
	case "debug":
		threshold = slog.LevelDebug
	case "warn":
		threshold = slog.LevelWarn
	case "error":
		threshold = slog.LevelError
	default:
		return nil, errors.New("invalid log level: choose debug, info, warn or error")
	}
	options := &slog.HandlerOptions{Level: threshold}
	switch format {
	case "", "json":
		return slog.New(slog.NewJSONHandler(w, options)), nil
	case "text":
		return slog.New(slog.NewTextHandler(w, options)), nil
	default:
		return nil, errors.New("invalid log format: choose json or text")
	}
}

// Record is the allowlist of operational metadata. IDs come from operator
// configuration or local counters, never provider/Threads payload fields.
// Event, Operation, Outcome, Reason and Tool must be static code/schema values.
// Optional zero numeric values are included to make accounting explicit.
type Record struct {
	Event, AgentID, ProviderID, ProfileID, Model, Protocol string
	WakeID                                                 int64
	Request                                                int
	ToolCallID                                             string
	Operation, Outcome, Reason, Tool                       string
	Attempt, HTTPStatus, Steps                             int
	DurationMS                                             int64
	TokensIn, TokensOut                                    int64
	CostUSD                                                float64
}

// Emit accepts a typed allowlist rather than arbitrary slog attributes. An
// injected nil logger disables operational output for library consumers.
func Emit(ctx context.Context, logger *slog.Logger, level slog.Level, r Record) {
	if logger == nil {
		return
	}
	attrs := []slog.Attr{slog.String("event", r.Event), slog.String("operation", r.Operation), slog.String("outcome", r.Outcome), slog.String("reason", r.Reason)}
	for _, field := range []struct{ k, v string }{{"agent_id", r.AgentID}, {"provider_id", r.ProviderID}, {"profile_id", r.ProfileID}, {"model", r.Model}, {"protocol", r.Protocol}, {"tool_call_id", r.ToolCallID}, {"tool", r.Tool}} {
		if field.v != "" {
			attrs = append(attrs, slog.String(field.k, field.v))
		}
	}
	attrs = append(attrs, slog.Int64("wake_id", r.WakeID), slog.Int("request", r.Request), slog.Int("attempt", r.Attempt), slog.Int("http_status", r.HTTPStatus), slog.Int64("duration_ms", r.DurationMS), slog.Int("steps", r.Steps), slog.Int64("tokens_in", r.TokensIn), slog.Int64("tokens_out", r.TokensOut), slog.Float64("cost_usd", r.CostUSD))
	logger.LogAttrs(ctx, level, "threadwalker event", attrs...)
}
