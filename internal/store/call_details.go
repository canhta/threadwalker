package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// MaxArgField caps one string of the arguments stored with a tool call.
const MaxArgField = 2 << 10

// CallDetail is what one tool call knew when it ended, for the operator who
// diagnoses a wake. Text in it may come from outside, so it lives as long as
// transcripts do.
type CallDetail struct {
	// Detail is the refusal's detail, such as the measured length.
	Detail string `json:"detail,omitempty"`
	// Ref is how a trend_ref or reply target resolved.
	Ref *RefTrace `json:"ref,omitempty"`
	// Tries is every draft the writer and critic produced, oldest first.
	Tries []TryTrace `json:"tries,omitempty"`
	// Counts is the length of each list a read returned.
	Counts map[string]int `json:"counts,omitempty"`
	// Stage and Evidence name the browser step that failed and the saved snapshot.
	Stage    string `json:"stage,omitempty"`
	Evidence string `json:"evidence,omitempty"`
	ActionID int64  `json:"action_id,omitempty"`
}

// RefTrace is the ref the agent gave, what the guard resolved it to, and the
// nearest refs noted this wake when it resolved to none.
type RefTrace struct {
	Field    string    `json:"field"`
	Given    string    `json:"given"`
	Resolved string    `json:"resolved,omitempty"`
	Nearest  []NearRef `json:"nearest,omitempty"`
	// Noted is how many refs this wake issued.
	Noted int `json:"noted,omitempty"`
}

// NearRef is a ref noted this wake, with its edit distance from the ref given.
type NearRef struct {
	Ref   string `json:"ref"`
	Item  string `json:"item"`
	Edits int    `json:"edits"`
}

// TryTrace is one draft: who wrote it, its first line and part lengths in
// runes, and the critic's verdict or the content rule that stopped it first.
type TryTrace struct {
	Source   string          `json:"source"`
	First    string          `json:"first"`
	Runes    []int           `json:"runes"`
	Pass     bool            `json:"pass"`
	Scores   json.RawMessage `json:"scores,omitempty"`
	Problems []string        `json:"problems,omitempty"`
}

// RecordCallArgs stores the redacted, capped arguments of an audited call.
func (s *Store) RecordCallArgs(ctx context.Context, agentID string, wakeID, callID int64, argsJSON string, at time.Time) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO tool_call_details(tool_call_id,agent_id,wake_id,args_json,at) VALUES(?,?,?,?,?)`, callID, agentID, wakeID, argsJSON, formatTime(at)); err != nil {
		return fmt.Errorf("recording tool arguments: %w", err)
	}
	return nil
}

// RecordCallDetail stores what the call knew when it ended.
func (s *Store) RecordCallDetail(ctx context.Context, callID int64, detail CallDetail) error {
	encoded, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encoding tool detail: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tool_call_details SET detail_json=? WHERE tool_call_id=?`, string(encoded), callID); err != nil {
		return fmt.Errorf("recording tool detail: %w", err)
	}
	return nil
}

// CallRecord is one audited tool call of a wake with its stored arguments and detail.
type CallRecord struct {
	ID        int64
	Tool      string
	BlockedBy string
	Result    json.RawMessage
	Args      json.RawMessage
	Detail    CallDetail
	At        time.Time
}

// WakeCalls lists a wake's audited tool calls in order. A call older than the
// details' retention has no Args and an empty Detail.
func (s *Store) WakeCalls(ctx context.Context, agentID string, wakeID int64) ([]CallRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.id,c.tool,c.blocked_by,c.result_json,COALESCE(d.args_json,''),COALESCE(d.detail_json,''),c.at
		FROM tool_calls c LEFT JOIN tool_call_details d ON d.tool_call_id=c.id WHERE c.agent_id=? AND c.wake_id=? ORDER BY c.id`, agentID, wakeID)
	if err != nil {
		return nil, fmt.Errorf("reading tool calls: %w", err)
	}
	defer rows.Close()
	var out []CallRecord
	for rows.Next() {
		var c CallRecord
		var result, args, detail, at string
		if err = rows.Scan(&c.ID, &c.Tool, &c.BlockedBy, &result, &args, &detail, &at); err != nil {
			return nil, fmt.Errorf("reading tool call: %w", err)
		}
		c.Result, c.Args = validJSON(result), validJSON(args)
		if detail != "" {
			if err = json.Unmarshal([]byte(detail), &c.Detail); err != nil {
				return nil, fmt.Errorf("reading tool detail: %w", err)
			}
		}
		if c.At, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("reading tool call time: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func validJSON(s string) json.RawMessage {
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	return nil
}

// ActionOutcome is the stored state of one action for a diagnosis.
type ActionOutcome struct {
	Kind, ThreadURL string
	DryRun, Failed  bool
}

// ActionOutcomes returns the actions with the given ids, keyed by id.
func (s *Store) ActionOutcomes(ctx context.Context, agentID string, ids []int64) (map[int64]ActionOutcome, error) {
	out := map[int64]ActionOutcome{}
	for _, id := range ids {
		var a ActionOutcome
		err := s.db.QueryRowContext(ctx, `SELECT kind,thread_url,dry_run,failed FROM actions WHERE agent_id=? AND id=?`, agentID, id).Scan(&a.Kind, &a.ThreadURL, &a.DryRun, &a.Failed)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading action outcome: %w", err)
		}
		out[id] = a
	}
	return out, nil
}
