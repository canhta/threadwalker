package tools_test

// Independent acceptance suite for the #10 guarded playbook tools. It drives
// the real dispatcher and guard, uses temporary SQLite and injected time, and
// asserts only the public tool contract plus published SQLite rows.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/guard"
	"github.com/canhta/threadwalker/internal/llm"
	"github.com/canhta/threadwalker/internal/store"
	"github.com/canhta/threadwalker/internal/tools"
)

var playbookToolsAt = time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)

type playbookHarness struct {
	store      *store.Store
	dispatcher *tools.Dispatcher
	opts       tools.Options
}

func newPlaybookHarness(t *testing.T, mutate ...func(*tools.Options)) *playbookHarness {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "playbook-tools.db"))
	if err != nil {
		t.Fatalf("opening playbook tool store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})
	if _, err := st.DB().Exec(`INSERT INTO agents(id,status) VALUES('agent','running'); INSERT INTO wakes(id,agent_id,started_at) VALUES(1,'agent','2026-06-01T07:00:00Z')`); err != nil {
		t.Fatalf("seeding agent and wake: %v", err)
	}
	now := playbookToolsAt
	opts := tools.Options{
		Store:    st,
		Template: config.Template{ID: "agent", DryRun: true, Limits: config.LimitsConfig{PostsPerDay: 100, RepliesPerDay: 100, RepliesPerUser7d: 10, MinSecondsBetweenActions: 1, MinMinutesBetweenWakes: 15}},
		WakeID:   1,
		// Advance injected time so independent action attempts are not blocked
		// by the per-action spacing rule before hypothesis validation.
		Now: func() time.Time { now = now.Add(2 * time.Second); return now },
	}
	for _, change := range mutate {
		change(&opts)
	}
	g, err := guard.New(guard.Options{Store: st, Template: opts.Template, WakeID: 1, Now: opts.Now})
	if err != nil {
		t.Fatalf("building guard: %v", err)
	}
	d, err := tools.NewDispatcher(g, opts)
	if err != nil {
		t.Fatalf("building dispatcher: %v", err)
	}
	return &playbookHarness{store: st, dispatcher: d, opts: opts}
}

func (h *playbookHarness) call(t *testing.T, name, args string) (string, error) {
	t.Helper()
	return h.dispatcher.Dispatch(context.Background(), llm.ToolCall{ID: "call-1", Type: "function", Function: llm.CallFunction{Name: name, Arguments: args}})
}

func (h *playbookHarness) mustCall(t *testing.T, name, args string) string {
	t.Helper()
	out, err := h.call(t, name, args)
	if err != nil {
		t.Fatalf("%s(%s): %v", name, args, err)
	}
	return out
}

func (h *playbookHarness) define(t *testing.T, name string) map[string]any {
	t.Helper()
	for _, definition := range h.dispatcher.Definitions() {
		if definition.Function.Name != name {
			continue
		}
		var schema map[string]any
		if err := json.Unmarshal(definition.Function.Parameters, &schema); err != nil {
			t.Fatalf("decoding %s schema: %v", name, err)
		}
		return schema
	}
	t.Fatalf("%s definition missing", name)
	return nil
}

func (h *playbookHarness) initialize(t *testing.T, agentID string, texts []string) []store.PlaybookEntry {
	t.Helper()
	if _, err := h.store.InitializePlaybook(context.Background(), agentID, texts, playbookToolsAt); err != nil {
		t.Fatalf("initializing %q: %v", agentID, err)
	}
	entries, err := h.store.PlaybookEntries(context.Background(), agentID, 100)
	if err != nil {
		t.Fatalf("reading entries for %q: %v", agentID, err)
	}
	return entries
}

func (h *playbookHarness) add(t *testing.T, agentID, text string) store.PlaybookEntry {
	t.Helper()
	entry, err := h.store.ProposePlaybook(context.Background(), agentID, 0, text, playbookToolsAt)
	if err != nil {
		t.Fatalf("adding entry for %q: %v", agentID, err)
	}
	return entry
}

func decodePlaybookList(t *testing.T, out string) []store.PlaybookEntry {
	t.Helper()
	var entries []store.PlaybookEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("decoding playbook list %s: %v", out, err)
	}
	if entries == nil {
		t.Fatalf("playbook list is null: %s", out)
	}
	return entries
}

func decodePlaybookEntry(t *testing.T, out string) store.PlaybookEntry {
	t.Helper()
	var entry store.PlaybookEntry
	if err := json.Unmarshal([]byte(out), &entry); err != nil {
		t.Fatalf("decoding playbook entry %s: %v", out, err)
	}
	return entry
}

func (h *playbookHarness) actionCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := h.store.DB().QueryRow(`SELECT COUNT(*) FROM actions`).Scan(&count); err != nil {
		t.Fatalf("counting actions: %v", err)
	}
	return count
}

func TestPlaybookToolSchemasExposeNoStatusCountersOrReplacement(t *testing.T) {
	h := newPlaybookHarness(t)

	read := h.define(t, "playbook_read")
	readProps, ok := read["properties"].(map[string]any)
	if !ok {
		t.Fatalf("playbook_read properties = %#v", read["properties"])
	}
	if len(readProps) != 1 {
		t.Fatalf("playbook_read properties = %v, want only optional limit", readProps)
	}
	limit, ok := readProps["limit"].(map[string]any)
	if !ok || limit["type"] != "integer" || limit["minimum"] != float64(1) || limit["maximum"] != float64(100) {
		t.Fatalf("playbook_read limit schema = %#v", readProps["limit"])
	}
	if required, _ := read["required"].([]any); len(required) != 0 {
		t.Fatalf("playbook_read required = %v, want none", required)
	}
	if read["additionalProperties"] != false {
		t.Fatalf("playbook_read additionalProperties = %v, want false", read["additionalProperties"])
	}

	propose := h.define(t, "playbook_propose")
	proposeProps, ok := propose["properties"].(map[string]any)
	if !ok {
		t.Fatalf("playbook_propose properties = %#v", propose["properties"])
	}
	if len(proposeProps) != 2 {
		t.Fatalf("playbook_propose properties = %v, want text and hypothesis_id", proposeProps)
	}
	text, ok := proposeProps["text"].(map[string]any)
	if !ok || text["type"] != "string" || text["minLength"] != float64(1) {
		t.Fatalf("playbook_propose text schema = %#v", proposeProps["text"])
	}
	hypothesis, ok := proposeProps["hypothesis_id"].(map[string]any)
	if !ok || hypothesis["type"] != "integer" || hypothesis["minimum"] != float64(1) {
		t.Fatalf("playbook_propose hypothesis_id schema = %#v", proposeProps["hypothesis_id"])
	}
	required, _ := propose["required"].([]any)
	if len(required) != 1 || required[0] != "text" {
		t.Fatalf("playbook_propose required = %v, want [text]", required)
	}
	if propose["additionalProperties"] != false {
		t.Fatalf("playbook_propose additionalProperties = %v, want false", propose["additionalProperties"])
	}

	forbidden := []string{"status", "n", "successes", "revision", "counters", "delete", "replace", "entries", "texts", "mode", "action_id", "list"}
	for _, name := range []string{"playbook_read", "playbook_propose"} {
		schema := h.define(t, name)
		props, _ := schema["properties"].(map[string]any)
		for _, key := range forbidden {
			if _, exists := props[key]; exists {
				t.Fatalf("%s exposes forbidden property %q", name, key)
			}
		}
	}
}

func TestPlaybookReadReturnsOnlyOwnBoundedEntries(t *testing.T) {
	h := newPlaybookHarness(t)
	own := h.initialize(t, "agent", []string{"short questions work", "lists invite replies"})
	if err := h.store.EnsureAgent(context.Background(), "foreign"); err != nil {
		t.Fatal(err)
	}
	foreign := h.add(t, "foreign", "FOREIGN SECRET HYPOTHESIS")

	// Grow beyond the default bounded context using decided entries so the
	// testing cap does not interfere with the limit assertions.
	for i := 0; i < 10; i++ {
		entry := h.add(t, "agent", "bounded hypothesis "+string(rune('a'+i)))
		if _, err := h.store.DB().Exec(`UPDATE playbook SET status='established',n=9,successes=6,decided_at=? WHERE id=?`, playbookToolsAt.Format(time.RFC3339Nano), entry.ID); err != nil {
			t.Fatal(err)
		}
	}

	out := h.mustCall(t, "playbook_read", `{}`)
	entries := decodePlaybookList(t, out)
	if len(entries) != 10 {
		t.Fatalf("default read returned %d entries, want 10", len(entries))
	}
	for _, entry := range entries {
		if entry.AgentID != "agent" || entry.ID == foreign.ID || strings.Contains(entry.Text, "FOREIGN") {
			t.Fatalf("read leaked foreign entry: %+v", entry)
		}
	}
	if entries[0].Status != "testing" || entries[0].Text != own[0].Text {
		t.Fatalf("read ordering starts with %+v, want own testing entry", entries[0])
	}
	if got := decodePlaybookList(t, h.mustCall(t, "playbook_read", `{"limit":2}`)); len(got) != 2 {
		t.Fatalf("limit 2 returned %d entries", len(got))
	}
	if got := decodePlaybookList(t, h.mustCall(t, "playbook_read", `{"limit":100}`)); len(got) != 12 {
		t.Fatalf("limit 100 returned %d entries, want 12", len(got))
	}
	for _, args := range []string{`{"limit":0}`, `{"limit":101}`, `{"limit":-1}`, `{"limit":"5"}`, `{"hypothesis_id":1}`} {
		out, err := h.call(t, "playbook_read", args)
		if err == nil {
			t.Fatalf("invalid read %s returned %s", args, out)
		}
	}
}

func TestPlaybookProposeToolAddsAndAmendsOnlyOwnTestingEntry(t *testing.T) {
	h := newPlaybookHarness(t)
	added := decodePlaybookEntry(t, h.mustCall(t, "playbook_propose", `{"text":"  questions in the first line  "}`))
	if added.ID <= 0 || added.AgentID != "agent" || added.Text != "questions in the first line" || added.Status != "testing" || added.Revision != 1 || added.N != 0 || added.Successes != 0 {
		t.Fatalf("proposed entry = %+v", added)
	}

	if _, err := h.store.DB().Exec(`UPDATE playbook SET n=6,successes=4 WHERE id=?`, added.ID); err != nil {
		t.Fatal(err)
	}
	same := decodePlaybookEntry(t, h.mustCall(t, "playbook_propose", `{"text":"questions in the first line","hypothesis_id":`+jsonInt(added.ID)+`}`))
	if same.Revision != 1 || same.N != 6 || same.Successes != 4 {
		t.Fatalf("identical proposal changed state: %+v", same)
	}
	amended := decodePlaybookEntry(t, h.mustCall(t, "playbook_propose", `{"text":"questions beat statements","hypothesis_id":`+jsonInt(added.ID)+`}`))
	if amended.ID != added.ID || amended.Text != "questions beat statements" || amended.Revision != 2 || amended.N != 0 || amended.Successes != 0 || !amended.CreatedAt.Equal(added.CreatedAt) {
		t.Fatalf("amended entry = %+v", amended)
	}

	before := decodePlaybookList(t, h.mustCall(t, "playbook_read", `{"limit":100}`))
	replacements := []string{
		`{"texts":["replacement one","replacement two"]}`,
		`{"entries":[]}`,
		`{"replace":[]}`,
		`{"list":true}`,
		`{"text":"ok","status":"established"}`,
		`{"text":"ok","n":99}`,
		`{"text":"ok","successes":99}`,
		`{"text":"ok","revision":9}`,
		`{"text":"ok","mode":"testing"}`,
		`{"text":"ok","action_id":1}`,
		`{"text":"ok","delete":true}`,
		`{"text":"ok","hypothesis_id":0}`,
		`{"text":"ok","hypothesis_id":-2}`,
		`{"hypothesis_id":` + jsonInt(added.ID) + `}`,
		`{"text":"   "}`,
		`{"text":"` + strings.Repeat("x", store.MaxHypothesisTextRunes+1) + `"}`,
	}
	for _, args := range replacements {
		if out, err := h.call(t, "playbook_propose", args); err == nil {
			t.Fatalf("forbidden proposal %s accepted: %s", args, out)
		}
	}
	after := decodePlaybookList(t, h.mustCall(t, "playbook_read", `{"limit":99}`))
	if len(after) != len(before) || after[0] != before[0] || after[0] != amended {
		t.Fatalf("rejected proposals changed entries: before=%+v after=%+v", before, after)
	}

	// A foreign or decided entry can never be amended through the tool.
	if err := h.store.EnsureAgent(context.Background(), "foreign"); err != nil {
		t.Fatal(err)
	}
	foreign := h.add(t, "foreign", "foreign text")
	decided := h.add(t, "agent", "decided text")
	if _, err := h.store.DB().Exec(`UPDATE playbook SET status='rejected',decided_at=? WHERE id=?`, playbookToolsAt.Format(time.RFC3339Nano), decided.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{foreign.ID, decided.ID, 999999} {
		out, err := h.call(t, "playbook_propose", `{"text":"attempted rewrite","hypothesis_id":`+jsonInt(id)+`}`)
		if err == nil {
			t.Fatalf("proposal to immutable id %d accepted: %s", id, out)
		}
	}
	stored := decodePlaybookList(t, h.mustCall(t, "playbook_read", `{"limit":98}`))
	if len(stored) != len(before)+1 {
		t.Fatalf("immutable proposals changed entries: %+v", stored)
	}
}

func TestPlaybookGuardRejectsForeignDecidedAndUnknownHypotheses(t *testing.T) {
	h := newPlaybookHarness(t)
	own := h.add(t, "agent", "own testing")
	if err := h.store.EnsureAgent(context.Background(), "foreign"); err != nil {
		t.Fatal(err)
	}
	foreign := h.add(t, "foreign", "foreign testing")
	decided := h.add(t, "agent", "own decided")
	if _, err := h.store.DB().Exec(`UPDATE playbook SET status='established',n=20,successes=15,decided_at=? WHERE id=?`, playbookToolsAt.Format(time.RFC3339Nano), decided.ID); err != nil {
		t.Fatal(err)
	}

	out := h.mustCall(t, "post", `{"text":"own synthetic post","hypothesis_ids":[`+jsonInt(own.ID)+`]}`)
	if !strings.Contains(out, `"action_id"`) {
		t.Fatalf("guarded post output = %s", out)
	}
	if count := h.actionCount(t); count != 1 {
		t.Fatalf("accepted post actions = %d, want 1", count)
	}
	var idsJSON string
	if err := h.store.DB().QueryRow(`SELECT hypothesis_ids_json FROM actions WHERE agent_id='agent'`).Scan(&idsJSON); err != nil {
		t.Fatalf("reading action hypotheses: %v", err)
	}
	var ids []int64
	if err := json.Unmarshal([]byte(idsJSON), &ids); err != nil || len(ids) != 1 || ids[0] != own.ID {
		t.Fatalf("action hypothesis ids = %q err=%v", idsJSON, err)
	}

	for name, id := range map[string]int64{"foreign": foreign.ID, "decided": decided.ID, "unknown": 987654} {
		for _, tool := range []string{"post", "reply"} {
			args := `{"text":"blocked synthetic text","hypothesis_ids":[` + jsonInt(id) + `]}`
			if tool == "reply" {
				args = `{"text":"blocked synthetic reply","target_url":"https://threads.example/post/1","target_username":"someone","hypothesis_ids":[` + jsonInt(id) + `]}`
			}
			if out, err := h.call(t, tool, args); err == nil {
				t.Fatalf("%s with %s hypothesis accepted: %s", tool, name, out)
			}
		}
	}
	if count := h.actionCount(t); count != 1 {
		t.Fatalf("blocked actions stored rows: %d", count)
	}
	var blocked int
	if err := h.store.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE blocked_by='invalid_hypothesis'`).Scan(&blocked); err != nil {
		t.Fatalf("counting blocked hypothesis calls: %v", err)
	}
	if blocked != 6 {
		rows, _ := h.store.DB().Query(`SELECT tool,blocked_by FROM tool_calls ORDER BY id`)
		var reasons []string
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var tool, reason string
				if rows.Scan(&tool, &reason) == nil {
					reasons = append(reasons, tool+":"+reason)
				}
			}
		}
		t.Fatalf("blocked hypothesis audits = %d, want 6; rows=%v", blocked, reasons)
	}

	// An empty or omitted tag list remains a valid untagged action.
	h.mustCall(t, "post", `{"text":"untagged synthetic post"}`)
	h.mustCall(t, "post", `{"text":"explicitly untagged","hypothesis_ids":[]}`)
	if count := h.actionCount(t); count != 3 {
		t.Fatalf("untagged actions = %d, want 3", count)
	}
}

func TestPlaybookGuardSnapshotsRevisionAtActionReservation(t *testing.T) {
	h := newPlaybookHarness(t)
	entry := h.add(t, "agent", "revision one text")

	first := decodeActionIdentity(t, h.mustCall(t, "post", `{"text":"first tagged post","hypothesis_ids":[`+jsonInt(entry.ID)+`]}`))
	if revision := h.actionHypothesisRevision(t, first, entry.ID); revision != 1 {
		t.Fatalf("first action snapshot revision = %d, want 1", revision)
	}

	amended, err := h.store.ProposePlaybook(context.Background(), "agent", entry.ID, "revision two text", playbookToolsAt.Add(time.Hour))
	if err != nil || amended.Revision != 2 {
		t.Fatalf("amending hypothesis = %+v err=%v", amended, err)
	}
	second := decodeActionIdentity(t, h.mustCall(t, "post", `{"text":"second tagged post","hypothesis_ids":[`+jsonInt(entry.ID)+`]}`))
	if revision := h.actionHypothesisRevision(t, second, entry.ID); revision != 2 {
		t.Fatalf("second action snapshot revision = %d, want 2", revision)
	}
	// The amendment must not rewrite the historical reservation: late metrics
	// for the old text can never be attributed to the new revision.
	if revision := h.actionHypothesisRevision(t, first, entry.ID); revision != 1 {
		t.Fatalf("historical action snapshot revision = %d, want 1", revision)
	}
}

type playbookActionIdentity struct {
	Untrusted bool   `json:"untrusted"`
	Source    string `json:"source"`
	Data      struct {
		ActionID int64 `json:"action_id"`
	} `json:"data"`
}

func decodeActionIdentity(t *testing.T, out string) int64 {
	t.Helper()
	var identity playbookActionIdentity
	if err := json.Unmarshal([]byte(out), &identity); err != nil || !identity.Untrusted || identity.Source != "post" || identity.Data.ActionID <= 0 {
		t.Fatalf("decoding action output %s: %+v err=%v", out, identity, err)
	}
	return identity.Data.ActionID
}

func (h *playbookHarness) actionHypothesisRevision(t *testing.T, actionID, hypothesisID int64) int64 {
	t.Helper()
	var revision int64
	if err := h.store.DB().QueryRow(`SELECT revision FROM action_hypotheses WHERE agent_id='agent' AND action_id=? AND hypothesis_id=?`, actionID, hypothesisID).Scan(&revision); err != nil {
		t.Fatalf("reading action %d hypothesis %d snapshot: %v", actionID, hypothesisID, err)
	}
	return revision
}

func TestPlaybookToolsRefusePausedAndKilledAgents(t *testing.T) {
	for _, status := range []string{"paused", "killed"} {
		t.Run(status, func(t *testing.T) {
			h := newPlaybookHarness(t)
			entry := h.initialize(t, "agent", []string{"seed hypothesis"})[0]
			if _, err := h.store.DB().Exec(`UPDATE agents SET status=? WHERE id='agent'`, status); err != nil {
				t.Fatal(err)
			}
			if out, err := h.call(t, "playbook_read", `{}`); err == nil {
				t.Fatalf("%s read accepted: %s", status, out)
			}
			if out, err := h.call(t, "playbook_propose", `{"text":"new hypothesis"}`); err == nil {
				t.Fatalf("%s proposal accepted: %s", status, out)
			}
			if out, err := h.call(t, "playbook_propose", `{"text":"amended hypothesis","hypothesis_id":`+jsonInt(entry.ID)+`}`); err == nil {
				t.Fatalf("%s amendment accepted: %s", status, out)
			}
			entries := decodePlaybookList(t, mustReadAfterUnpause(t, h, status))
			if len(entries) != 1 || entries[0] != entry {
				t.Fatalf("%s entries changed: %+v", status, entries)
			}
			var blocked int
			if err := h.store.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE blocked_by='agent_not_running'`).Scan(&blocked); err != nil {
				t.Fatal(err)
			}
			if blocked != 3 {
				t.Fatalf("%s blocked audits = %d, want 3", status, blocked)
			}
		})
	}
}

func mustReadAfterUnpause(t *testing.T, h *playbookHarness, status string) string {
	t.Helper()
	if _, err := h.store.DB().Exec(`UPDATE agents SET status='running' WHERE id='agent'`); err != nil {
		t.Fatal(err)
	}
	out, err := h.call(t, "playbook_read", `{}`)
	if err != nil {
		t.Fatalf("reading after %s: %v", status, err)
	}
	return out
}

func TestPlaybookToolRedactsConfiguredSecrets(t *testing.T) {
	const secret = "sk-live-playbook-secret"
	h := newPlaybookHarness(t, func(opts *tools.Options) {
		opts.Secrets = []config.Secret{config.NewSecret(secret)}
	})
	h.initialize(t, "agent", []string{"hypothesis contains " + secret + " inline"})

	out := h.mustCall(t, "playbook_read", `{}`)
	if strings.Contains(out, secret) {
		t.Fatalf("read leaked configured secret: %s", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("read did not mark redaction: %s", out)
	}
	if out, err := h.call(t, "playbook_propose", `{"text":"amended with `+secret+`"}`); err == nil {
		t.Fatalf("proposal containing secret accepted: %s", out)
	}
	if count := playbookToolEntryCount(t, h); count != 1 {
		t.Fatalf("secret proposal inserted entries: %d", count)
	}
	var leaked int
	if err := h.store.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE args_json LIKE '%` + secret + `%' OR result_json LIKE '%` + secret + `%'`).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatalf("secret persisted in audit metadata: %d", leaked)
	}
}

func playbookToolEntryCount(t *testing.T, h *playbookHarness) int {
	t.Helper()
	var count int
	if err := h.store.DB().QueryRow(`SELECT COUNT(*) FROM playbook WHERE agent_id='agent'`).Scan(&count); err != nil {
		t.Fatalf("counting playbook entries: %v", err)
	}
	return count
}

func TestPlaybookToolsSurfacePersistenceAndCancellationFailures(t *testing.T) {
	h := newPlaybookHarness(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, call := range []llm.ToolCall{
		{ID: "c1", Type: "function", Function: llm.CallFunction{Name: "playbook_read", Arguments: `{}`}},
		{ID: "c2", Type: "function", Function: llm.CallFunction{Name: "playbook_propose", Arguments: `{"text":"cancelled proposal"}`}},
	} {
		if _, err := h.dispatcher.Dispatch(cancelled, call); err == nil {
			t.Fatalf("cancelled %s succeeded", call.Function.Name)
		}
	}
	if count := playbookToolEntryCount(t, h); count != 0 {
		t.Fatalf("cancelled proposal inserted %d entries", count)
	}

	closed := newPlaybookHarness(t)
	if err := closed.store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, call := range []llm.ToolCall{
		{ID: "p1", Type: "function", Function: llm.CallFunction{Name: "playbook_read", Arguments: `{}`}},
		{ID: "p2", Type: "function", Function: llm.CallFunction{Name: "playbook_propose", Arguments: `{"text":"closed store proposal"}`}},
	} {
		if out, err := closed.dispatcher.Dispatch(context.Background(), call); err == nil {
			t.Fatalf("closed-store %s succeeded: %s", call.Function.Name, out)
		}
	}
}

func TestEveryPlaybookToolCallPassesTheAuditGuard(t *testing.T) {
	h := newPlaybookHarness(t)
	h.mustCall(t, "playbook_read", `{}`)
	h.mustCall(t, "playbook_propose", `{"text":"audited hypothesis"}`)
	var audits, finished int
	if err := h.store.DB().QueryRow(`SELECT COUNT(*), SUM(CASE WHEN result_json LIKE '%"outcome":"success"%' THEN 1 ELSE 0 END) FROM tool_calls WHERE tool IN ('playbook_read','playbook_propose') AND agent_id='agent'`).Scan(&audits, &finished); err != nil {
		t.Fatal(err)
	}
	if audits != 2 || finished != 2 {
		t.Fatalf("playbook audits=%d finished=%d, want 2/2", audits, finished)
	}
	var unfinished int
	if err := h.store.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE tool IN ('playbook_read','playbook_propose') AND (result_json IS NULL OR result_json='')`).Scan(&unfinished); err != nil {
		t.Fatal(err)
	}
	if unfinished != 0 {
		t.Fatalf("playbook calls bypassed result audit: %d", unfinished)
	}
}

func jsonInt(value int64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
