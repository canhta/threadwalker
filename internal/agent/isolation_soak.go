package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
)

type IsolationAgentEvidence struct {
	AgentID        string `json:"agent_id"`
	CompletedWakes int    `json:"completed_wakes"`
	BudgetStops    int    `json:"budget_stops"`
	// BudgetDeferrals counts hours the agent was over its daily cap and its next wake was not before the next UTC day.
	BudgetDeferrals  int     `json:"budget_deferrals"`
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
	e = IsolationEvidence{Synthetic: true, TimingKind: "logical_time", LogicalHours: hours, Serial: true, StateIsolated: true, BudgetIsolated: true, ContextIsolated: true, MemoryKind: "local_go_process_samples_with_synthetic_backends_not_elapsed_qualification"}
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
	existing, err := st.AgentCount(ctx)
	if err != nil {
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
		if _, err = st.SeedAction(ctx, store.SeededAction{AgentID: id, Kind: "post", Text: markers[i], DryRun: t.DryRun, PostedAt: now.Add(-time.Hour)}); err != nil {
			return e, err
		}
	}
	active := 0
	var runtimes []*Runtime
	var fixtures []*OfflineFixture
	create := func() error {
		runtimes, fixtures = nil, nil
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
				return isolationBackend(&active, "https://fixture.invalid/@"+chosen.Threads.Username+"/post/seed"), nil
			}
			r, err := New(Options{Store: st, Template: t, Now: clock, Wait: func(context.Context, time.Duration) error { return nil }, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends, Logger: logger, ThreadsFactory: factory})
			if err != nil {
				return err
			}
			runtimes = append(runtimes, r)
			fixtures = append(fixtures, fixture)
			// The synthetic transport requests these actual guarded tools each wake.
			// A blocked own-account reply is nonterminal and cannot reach the backend.
			other := templates[1-i].Threads.Username
			stored, err := st.NoteSeenPost(ctx, t.ID, store.SeenPost{URL: "https://threads.net/@" + other + "/post/1", MediaID: "fixture-id", Author: other, ReadAt: clock()})
			if err != nil {
				return err
			}
			fixture.Calls = []llm.ToolCall{
				SyntheticCall("find_posts", `{"source":"feed"}`),
				SyntheticCall("fetch_trends", `{}`),
				SyntheticCall("reply", string(mustJSON(map[string]string{"text": "Synthetic refused own-account reply.", "target": "p" + strconv.FormatInt(stored, 10)}))),
				SyntheticCall("post", ""),
				SyntheticCall("post", ""),
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
		// A post differs each wake, so the repeated-closing check never fires on the fixture.
		// The second post reaches the daily cap inside the wake that has one post
		// left; a tool hidden at the wake's start is refused before the guard.
		for i, f := range fixtures {
			for j, word := range []string{"agent", "second"} {
				f.Calls[len(f.Calls)-2+j] = SyntheticCall("post", string(mustJSON(map[string]string{"text": fmt.Sprintf("%s%d https://links.invalid/%d wake%d", word, i, i, hour)})))
			}
		}
		// Once executes one due session. Repeating it at the same injected instant
		// drains the two agents without introducing concurrent wake execution.
		// Exercise actual metric maintenance against a synthetic confirmed own
		// publication. These seed rows are fixture data, never real live posts.
		if hour == 0 {
			for _, t := range templates {
				if _, err = st.SeedAction(ctx, store.SeededAction{AgentID: t.ID, Kind: "post", Text: "synthetic metric seed", ThreadURL: "https://fixture.invalid/@" + t.Threads.Username + "/post/seed", MediaID: "seed", PostedAt: now}); err != nil {
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
			_, dayEnd, day := t.Limits.DayBounds(now)
			usage, err := st.LLMUsageForDay(ctx, t.ID, day)
			if err != nil {
				return e, err
			}
			if state.Status == "running" && usage.CostUSD >= t.Limits.MaxUSDPerDay {
				if state.NextWake.Before(dayEnd) {
					e.BudgetIsolated = false
				}
				e.Agents[idx(templates, t.ID)].BudgetDeferrals++
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
	// Execute the half-open [start,end) wake interval, then advance the clock to
	// its exact end before the final local memory sample.
	now = start.Add(time.Duration(hours) * time.Hour)
	sample(hours)
	records, err := st.WakeRecords(ctx, "")
	if err != nil {
		return e, err
	}
	for _, w := range records {
		e.Wakes = append(e.Wakes, ScopedWakeEvidence{AgentID: w.AgentID, WakeEvidence: WakeEvidence{ID: w.ID, StartedAt: w.StartedAt, EndedAt: w.EndedAt, EndReason: w.EndReason, Steps: w.Steps, TokensIn: w.TokensIn, TokensOut: w.TokensOut, CostUSD: w.CostUSD}})
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
		tally, err := st.ActionTally(ctx, a.AgentID)
		if err != nil {
			return e, err
		}
		a.DryRunActions = tally.DryRun
	}
	if e.MaintenanceWakes, err = st.MetricsWakeCount(ctx); err != nil {
		return e, err
	}
	e.Serial = e.MaxActiveBackends == 1 && active == 0
	e.BudgetIsolated = e.BudgetIsolated && e.Agents[1].BudgetDeferrals > 0 && e.Agents[0].BudgetDeferrals == 0 && e.Agents[0].CompletedWakes > 0
	if !e.Serial || !e.StateIsolated || !e.BudgetIsolated || !e.ContextIsolated {
		return e, errors.New("isolation soak: observed isolation failure")
	}
	return e, nil
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// isolationBackend reads an empty feed and the seeded own thread, refuses every
// other operation and counts itself out of active when closed.
func isolationBackend(active *int, ownURL string) *threads.Memory {
	m := threads.NewMemory()
	own := threads.Metrics{Views: 40, Likes: 2}
	m.Pages[threads.OpReadFeed] = threads.Page{Kind: "feed", Posts: []threads.Post{}}
	m.Own = own
	m.Actions[threads.SyntheticID(ownURL)] = own
	for _, op := range []string{threads.OpSearch, threads.OpReadPost, threads.OpPost, threads.OpReply} {
		m.Errors[op] = errors.New("synthetic source: unexpected " + op)
	}
	m.Hook = func(_ context.Context, c threads.Call) error {
		if c.Op == threads.OpClose {
			*active--
		}
		return nil
	}
	return m
}

func idx(templates []config.Template, id string) int {
	for i, t := range templates {
		if t.ID == id {
			return i
		}
	}
	return 0
}
