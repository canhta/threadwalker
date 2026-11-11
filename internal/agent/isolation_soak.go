package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/llm"
	"github.com/canhta/threadwalker/internal/store"
	"github.com/canhta/threadwalker/internal/threads"
)

type IsolationAgentEvidence struct {
	AgentID          string  `json:"agent_id"`
	CompletedWakes   int     `json:"completed_wakes"`
	BudgetStops      int     `json:"budget_stops"`
	ActionLimitStops int     `json:"action_limit_stops"`
	PausedSkips      int     `json:"paused_skips"`
	KilledSkips      int     `json:"killed_skips"`
	DryRunActions    int     `json:"dry_run_actions"`
	TokensIn         int64   `json:"tokens_in"`
	TokensOut        int64   `json:"tokens_out"`
	CostUSD          float64 `json:"estimated_cost_usd"`
}
type IsolationMemory struct {
	LogicalHour    int    `json:"logical_hour"`
	AllocBytes     uint64 `json:"go_alloc_bytes"`
	HeapInuseBytes uint64 `json:"go_heap_inuse_bytes"`
	SysBytes       uint64 `json:"go_sys_bytes"`
	Goroutines     int    `json:"goroutines"`
}
type IsolationHour struct {
	LogicalHour  int     `json:"logical_hour"`
	AgentID      string  `json:"agent_id"`
	Status       string  `json:"status"`
	DailyCostUSD float64 `json:"daily_estimated_cost_usd"`
}
type ScopedWakeEvidence struct {
	AgentID string `json:"agent_id"`
	WakeEvidence
}
type IsolationEvidence struct {
	Version           string                   `json:"version"`
	SourceRevision    string                   `json:"source_revision,omitempty"`
	Synthetic         bool                     `json:"synthetic"`
	TimingKind        string                   `json:"timing_kind"`
	LogicalHours      int                      `json:"logical_hours"`
	Restarts          int                      `json:"restarts"`
	Serial            bool                     `json:"serial"`
	StateIsolated     bool                     `json:"state_isolated"`
	BudgetIsolated    bool                     `json:"budget_isolated"`
	ContextIsolated   bool                     `json:"context_isolated"`
	MaxActiveBackends int                      `json:"max_active_synthetic_backends"`
	MaintenanceWakes  int                      `json:"maintenance_wakes"`
	MemoryKind        string                   `json:"memory_kind"`
	Memory            []IsolationMemory        `json:"memory"`
	Agents            []IsolationAgentEvidence `json:"agents"`
	Hours             []IsolationHour          `json:"hours"`
	Wakes             []ScopedWakeEvidence     `json:"wakes"`
}

// RunIsolationSoak uses actual runtime, scheduler, SQLite and guards with two
// synthetic identities. Time advances locally; memory samples describe this Go
// process with fake backends, not browser RSS or 48 elapsed hours of live use.
func RunIsolationSoak(ctx context.Context, dbPath string, hours int, logger *slog.Logger) (e IsolationEvidence, runErr error) {
	e = IsolationEvidence{Version: "v1", Synthetic: true, TimingKind: "logical_time", LogicalHours: hours, Serial: true, StateIsolated: true, BudgetIsolated: true, ContextIsolated: true, MemoryKind: "local_go_process_samples_with_synthetic_backends_not_elapsed_qualification"}
	if hours < 48 || hours > 720 || dbPath == "" || dbPath == ":memory:" {
		return e, errors.New("isolation soak: empty file database and 48..720 logical hours required")
	}
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return e, err
	}
	defer func() {
		if st != nil {
			runErr = errors.Join(runErr, st.Close())
		}
	}()
	var existing int
	if err = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM agents`).Scan(&existing); err != nil {
		return e, err
	}
	if existing != 0 {
		return e, errors.New("isolation soak: isolated empty database required")
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start
	clock := func() time.Time { return now }
	templates := make([]config.Template, 2)
	markers := []string{"PRIVATE_SYNTHETIC_STATE_A", "PRIVATE_SYNTHETIC_STATE_B"}
	for i, id := range []string{"synthetic-isolation-a", "synthetic-isolation-b"} {
		t := OfflineTemplate()
		t.ID = id
		t.Goal = markers[i]
		t.Persona.Name = id
		t.SeedPlaybook = []string{markers[i]}
		t.Threads.Username = fmt.Sprintf("synthetic_isolation_%c", 'a'+i)
		t.Threads.ProfileDir = filepath.Join(filepath.Dir(dbPath), fmt.Sprintf("profile-%d", i))
		t.Limits.PostsPerDay = 6
		if i == 1 {
			t.Limits.MaxUSDPerDay = 0.00008
		}
		templates[i] = t
		e.Agents = append(e.Agents, IsolationAgentEvidence{AgentID: id})
		if err = st.EnsureAgent(ctx, id); err != nil {
			return e, err
		}
		if _, err = st.WriteAgentNote(ctx, id, markers[i], now); err != nil {
			return e, err
		}
	}
	active := 0
	var runtimes []*Runtime
	create := func() error {
		runtimes = nil
		for i, t := range templates {
			fixture := NewOfflineFixture(clock)
			fixture.Observe = func(messages []llm.Message) {
				for _, m := range messages {
					if m.Role == "user" && strings.Contains(m.Content, markers[1-i]) {
						e.ContextIsolated = false
					}
				}
			}
			factory := func(_ context.Context, chosen config.Template) (threads.Backend, error) {
				if chosen.ID != t.ID || chosen.Threads.ProfileDir != t.Threads.ProfileDir {
					return nil, errors.New("isolation soak: profile scope mismatch")
				}
				active++
				e.MaxActiveBackends = max(e.MaxActiveBackends, active)
				return &isolationSource{active: &active, ownURL: "https://fixture.invalid/@" + chosen.Threads.Username + "/post/seed"}, nil
			}
			r, err := New(Options{Store: st, Template: t, Now: clock, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends, Logger: logger, ThreadsFactory: factory})
			if err != nil {
				return err
			}
			runtimes = append(runtimes, r)
			// The synthetic transport requests these actual guarded tools each wake.
			// A blocked own-account reply is nonterminal and cannot reach the backend.
			fixture.Calls = []llm.ToolCall{
				SyntheticCall("read_feed", `{}`),
				SyntheticCall("note_write", string(mustJSON(map[string]string{"text": markers[i]}))),
				SyntheticCall("playbook_read", `{}`),
				SyntheticCall("reply", string(mustJSON(map[string]string{"text": "Synthetic refused own-account reply.", "target_username": templates[1-i].Threads.Username, "target_url": "https://threads.net/@" + templates[1-i].Threads.Username + "/post/1"}))),
				SyntheticCall("post", string(mustJSON(map[string]string{"text": fmt.Sprintf("Synthetic agent %d https://links.invalid/%d", i, i)}))),
			}
		}
		return nil
	}
	if err = create(); err != nil {
		return e, err
	}
	sample := func(hour int) {
		var m goruntime.MemStats
		goruntime.ReadMemStats(&m)
		e.Memory = append(e.Memory, IsolationMemory{LogicalHour: hour, AllocBytes: m.Alloc, HeapInuseBytes: m.HeapInuse, SysBytes: m.Sys, Goroutines: goruntime.NumGoroutine()})
	}
	sample(0)
	for hour := 0; hour < hours; hour++ {
		if err = ctx.Err(); err != nil {
			return e, err
		}
		now = start.Add(time.Duration(hour) * time.Hour)
		if hour == 6 || hour == 10 || hour == 36 {
			index, command := 0, "pause"
			if hour == 10 {
				command = "resume"
			}
			if hour == 36 {
				index, command = 1, "kill"
			}
			if err = st.OwnerControl(ctx, templates[index].ID, command, fmt.Sprintf("synthetic-%d", hour), "Synthetic owner control", now); err != nil {
				return e, err
			}
		}
		if hour == 24 {
			// Lose one in-flight wake across an actual SQLite close/reopen. No pending
			// effects or model conversation are loaded after recovery.
			if _, err = st.BeginWake(ctx, templates[0].ID, now, now.Add(time.Hour)); err != nil {
				return e, err
			}
			if err = st.Close(); err != nil {
				return e, err
			}
			st = nil
			st, err = store.Open(ctx, dbPath)
			if err != nil {
				return e, err
			}
			if err = create(); err != nil {
				return e, err
			}
			e.Restarts++
		}
		// Once executes one due session. Repeating it at the same injected instant
		// drains the two agents without introducing concurrent wake execution.
		// Exercise actual metric maintenance against a synthetic confirmed own
		// publication. These seed rows are fixture data, never real live posts.
		if hour == 0 {
			for _, t := range templates {
				if _, err = st.DB().ExecContext(ctx, `INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url) VALUES(?,'post','synthetic metric seed',0,?,?)`, t.ID, now.Format(time.RFC3339Nano), "https://fixture.invalid/@"+t.Threads.Username+"/post/seed"); err != nil {
					return e, err
				}
			}
		}
		for i, t := range templates {
			state, err := st.AgentState(ctx, t.ID)
			if err != nil {
				return e, err
			}
			switch state.Status {
			case "paused":
				e.Agents[i].PausedSkips++
			case "killed":
				e.Agents[i].KilledSkips++
			}
		}
		for drained := 0; ; drained++ {
			pending := false
			for _, r := range runtimes {
				state, err := st.AgentState(ctx, r.opts.Template.ID)
				if err != nil {
					return e, err
				}
				if state.Status != "running" {
					continue
				}
				if state.NextWake.IsZero() || !state.NextWake.After(now) {
					pending = true
				}
				at, ok, err := r.metrics.NextDue(ctx)
				if err != nil {
					return e, err
				}
				if ok && !at.After(now) {
					pending = true
				}
			}
			if !pending {
				break
			}
			if drained > 20 {
				return e, errors.New("isolation soak: scheduler did not drain")
			}
			if err = (Scheduler{Agents: runtimes, Now: clock, Wait: func(context.Context, time.Duration) error { return errors.New("isolation soak: unexpectedly waited") }}).Run(ctx, true); err != nil {
				return e, err
			}
			if active != 0 {
				return e, errors.New("isolation soak: backend leaked across wakes")
			}
		}
		for _, t := range templates {
			state, err := st.AgentState(ctx, t.ID)
			if err != nil {
				return e, err
			}
			usage, err := st.LLMUsageForDay(ctx, t.ID, now.Format("2006-01-02"))
			if err != nil {
				return e, err
			}
			e.Hours = append(e.Hours, IsolationHour{LogicalHour: hour, AgentID: t.ID, Status: state.Status, DailyCostUSD: usage.CostUSD})
			if t.ID == templates[0].ID && state.Status != "paused" && state.Status != "running" {
				e.StateIsolated = false
			}
			if t.ID == templates[1].ID && hour < 36 && state.Status != "running" {
				e.StateIsolated = false
			}
		}
		if hour > 0 && hour%12 == 0 {
			sample(hour)
		}
	}
	sample(hours)
	rows, err := st.DB().QueryContext(ctx, `SELECT agent_id,id,started_at,ended_at,end_reason,steps,tokens_in,tokens_out,cost_usd FROM wakes ORDER BY id`)
	if err != nil {
		return e, err
	}
	for rows.Next() {
		var w ScopedWakeEvidence
		if err = rows.Scan(&w.AgentID, &w.ID, &w.StartedAt, &w.EndedAt, &w.EndReason, &w.Steps, &w.TokensIn, &w.TokensOut, &w.CostUSD); err != nil {
			rows.Close()
			return e, err
		}
		e.Wakes = append(e.Wakes, w)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return e, err
	}
	interrupted := 0
	for _, w := range e.Wakes {
		at, err := time.Parse(time.RFC3339Nano, w.StartedAt)
		if err != nil {
			return e, errors.New("isolation soak: invalid wake time")
		}
		hour := int(at.Sub(start) / time.Hour)
		if w.AgentID == templates[0].ID && hour >= 6 && hour < 10 {
			e.StateIsolated = false
		}
		if w.AgentID == templates[1].ID && hour >= 36 {
			e.StateIsolated = false
		}
		if w.EndReason == "interrupted" {
			interrupted++
		}
	}
	if interrupted != 1 {
		return e, errors.New("isolation soak: restart recovery count mismatch")
	}
	for i := range e.Agents {
		a := &e.Agents[i]
		for _, w := range e.Wakes {
			if w.AgentID != a.AgentID {
				continue
			}
			a.TokensIn += w.TokensIn
			a.TokensOut += w.TokensOut
			a.CostUSD += w.CostUSD
			switch w.EndReason {
			case "completed":
				a.CompletedWakes++
			case "budget":
				a.BudgetStops++
			case "action_limit":
				a.ActionLimitStops++
			}
		}
		if err = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM actions WHERE agent_id=? AND dry_run=1`, a.AgentID).Scan(&a.DryRunActions); err != nil {
			return e, err
		}
	}
	if err = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM wakes WHERE kind='metrics'`).Scan(&e.MaintenanceWakes); err != nil {
		return e, err
	}
	e.Serial = e.MaxActiveBackends == 1 && active == 0
	e.BudgetIsolated = e.Agents[1].BudgetStops > 0 && e.Agents[0].BudgetStops == 0 && e.Agents[0].CompletedWakes > 0
	if !e.Serial || !e.StateIsolated || !e.BudgetIsolated || !e.ContextIsolated {
		return e, errors.New("isolation soak: observed isolation failure")
	}
	return e, nil
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

type isolationSource struct {
	active *int
	closed bool
	ownURL string
}

func (s *isolationSource) ReadFeed(context.Context) (threads.Page, error) {
	return threads.Page{Kind: "feed", Posts: []threads.Post{}}, nil
}
func (s *isolationSource) Search(context.Context, string) (threads.Page, error) {
	return threads.Page{}, errors.New("synthetic source: unexpected search")
}
func (s *isolationSource) ReadPost(context.Context, string) (threads.Page, error) {
	return threads.Page{}, errors.New("synthetic source: unexpected post read")
}
func (s *isolationSource) ReadOwnMetrics(context.Context) (threads.Metrics, error) {
	return threads.Metrics{Views: 40, Likes: 2}, nil
}
func (s *isolationSource) ReadActionMetrics(_ context.Context, url string) (threads.Metrics, error) {
	if url != s.ownURL {
		return threads.Metrics{}, threads.ErrMetricsUnavailable
	}
	return threads.Metrics{Views: 40, Likes: 2}, nil
}
func (s *isolationSource) Post(context.Context, string) (string, error) {
	return "", errors.New("synthetic source: forbidden submission")
}
func (s *isolationSource) Reply(context.Context, string, string) (string, error) {
	return "", errors.New("synthetic source: forbidden submission")
}
func (s *isolationSource) Close(context.Context) error {
	if !s.closed {
		*s.active--
		s.closed = true
	}
	return nil
}
