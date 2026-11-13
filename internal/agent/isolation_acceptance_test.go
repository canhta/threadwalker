package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/canhta/threadz/internal/stdx"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/telegram"
	"github.com/canhta/threadz/internal/threads"
)

const (
	isolationAgentOne = "agent-one"
	isolationAgentTwo = "agent-two"
	isolationUserOne  = "synthetic_one"
	isolationUserTwo  = "synthetic_two"
)

type isolationAgentFixture struct {
	store *store.Store
	one   *agent.Runtime
	two   *agent.Runtime
	fix1  *agent.OfflineFixture
	fix2  *agent.OfflineFixture
	tmpl1 config.Template
	tmpl2 config.Template
	now   time.Time
}

func isolationTwoTemplates(t *testing.T) (config.Template, config.Template) {
	t.Helper()
	root := t.TempDir()
	one := agent.OfflineTemplate()
	one.ID = isolationAgentOne
	one.Threads = config.ThreadsConfig{Backend: "stub", Username: isolationUserOne, ProfileDir: filepath.Join(root, "one")}
	one.SeedPlaybook = []string{"one-only-playbook-sentinel"}
	one.Limits.MaxUSDPerDay = 0.50
	one.Limits.RepliesPerUser7d = 1
	two := agent.OfflineTemplate()
	two.ID = isolationAgentTwo
	two.Threads = config.ThreadsConfig{Backend: "stub", Username: isolationUserTwo, ProfileDir: filepath.Join(root, "two")}
	two.SeedPlaybook = []string{"two-only-playbook-sentinel"}
	two.Limits.MaxUSDPerDay = 1.00
	return one, two
}

func newIsolationAgentFixture(t *testing.T, logger *slog.Logger) *isolationAgentFixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "agents.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	one, two := isolationTwoTemplates(t)
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	fix1 := agent.NewOfflineFixture(func() time.Time { return now })
	fix2 := agent.NewOfflineFixture(func() time.Time { return now })
	own := []string{isolationUserOne, isolationUserTwo}
	r1, err := agent.New(agent.Options{Store: st, Template: one, OwnUsernames: own, Logger: logger, Now: func() time.Time { return now }, HTTPClient: fix1.Client(), FetchTrends: fix1.Trends, FetchStudy: fix1.Study})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := agent.New(agent.Options{Store: st, Template: two, OwnUsernames: own, Logger: logger, Now: func() time.Time { return now }, HTTPClient: fix2.Client(), FetchTrends: fix2.Trends, FetchStudy: fix2.Study})
	if err != nil {
		t.Fatal(err)
	}
	for _, rt := range []*agent.Runtime{r1, r2} {
		if err = rt.Start(ctx); err != nil {
			t.Fatalf("starting isolated runtime: %v", err)
		}
	}
	return &isolationAgentFixture{store: st, one: r1, two: r2, fix1: fix1, fix2: fix2, tmpl1: one, tmpl2: two, now: now}
}

type isolationContextState struct {
	MemoryIndex  []store.MemoryEntry   `json:"memory_index"`
	Playbook     []store.PlaybookEntry `json:"playbook"`
	RemainingUSD float64               `json:"remaining_usd"`
}

func isolationContextStateOf(t *testing.T, rt *agent.Runtime) (isolationContextState, string) {
	t.Helper()
	messages, err := rt.Context(context.Background(), "agent")
	if err != nil {
		t.Fatalf("reading isolated context: %v", err)
	}
	if len(messages) != 2 || messages[1].Role != "user" {
		t.Fatalf("unexpected context shape: %+v", messages)
	}
	var state isolationContextState
	if err := json.Unmarshal([]byte(messages[1].Content), &state); err != nil {
		t.Fatalf("decoding isolated context: %v", err)
	}
	return state, messages[0].Content + "\n" + messages[1].Content
}

func TestIsolationAcceptanceNotesAndPlaybookAreInvisibleAcrossAgents(t *testing.T) {
	env := newIsolationAgentFixture(t, nil)
	ctx := context.Background()
	if err := env.store.AddMemoryEntry(ctx, isolationAgentOne, "syntheticnote oneonlysentinel", env.now, store.MemoryActor{}); err != nil {
		t.Fatal(err)
	}
	if err := env.store.AddMemoryEntry(ctx, isolationAgentTwo, "syntheticnote twoonlysentinel", env.now, store.MemoryActor{}); err != nil {
		t.Fatal(err)
	}

	for id, text := range map[string]string{isolationAgentOne: "syntheticnote oneonlysentinel", isolationAgentTwo: "syntheticnote twoonlysentinel"} {
		if _, err := env.store.WriteTopic(ctx, id, "sentinel", "", text, env.now); err != nil {
			t.Fatal(err)
		}
	}
	stateOne, allOne := isolationContextStateOf(t, env.one)
	stateTwo, allTwo := isolationContextStateOf(t, env.two)
	if len(stateOne.MemoryIndex) != 1 || stateOne.MemoryIndex[0].Text != "syntheticnote oneonlysentinel" || len(stateTwo.MemoryIndex) != 1 || stateTwo.MemoryIndex[0].Text != "syntheticnote twoonlysentinel" {
		t.Fatalf("memory indexes crossed scopes: %v %v", stateOne.MemoryIndex, stateTwo.MemoryIndex)
	}
	if len(stateOne.Playbook) != 1 || stateOne.Playbook[0].Text != "one-only-playbook-sentinel" {
		t.Fatalf("agent-one playbook = %+v", stateOne.Playbook)
	}
	if len(stateTwo.Playbook) != 1 || stateTwo.Playbook[0].Text != "two-only-playbook-sentinel" {
		t.Fatalf("agent-two playbook = %+v", stateTwo.Playbook)
	}
	if strings.Contains(allOne, "twoonlysentinel") || strings.Contains(allOne, "two-only-playbook-sentinel") {
		t.Fatal("agent-one context exposed agent-two state")
	}
	if strings.Contains(allTwo, "oneonlysentinel") || strings.Contains(allTwo, "one-only-playbook-sentinel") {
		t.Fatal("agent-two context exposed agent-one state")
	}

	// Exercise the note_search tool through a real scripted wake so the
	// isolation assertion covers the agent's action surface, not only storage.
	observed := make([][]llm.Message, 2)
	env.fix1.Calls = []llm.ToolCall{agent.SyntheticCall("note_search", `{"query":"syntheticnote"}`)}
	env.fix2.Calls = []llm.ToolCall{agent.SyntheticCall("note_search", `{"query":"syntheticnote"}`)}
	env.fix1.Observe = func(messages []llm.Message) { observed[0] = append([]llm.Message(nil), messages...) }
	env.fix2.Observe = func(messages []llm.Message) { observed[1] = append([]llm.Message(nil), messages...) }
	for i, rt := range []*agent.Runtime{env.one, env.two} {
		agentID := []string{isolationAgentOne, isolationAgentTwo}[i]
		if err := env.store.SetNextWake(ctx, agentID, env.now); err != nil {
			t.Fatal(err)
		}
		if _, err := rt.Wake(ctx); err != nil {
			t.Fatalf("note isolation wake %d: %v", i, err)
		}
	}
	for i, wantOwn := range []string{"oneonlysentinel", "twoonlysentinel"} {
		wantOther := "twoonlysentinel"
		if i == 1 {
			wantOther = "oneonlysentinel"
		}
		foundOwn := false
		for _, message := range observed[i] {
			if message.Role != "tool" {
				continue
			}
			if strings.Contains(message.Content, wantOwn) {
				foundOwn = true
			}
			if strings.Contains(message.Content, wantOther) {
				t.Fatalf("agent %d note_search returned another agent's note: %s", i, message.Content)
			}
		}
		if !foundOwn {
			t.Fatalf("agent %d note_search did not return its own note", i)
		}
	}
}

type isolationWakeTemplate interface{ TemplateIDForAcceptance() string }

func TestIsolationAcceptanceLedgerAndBudgetAreIndependent(t *testing.T) {
	env := newIsolationAgentFixture(t, nil)
	ctx := context.Background()
	record := func(agentID string, wakeID int64, usage store.LLMUsage) {
		if err := env.store.RecordLLMUsage(ctx, agentID, wakeID, "2026-11-11", usage); err != nil {
			t.Fatalf("recording usage for %s: %v", agentID, err)
		}
	}
	for _, tc := range []struct {
		id   string
		used store.LLMUsage
	}{
		{isolationAgentOne, store.LLMUsage{TokensIn: 100, TokensOut: 20, CostUSD: 0.10}},
		{isolationAgentTwo, store.LLMUsage{TokensIn: 300, TokensOut: 30, CostUSD: 0.20}},
	} {
		wakeID, err := env.store.BeginWake(ctx, tc.id, env.now, env.now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		record(tc.id, wakeID, tc.used)
		if err := env.store.FinishWake(ctx, wakeID, env.now, 1, "completed"); err != nil {
			t.Fatal(err)
		}
		if err := env.store.SetNextWake(ctx, tc.id, env.now); err != nil {
			t.Fatal(err)
		}
	}
	one, _ := isolationContextStateOf(t, env.one)
	two, _ := isolationContextStateOf(t, env.two)
	if one.RemainingUSD != 0.40 {
		t.Errorf("agent-one remaining = %v, want 0.40", one.RemainingUSD)
	}
	if two.RemainingUSD != 0.80 {
		t.Errorf("agent-two remaining = %v, want 0.80", two.RemainingUSD)
	}

	// Exhaust only agent-one's budget in a fresh process state and verify the
	// other agent still completes a real wake.
	env.tmpl1.Limits.MaxUSDPerDay = 0.05
	exhausted, err := agent.New(agent.Options{Store: env.store, Template: env.tmpl1, OwnUsernames: []string{isolationUserOne, isolationUserTwo}, Now: func() time.Time { return env.now }, HTTPClient: env.fix1.Client(), FetchTrends: env.fix1.Trends, FetchStudy: env.fix1.Study})
	if err != nil {
		t.Fatal(err)
	}
	env.fix1.Calls = []llm.ToolCall{}
	env.fix2.Calls = []llm.ToolCall{}
	exhaustedResult, err := exhausted.Wake(ctx)
	if err != nil {
		t.Fatalf("budget wake returned a nonzero exit error: %v", err)
	}
	if exhaustedResult.EndReason != "budget" || exhaustedResult.Steps != 0 {
		t.Fatalf("exhausted agent wake = %+v, want budget stop before a request", exhaustedResult)
	}
	resultTwo, err := env.two.Wake(ctx)
	if err != nil || resultTwo.EndReason != "completed" {
		t.Fatalf("agent-two wake after peer budget exhaustion: %+v %v", resultTwo, err)
	}
}

func TestIsolationAcceptanceContactsAndRateCountersAreScoped(t *testing.T) {
	env := newIsolationAgentFixture(t, nil)
	ctx := context.Background()
	clock := env.now
	guards := map[string]*guard.Guard{}
	for i, tmpl := range []config.Template{env.tmpl1, env.tmpl2} {
		wakeID, err := env.store.BeginWake(ctx, tmpl.ID, env.now, env.now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		g, err := guard.New(guard.Scope{
			Store:        env.store,
			Template:     tmpl,
			OwnUsernames: []string{isolationUserOne, isolationUserTwo},
			WakeID:       wakeID,
			Now:          func() time.Time { return clock },
		})
		if err != nil {
			t.Fatalf("configuring scoped guard %d: %v", i, err)
		}
		guards[tmpl.ID] = g
	}
	reply := func(g *guard.Guard, target, thread string) error {
		g.NoteRead("tid", thread, "")
		_, err := g.Execute(ctx, guard.Attempt{
			Tool:      "reply",
			Arguments: `{"text":"synthetic reply","target_username":"` + target + `","target_id": "tid", "target_url":"` + thread + `"}`,
		}, func() (string, error) { return "synthetic result", nil })
		return err
	}

	// A permanent do-not-contact flag belongs to one agent only.
	if err := env.store.MarkDoNotContact(ctx, isolationAgentOne, "blocked_target", "owner_request"); err != nil {
		t.Fatal(err)
	}
	if err := reply(guards[isolationAgentOne], "blocked_target", "https://threads.net/@blocked_target/post/1"); !errors.Is(err, guard.ErrBlocked) || !strings.Contains(err.Error(), "do_not_contact") {
		t.Fatalf("agent-one ignored its own do-not-contact entry: %v", err)
	}
	if err := reply(guards[isolationAgentTwo], "blocked_target", "https://threads.net/@blocked_target/post/1"); err != nil {
		t.Fatalf("agent-two was blocked by agent-one's contact entry: %v", err)
	}

	// Per-user reply counters are also scoped to the replying agent.
	if err := reply(guards[isolationAgentOne], "ordinary_author", "https://threads.net/@ordinary_author/post/1"); err != nil {
		t.Fatalf("agent-one first scoped reply: %v", err)
	}
	clock = env.now.Add(2 * time.Second)
	if err := reply(guards[isolationAgentOne], "ordinary_author", "https://threads.net/@ordinary_author/post/2"); !errors.Is(err, guard.ErrBlocked) || !strings.Contains(err.Error(), "replies_per_user_7d") {
		t.Fatalf("agent-one per-user counter not enforced: %v", err)
	}
	if err := reply(guards[isolationAgentTwo], "ordinary_author", "https://threads.net/@ordinary_author/post/3"); err != nil {
		t.Fatalf("agent-two inherited agent-one's per-user counter: %v", err)
	}
}

func TestIsolationAcceptancePauseAndKillAreIndependent(t *testing.T) {
	env := newIsolationAgentFixture(t, nil)
	ctx := context.Background()
	env.fix1.Calls = []llm.ToolCall{}
	env.fix2.Calls = []llm.ToolCall{}
	if err := env.store.OwnerControl(ctx, isolationAgentOne, "pause", "pause-one", "Agent agent-one: pause received.", env.now); err != nil {
		t.Fatal(err)
	}
	if state, err := env.store.AgentState(ctx, isolationAgentOne); err != nil || state.Status != "paused" {
		t.Fatalf("agent-one state after pause: %+v %v", state, err)
	}
	if _, err := env.one.Wake(ctx); !errors.Is(err, agent.ErrNotRunning) {
		t.Fatalf("paused agent woke: %v", err)
	}
	resultTwo, err := env.two.Wake(ctx)
	if err != nil || resultTwo.EndReason != "completed" {
		t.Fatalf("running peer blocked by pause: %+v %v", resultTwo, err)
	}
	if err = env.store.OwnerControl(ctx, isolationAgentOne, "kill", "kill-one", "Agent agent-one: kill received.", env.now); err != nil {
		t.Fatal(err)
	}
	if err = env.store.OwnerControl(ctx, isolationAgentOne, "resume", "resume-one", "Agent agent-one: resume received.", env.now); err != nil {
		t.Fatal(err)
	}
	if state, err := env.store.AgentState(ctx, isolationAgentOne); err != nil || state.Status != "killed" {
		t.Fatalf("killed agent was revived: %+v %v", state, err)
	}
	if _, err = env.one.Wake(ctx); !errors.Is(err, agent.ErrNotRunning) {
		t.Fatalf("killed agent woke: %v", err)
	}
	if err = env.store.SetNextWake(ctx, isolationAgentTwo, env.now); err != nil {
		t.Fatal(err)
	}
	if resultTwo, err = env.two.Wake(ctx); err != nil || resultTwo.EndReason != "completed" {
		t.Fatalf("running peer blocked by kill: %+v %v", resultTwo, err)
	}
}

type isolationBlockingTransport struct {
	base    http.RoundTripper
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *isolationBlockingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	return b.base.RoundTrip(req)
}

func TestIsolationAcceptanceSchedulerSerializesOneWakeAtATime(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "scheduler.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	one, two := isolationTwoTemplates(t)
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	blocked := &isolationBlockingTransport{base: nil, entered: make(chan struct{}), release: make(chan struct{})}
	fixtureOne := agent.NewOfflineFixture(func() time.Time { return now })
	fixtureOne.Calls = []llm.ToolCall{}
	blocked.base = fixtureOne
	r1, err := agent.New(agent.Options{Store: st, Template: one, OwnUsernames: []string{isolationUserOne, isolationUserTwo}, Now: func() time.Time { return now }, HTTPClient: &http.Client{Transport: blocked}, FetchTrends: fixtureOne.Trends, FetchStudy: fixtureOne.Study})
	if err != nil {
		t.Fatal(err)
	}
	fixtureTwo := agent.NewOfflineFixture(func() time.Time { return now })
	fixtureTwo.Calls = []llm.ToolCall{}
	r2, err := agent.New(agent.Options{Store: st, Template: two, OwnUsernames: []string{isolationUserOne, isolationUserTwo}, Now: func() time.Time { return now }, HTTPClient: fixtureTwo.Client(), FetchTrends: fixtureTwo.Trends, FetchStudy: fixtureTwo.Study})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := agent.Scheduler{Agents: []*agent.Runtime{r1, r2}, Now: func() time.Time { return now }, Wait: stdx.Sleep}
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx, true) }()
	select {
	case <-blocked.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first wake never reached the blocking transport")
	}
	var open, second int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM wakes WHERE ended_at IS NULL`).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM wakes WHERE agent_id=?`, isolationAgentTwo).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if open != 1 || second != 0 {
		t.Fatalf("scheduler ran wakes concurrently: open=%d second-agent-wakes=%d", open, second)
	}
	close(blocked.release)
	if err := <-done; err != nil {
		t.Fatalf("serialized scheduler run: %v", err)
	}
}

func TestIsolationAcceptanceSchedulerSerializesMetricMaintenanceWithAgentWake(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "maintenance.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	one, two := isolationTwoTemplates(t)
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	// A confirmed action makes metric maintenance due before either agent wake.
	if err = st.EnsureAgent(ctx, isolationAgentOne); err != nil {
		t.Fatal(err)
	}
	posted := now.Add(-2 * time.Hour).Format(time.RFC3339Nano)
	if _, err = st.DB().Exec(`INSERT INTO actions(agent_id,kind,text,posted_at,thread_url,dry_run,media_id) VALUES(?,?,?,?,?,0,'m')`, isolationAgentOne, "post", "synthetic confirmed action", posted, "https://example.test/own"); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	factory := func(context.Context, config.Template) (threads.Backend, error) {
		once.Do(func() { close(entered) })
		<-release
		return nil, errors.New("synthetic source unavailable")
	}
	r1, err := agent.New(agent.Options{Store: st, Template: one, OwnUsernames: []string{isolationUserOne, isolationUserTwo}, Now: func() time.Time { return now }, ThreadsFactory: factory})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := agent.New(agent.Options{Store: st, Template: two, OwnUsernames: []string{isolationUserOne, isolationUserTwo}, Now: func() time.Time { return now }, ThreadsFactory: factory})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := agent.Scheduler{Agents: []*agent.Runtime{r1, r2}, Now: func() time.Time { return now }, Wait: stdx.Sleep}
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx, true) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("metric maintenance never opened its source")
	}
	var open, second int
	var openKind string
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM wakes WHERE ended_at IS NULL`).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if err = st.DB().QueryRow(`SELECT kind FROM wakes WHERE ended_at IS NULL`).Scan(&openKind); err != nil {
		t.Fatal(err)
	}
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM wakes WHERE agent_id=?`, isolationAgentTwo).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if open != 1 || openKind != "metrics" || second != 0 {
		t.Fatalf("maintenance not serialized: open=%d kind=%q second-agent-wakes=%d", open, openKind, second)
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatalf("maintenance scheduler run: %v", err)
	}
}

type isolationFakeBot struct{ updates []telegram.Update }

func (b *isolationFakeBot) GetUpdates(context.Context, int64, int) ([]telegram.Update, error) {
	return b.updates, nil
}
func (b *isolationFakeBot) SendMessage(context.Context, int64, string, int64) (int64, error) {
	return 1, nil
}
func (b *isolationFakeBot) SetMyCommands(context.Context, []telegram.BotCommand) error { return nil }
func (b *isolationFakeBot) DeleteMessage(context.Context, int64, int64) error          { return nil }

func TestIsolationAcceptanceOwnerCommandSelectsAgent(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "owner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	one, two := isolationTwoTemplates(t)
	for _, id := range []string{isolationAgentOne, isolationAgentTwo} {
		if err = st.EnsureAgent(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	bot := &isolationFakeBot{updates: []telegram.Update{
		{UpdateID: 1, Message: &telegram.Message{Chat: telegram.Chat{ID: 77}, Text: "/pause agent-one"}},
		{UpdateID: 2, Message: &telegram.Message{Chat: telegram.Chat{ID: 77}, Text: "/kill agent-two"}},
	}}
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	service, err := telegram.NewService(telegram.ServiceOptions{
		Store:       st,
		Bot:         bot,
		BotKey:      "synthetic-owner-bot",
		OwnerChatID: 77,
		Templates:   []config.Template{one, two},
		Now:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Poll(ctx, 0); err != nil {
		t.Fatalf("polling selected-agent commands: %v", err)
	}
	if state, err := st.AgentState(ctx, isolationAgentOne); err != nil || state.Status != "paused" {
		t.Fatalf("selected pause state: %+v %v", state, err)
	}
	if state, err := st.AgentState(ctx, isolationAgentTwo); err != nil || state.Status != "killed" {
		t.Fatalf("selected kill state: %+v %v", state, err)
	}
}

func TestIsolationAcceptanceWakeLogsCorrelateAgentsWithoutPayloads(t *testing.T) {
	var logs bytes.Buffer
	logger, err := logging.New(&logs, "debug", "json")
	if err != nil {
		t.Fatal(err)
	}
	env := newIsolationAgentFixture(t, logger)
	ctx := context.Background()
	if err := env.store.AddMemoryEntry(ctx, isolationAgentOne, "oneonlysentinel", env.now, store.MemoryActor{}); err != nil {
		t.Fatal(err)
	}
	if err := env.store.AddMemoryEntry(ctx, isolationAgentTwo, "twoonlysentinel", env.now, store.MemoryActor{}); err != nil {
		t.Fatal(err)
	}
	env.fix1.Calls = []llm.ToolCall{}
	env.fix2.Calls = []llm.ToolCall{}
	for _, rt := range []*agent.Runtime{env.one, env.two} {
		if _, err := rt.Wake(ctx); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("non-JSON operational log: %q", line)
		}
		if record["event"] == logging.WakeStarted {
			if id, _ := record["agent_id"].(string); id != "" {
				seen[id] = true
			}
		}
		for _, forbidden := range []string{"oneonlysentinel", "twoonlysentinel", "offline-dummy-key", "Synthetic note for the next wake."} {
			if strings.Contains(line, forbidden) {
				t.Fatalf("operational log leaked payload %q: %s", forbidden, line)
			}
		}
	}
	if !seen[isolationAgentOne] || !seen[isolationAgentTwo] {
		t.Fatalf("wake logs missing agent correlation: %v", seen)
	}
}
