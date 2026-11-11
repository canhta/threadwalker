package tools_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

func setup(t *testing.T) (*store.Store, tools.Options, *tools.Dispatcher) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "tools.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	_, err = st.DB().Exec(`INSERT INTO agents(id,status) VALUES('agent','running'); INSERT INTO wakes(id,agent_id,started_at) VALUES(1,'agent','2026-11-11T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	opts := tools.Options{Store: st, Template: config.Template{ID: "agent", DryRun: true, Limits: config.LimitsConfig{PostsPerDay: 100, RepliesPerDay: 100, RepliesPerUser7d: 10, MinSecondsBetweenActions: 1, MinMinutesBetweenWakes: 15}}, WakeID: 1, Now: func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }, FetchTrends: func(context.Context) ([]tools.Trend, error) {
		return []tools.Trend{{Source: "synthetic", Title: "untrusted-external-text", URL: "https://example.invalid/synthetic"}}, nil
	}}
	g, err := guard.New(guard.Options{Store: st, Template: opts.Template, WakeID: 1, Now: opts.Now})
	if err != nil {
		t.Fatal(err)
	}
	d, err := tools.NewDispatcher(g, opts)
	if err != nil {
		t.Fatal(err)
	}
	return st, opts, d
}
func call(name, args string) llm.ToolCall {
	return llm.ToolCall{ID: "id", Type: "function", Function: llm.CallFunction{Name: name, Arguments: args}}
}
func dispatch(t *testing.T, d *tools.Dispatcher, name, args string) string {
	t.Helper()
	out, err := d.Dispatch(context.Background(), call(name, args))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}
func TestDefinitionsAndConstructor(t *testing.T) {
	st, opts, d := setup(t)
	defs := d.Definitions()
	if len(defs) != 13 {
		t.Fatalf("definitions %d", len(defs))
	}
	names := map[string]bool{}
	for _, def := range defs {
		if def.Type != "function" || names[def.Function.Name] || !json.Valid(def.Function.Parameters) {
			t.Fatalf("definition %+v", def)
		}
		names[def.Function.Name] = true
	}
	defs[0].Function.Name = "tampered"
	defs[1].Function.Parameters[0] = '!'
	for _, def := range d.Definitions() {
		if def.Function.Name == "tampered" || !json.Valid(def.Function.Parameters) {
			t.Fatal("mutable registry")
		}
	}
	if _, err := tools.NewDispatcher(nil, opts); err == nil {
		t.Fatal("nil guard")
	}
	g, _ := guard.New(guard.Options{Store: st, Template: opts.Template, WakeID: 1})
	for _, mutate := range []func(*tools.Options){func(o *tools.Options) { o.Store = nil }, func(o *tools.Options) { o.Template.ID = "other" }, func(o *tools.Options) { o.WakeID = 2 }, func(o *tools.Options) { o.Template.DryRun = false }, func(o *tools.Options) { o.Template.Limits.MinMinutesBetweenWakes = 0 }, func(o *tools.Options) { o.Template.Limits.MinMinutesBetweenWakes = 1441 }} {
		bad := opts
		mutate(&bad)
		if _, err := tools.NewDispatcher(g, bad); err == nil {
			t.Fatal("invalid options")
		}
	}
	opts.Now = nil
	opts.FetchTrends = nil
	if _, err := tools.NewDispatcher(g, opts); err != nil {
		t.Fatal(err)
	}
}
func TestNotesAndSchedule(t *testing.T) {
	st, _, d := setup(t)
	dispatch(t, d, "note_write", `{"text":"remember deterministic robots"}`)
	var note string
	st.DB().QueryRow(`SELECT self_note FROM agents`).Scan(&note)
	if note != "remember deterministic robots" {
		t.Fatal(note)
	}
	out := dispatch(t, d, "note_search", `{"query":"robots"}`)
	if !strings.Contains(out, "remember deterministic robots") {
		t.Fatal(out)
	}
	for _, tc := range []struct{ at, want string }{{"2020-01-01T00:00:00Z", "2026-11-11T00:15:00Z"}, {"2027-01-01T00:00:00Z", "2026-11-12T00:00:00Z"}, {"2026-11-11T04:00:00+02:00", "2026-11-11T02:00:00Z"}} {
		dispatch(t, d, "schedule_next_wake", `{"at":"`+tc.at+`"}`)
		var got string
		st.DB().QueryRow(`SELECT next_wake_at FROM agents`).Scan(&got)
		if got != tc.want {
			t.Fatalf("got %s want %s", got, tc.want)
		}
	}
}
func TestExternalWrappersAndDryRunActions(t *testing.T) {
	st, opts, _ := setup(t)
	now := opts.Now()
	opts.Now = func() time.Time { now = now.Add(time.Second); return now }
	g, _ := guard.New(guard.Options{Store: st, Template: opts.Template, WakeID: 1, Now: opts.Now})
	d, _ := tools.NewDispatcher(g, opts)
	for _, tc := range []struct{ name, args string }{{"fetch_trends", "{}"}, {"read_feed", "{}"}, {"search", `{"query":"golang"}`}, {"read_post", `{"url":"https://example.invalid/thread"}`}, {"read_own_metrics", "{}"}, {"post", `{"text":"synthetic own post"}`}, {"reply", `{"text":"synthetic own reply","target_url":"https://example.invalid/thread","target_username":"synthetic_user"}`}} {
		out := dispatch(t, d, tc.name, tc.args)
		var envelope struct {
			Untrusted bool            `json:"untrusted"`
			Source    string          `json:"source"`
			Data      json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &envelope); err != nil || !envelope.Untrusted || envelope.Source != tc.name || len(envelope.Data) == 0 {
			t.Fatalf("wrapper %s %s %v", tc.name, out, err)
		}
		if tc.name != "fetch_trends" && !strings.Contains(string(envelope.Data), "synthetic") {
			t.Fatalf("unlabelled fixture %s", out)
		}
	}
	var count int
	st.DB().QueryRow(`SELECT COUNT(*) FROM actions WHERE dry_run=1 AND agent_id='agent'`).Scan(&count)
	if count != 2 {
		t.Fatal(count)
	}
	st.DB().QueryRow(`SELECT COUNT(*) FROM contacts`).Scan(&count)
	if count != 1 {
		t.Fatal("reply rate counter missing")
	}
	var leaked int
	st.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE args_json LIKE '%synthetic_user%' OR result_json LIKE '%untrusted-external-text%' OR result_json LIKE '%example.invalid%'`).Scan(&leaked)
	if leaked != 0 {
		t.Fatal("external body persisted in audit")
	}
}
func TestStallCanonicalAttemptsAndLatch(t *testing.T) {
	st, opts, _ := setup(t)
	now := opts.Now()
	opts.Now = func() time.Time { now = now.Add(time.Second); return now }
	g, _ := guard.New(guard.Options{Store: st, Template: opts.Template, WakeID: 1, Now: opts.Now})
	d, _ := tools.NewDispatcher(g, opts)
	for _, args := range []string{`{"text":"same","hypothesis_ids":[]}`, `{ "hypothesis_ids": [], "text": "same" }`} {
		dispatch(t, d, "post", args)
	}
	_, err := d.Dispatch(context.Background(), call("post", `{"text":"same","hypothesis_ids":[]}`))
	if !errors.Is(err, tools.ErrStallStop) {
		t.Fatal(err)
	}
	_, err = d.Dispatch(context.Background(), call("post", `{"text":"new"}`))
	if !errors.Is(err, tools.ErrStallStop) {
		t.Fatal("stopped dispatcher accepted more effects")
	}
	var actions, blocked int
	st.DB().QueryRow(`SELECT COUNT(*) FROM actions`).Scan(&actions)
	st.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE blocked_by='stall_stop'`).Scan(&blocked)
	if actions != 2 || blocked != 2 {
		t.Fatalf("actions %d blocked %d", actions, blocked)
	}
}
func TestInvalidToolsAreAuditedAndCannotMutate(t *testing.T) {
	for _, tc := range []struct{ name, args string }{{"unknown", "{}"}, {"post", "{"}, {"post", "[]"}, {"post", "null"}, {"post", `{"text":""}`}, {"post", `{"text":null}`}, {"post", `{"text":"x","extra":true}`}, {"post", `{"text":"x"} {}`}, {"reply", `{"text":"x"}`}, {"search", `{"query":""}`}, {"read_post", `{"url":""}`}, {"note_write", `{"text":""}`}, {"note_search", `{"query":"x","limit":0}`}, {"note_search", `{"query":"x","limit":101}`}, {"note_search", `{"query":"\""}`}, {"schedule_next_wake", `{"at":"tomorrow"}`}, {"read_feed", `{"query":"x"}`}, {"post", `{"text":"x","hypothesis_ids":[1]}`}} {
		t.Run(tc.name+tc.args, func(t *testing.T) {
			st, _, d := setup(t)
			_, err := d.Dispatch(context.Background(), call(tc.name, tc.args))
			if err == nil {
				t.Fatal("accepted invalid call")
			}
			var count, actions int
			st.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls`).Scan(&count)
			st.DB().QueryRow(`SELECT COUNT(*) FROM actions`).Scan(&actions)
			if count != 1 || actions != 0 {
				t.Fatalf("audit %d actions %d", count, actions)
			}
		})
	}
	st, _, d := setup(t)
	c := call("post", `{"text":"no"}`)
	c.Type = "bad"
	_, err := d.Dispatch(context.Background(), c)
	if err == nil {
		t.Fatal("invalid type")
	}
	var blocked string
	st.DB().QueryRow(`SELECT blocked_by FROM tool_calls`).Scan(&blocked)
	if blocked != "unsupported_tool_type" {
		t.Fatal(blocked)
	}
}
func TestAuditFailureAndPausedStatePreventExternalEffects(t *testing.T) {
	for _, mode := range []string{"audit", "paused", "completion"} {
		t.Run(mode, func(t *testing.T) {
			st, opts, _ := setup(t)
			calls := 0
			opts.FetchTrends = func(context.Context) ([]tools.Trend, error) { calls++; return nil, nil }
			g, _ := guard.New(guard.Options{Store: st, Template: opts.Template, WakeID: 1, Now: opts.Now})
			d, _ := tools.NewDispatcher(g, opts)
			switch mode {
			case "audit":
				st.DB().Exec(`CREATE TRIGGER fail_audit BEFORE INSERT ON tool_calls BEGIN SELECT RAISE(FAIL,'private-error'); END`)
			case "completion":
				st.DB().Exec(`CREATE TRIGGER fail_audit BEFORE UPDATE ON tool_calls BEGIN SELECT RAISE(FAIL,'private-error'); END`)
			case "paused":
				st.DB().Exec(`UPDATE agents SET status='paused'`)
			}
			_, err := d.Dispatch(context.Background(), call("fetch_trends", "{}"))
			if err == nil || calls != map[bool]int{true: 1, false: 0}[mode == "completion"] {
				t.Fatalf("calls %d err %v", calls, err)
			}
			if mode != "paused" {
				_, err = d.Dispatch(context.Background(), call("post", `{"text":"no effect after failure"}`))
				if err == nil {
					t.Fatal("failed audit dispatcher continued")
				}
				var actions int
				st.DB().QueryRow(`SELECT COUNT(*) FROM actions`).Scan(&actions)
				if actions != 0 {
					t.Fatal("effects after audit failure")
				}
			}
		})
	}
}
func TestGuardClientAuditsRejectedAndStopsThirdUnknownAttempt(t *testing.T) {
	for _, tc := range []struct{ name, args, kind string }{{"unknown", "{}", "function"}, {"post", "{", "function"}, {"post", "{}", "bad"}, {"note_write", `{"text":"same"}`, "function"}} {
		t.Run(tc.name+tc.args+tc.kind, func(t *testing.T) {
			st, _, d := setup(t)
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "id", Type: tc.kind, Function: llm.CallFunction{Name: tc.name, Arguments: tc.args}}}}, "finish_reason": "tool_calls"}}, "usage": map[string]int{"prompt_tokens": 0, "completion_tokens": 0}})
			}))
			defer server.Close()
			client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "synthetic", APIKey: config.NewSecret("dummy")}, config.LimitsConfig{MaxStepsPerWake: 10, MaxUSDPerDay: 1}, st, d.ConfigureClient(llm.Options{}))
			if err != nil {
				t.Fatal(err)
			}
			res, err := client.Run(context.Background(), "agent", 1, nil, d.Definitions(), d)
			if !errors.Is(err, tools.ErrStallStop) || requests != 3 || res.Steps != 3 {
				t.Fatalf("requests %d steps %d err %v", requests, res.Steps, err)
			}
			var count, blocked int
			st.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls`).Scan(&count)
			st.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE blocked_by='stall_stop'`).Scan(&blocked)
			if count != 3 || blocked != 1 {
				t.Fatalf("audits %d stall %d", count, blocked)
			}
		})
	}
}
func TestToolsLogsContainMetadataOnly(t *testing.T) {
	st, opts, _ := setup(t)
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	opts.Logger = logger
	g, _ := guard.New(guard.Options{Store: st, Template: opts.Template, WakeID: 1, Now: opts.Now, Logger: logger})
	d, _ := tools.NewDispatcher(g, opts)
	dispatch(t, d, "note_write", `{"text":"private-note-canary"}`)
	dispatch(t, d, "fetch_trends", "{}")
	d.Dispatch(context.Background(), call("unknown-secret-canary", "{}"))
	dispatch(t, d, "reply", `{"text":"own-post-canary","target_url":"https://private-url-canary.invalid/x","target_username":"stranger_canary"}`)
	for _, canary := range []string{"private-note-canary", "untrusted-external-text", "unknown-secret-canary", "own-post-canary", "private-url-canary", "stranger_canary"} {
		if strings.Contains(buf.String(), canary) {
			t.Fatalf("log leaked %s", canary)
		}
	}
	if strings.Contains(buf.String(), `"tool":"unknown"`) {
		t.Fatal("unknown tool must be omitted from operational metadata")
	}
	for _, event := range []string{"tool.started", "tool.finished", "guard.checked", "audit.recorded", "tool.blocked", "note.written", "trends.fetched"} {
		if !strings.Contains(buf.String(), event) {
			t.Errorf("missing %s", event)
		}
	}
}

func TestBuiltinFailuresAreAuditedWithoutResultsOrEffects(t *testing.T) {
	for _, mode := range []string{"trends", "note-write", "note-search", "schedule", "action", "hypothesis"} {
		t.Run(mode, func(t *testing.T) {
			st, opts, _ := setup(t)
			name, args := "fetch_trends", "{}"
			switch mode {
			case "trends":
				opts.FetchTrends = func(context.Context) ([]tools.Trend, error) { return nil, errors.New("private-source-error") }
			case "note-write":
				name, args = "note_write", `{"text":"note"}`
				st.DB().Exec(`CREATE TRIGGER fail_note BEFORE INSERT ON notes BEGIN SELECT RAISE(FAIL,'private-store-error'); END`)
			case "note-search":
				name, args = "note_search", `{"query":"note","limit":2}`
				st.DB().Exec(`DROP TABLE notes_fts`)
			case "schedule":
				name, args = "schedule_next_wake", `{"at":"2026-11-11T01:00:00Z"}`
				st.DB().Exec(`CREATE TRIGGER fail_schedule BEFORE UPDATE ON agents BEGIN SELECT RAISE(FAIL,'private-store-error'); END`)
			case "action":
				name, args = "post", `{"text":"post"}`
				st.DB().Exec(`CREATE TRIGGER fail_action BEFORE INSERT ON actions BEGIN SELECT RAISE(FAIL,'private-store-error'); END`)
			case "hypothesis":
				name, args = "post", `{"text":"post","hypothesis_ids":[1]}`
				st.DB().Exec(`DROP TABLE playbook`)
			}
			g, _ := guard.New(guard.Options{Store: st, Template: opts.Template, WakeID: 1, Now: opts.Now})
			d, _ := tools.NewDispatcher(g, opts)
			_, err := d.Dispatch(context.Background(), call(name, args))
			if err == nil {
				t.Fatal("accepted failure")
			}
			if mode == "trends" && strings.Contains(err.Error(), "private-source") {
				t.Fatal("source diagnostics leaked")
			}
			if _, ok := d.NextWake(); ok {
				t.Fatal("failed schedule reported success")
			}
			var actions, count int
			st.DB().QueryRow(`SELECT COUNT(*) FROM actions`).Scan(&actions)
			if mode == "action" || mode == "hypothesis" {
				st.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE result_json=''`).Scan(&count)
			} else {
				st.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE result_json='{"outcome":"error","bytes":0}'`).Scan(&count)
			}
			if actions != 0 || count != 1 {
				t.Fatalf("actions %d audits %d", actions, count)
			}
		})
	}
}
func TestHypothesisTagsRequireOwnTestingEntries(t *testing.T) {
	st, _, d := setup(t)
	_, err := st.DB().Exec(`INSERT INTO playbook(id,agent_id,text,status,created_at) VALUES(1,'agent','own hypothesis','testing','2026-11-11'),(2,'other','foreign hypothesis','testing','2026-11-11'),(3,'agent','established hypothesis','established','2026-11-11'),(4,'agent','rejected hypothesis','rejected','2026-11-11')`)
	if err != nil {
		t.Fatal(err)
	}
	dispatch(t, d, "post", `{"text":"own experiment","hypothesis_ids":[1]}`)
	for _, args := range []string{`{"text":"foreign","hypothesis_ids":[2]}`, `{"text":"established","hypothesis_ids":[3]}`, `{"text":"rejected","hypothesis_ids":[4]}`, `{"text":"unknown","hypothesis_ids":[99]}`} {
		if _, err := d.Dispatch(context.Background(), call("post", args)); err == nil {
			t.Fatal("accepted unavailable hypothesis")
		}
	}
	var count int
	var ids string
	st.DB().QueryRow(`SELECT COUNT(*),hypothesis_ids_json FROM actions`).Scan(&count, &ids)
	if count != 1 || ids != "[1]" {
		t.Fatalf("actions %d ids %s", count, ids)
	}
}
func TestDispatcherKeepsConfiguredSecretsOutOfEffectsAndAudit(t *testing.T) {
	st, opts, _ := setup(t)
	opts.Template.LLM.APIKey = config.NewSecret("private-key-canary")
	g, _ := guard.New(guard.Options{Store: st, Template: opts.Template, WakeID: 1, Now: opts.Now})
	d, _ := tools.NewDispatcher(g, opts)
	for _, args := range []string{`{"text":"private-key-canary"}`, `{"text":"private-key-can\u0061ry"}`} {
		if _, err := d.Dispatch(context.Background(), call("post", args)); err == nil {
			t.Fatal("accepted secret in action")
		}
	}
	var actions, leaked int
	st.DB().QueryRow(`SELECT COUNT(*) FROM actions`).Scan(&actions)
	st.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE args_json LIKE '%private-key%' OR result_json LIKE '%private-key%'`).Scan(&leaked)
	if actions != 0 || leaked != 0 {
		t.Fatal("secret persisted")
	}
}
func TestScheduleSignalAndLocalValidationBeforeEffects(t *testing.T) {
	_, _, d := setup(t)
	if _, ok := d.NextWake(); ok {
		t.Fatal("unscheduled dispatcher")
	}
	dispatch(t, d, "schedule_next_wake", `{"at":"2026-11-11T01:00:00Z"}`)
	at, ok := d.NextWake()
	if !ok || at.Hour() != 1 {
		t.Fatal("missing schedule")
	}
	for _, tc := range []struct{ name, args string }{{"note_write", `{"text":1}`}, {"note_search", `{"query":"x","limit":"1"}`}, {"note_search", `{"query":"x","limit":null}`}, {"post", `{"text":"x","hypothesis_ids":null}`}, {"post", `{"text":"x","hypothesis_ids":[0]}`}, {"post", `{"text":"x","hypothesis_ids":["1"]}`}} {
		if _, err := d.Dispatch(context.Background(), call(tc.name, tc.args)); err == nil {
			t.Fatal("accepted invalid typed input")
		}
	}
	c := call("note_write", `{"text":"x"}`)
	c.ID = ""
	if _, err := d.Dispatch(context.Background(), c); err == nil {
		t.Fatal("accepted missing id")
	}
}

func TestClientStopsImmediatelyOnAuditFailure(t *testing.T) {
	for _, name := range []string{"post", "unknown"} {
		t.Run(name, func(t *testing.T) {
			st, _, d := setup(t)
			_, err := st.DB().Exec(`CREATE TRIGGER fail_audit BEFORE INSERT ON tool_calls BEGIN SELECT RAISE(FAIL,'private-audit-error'); END`)
			if err != nil {
				t.Fatal(err)
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "one", Type: "function", Function: llm.CallFunction{Name: name, Arguments: `{"text":"must not execute"}`}}, {ID: "two", Type: "function", Function: llm.CallFunction{Name: "post", Arguments: `{"text":"must not execute either"}`}}}}, "finish_reason": "tool_calls"}}, "usage": map[string]int{"prompt_tokens": 0, "completion_tokens": 0}})
			}))
			defer server.Close()
			client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "synthetic", APIKey: config.NewSecret("dummy")}, config.LimitsConfig{MaxStepsPerWake: 10, MaxUSDPerDay: 1}, st, d.ConfigureClient(llm.Options{}))
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Run(context.Background(), "agent", 1, nil, d.Definitions(), d)
			if !errors.Is(err, guard.ErrAudit) || strings.Contains(err.Error(), "private") || requests != 1 || result.Steps != 1 {
				t.Fatalf("requests %d steps %d err %v", requests, result.Steps, err)
			}
			var actions int
			st.DB().QueryRow(`SELECT COUNT(*) FROM actions`).Scan(&actions)
			if actions != 0 {
				t.Fatal("side effect after audit failure")
			}
		})
	}
}
