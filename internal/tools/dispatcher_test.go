package tools_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/quality"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/tools"
	"github.com/canhta/threadz/internal/trends"
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
	opts := tools.Options{Scope: guard.Scope{Store: st, Template: config.Template{ID: "agent", DryRun: true, Limits: config.LimitsConfig{PostsPerDay: 100, RepliesPerDay: 100, RepliesPerUser7d: 10, MinSecondsBetweenActions: 1, MinMinutesBetweenWakes: 15}}, WakeID: 1, Now: func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }}, Threads: threads.NewMemory(), FetchTrends: func(context.Context) ([]trends.Trend, error) {
		return []trends.Trend{{Source: "synthetic", Title: "untrusted-external-text", URL: "https://example.invalid/synthetic"}}, nil
	}}
	opts.Scope = guard.Scope{Store: st, Template: opts.Template, WakeID: 1, Now: opts.Now}
	d, err := tools.NewDispatcher(opts)
	if err != nil {
		t.Fatal(err)
	}
	return st, opts, d
}

// readRef has the guard read the post at url (an API post id, empty for a post
// only a browser read showed) and returns its ref.
func readRef(t *testing.T, g *guard.Guard, url, id string) string {
	t.Helper()
	ref := g.NotePost(context.Background(), threads.Post{ID: id, URL: url})
	if ref == "" {
		t.Fatalf("post %q has no ref", url)
	}
	return ref
}

func call(name, args string) llm.ToolCall {
	return llm.ToolCall{ID: "id", Type: "function", Function: llm.CallFunction{Name: name, Arguments: args}}
}
func dispatch(t *testing.T, d *tools.Dispatcher, name, args string) string {
	t.Helper()
	run := d.Dispatch
	if name == "read_own_metrics" {
		run = d.DispatchHarness
	}
	out, err := run(context.Background(), call(name, args))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}
func TestDefinitionsAndConstructor(t *testing.T) {
	_, opts, d := setup(t)
	defs := d.Definitions()
	if len(defs) != 8 {
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
	for _, mutate := range []func(*tools.Options){
		func(o *tools.Options) { o.Store = nil },
		func(o *tools.Options) { o.Template.ID = "" },
		func(o *tools.Options) { o.WakeID = 0 },
		func(o *tools.Options) { o.Template.DryRun = false },
		func(o *tools.Options) { o.Template.Limits.MinMinutesBetweenWakes = 0 },
		func(o *tools.Options) { o.Template.Limits.MinMinutesBetweenWakes = 1441 },
	} {
		bad := opts
		mutate(&bad)
		if _, err := tools.NewDispatcher(bad); err == nil {
			t.Fatal("invalid options")
		}
	}
	opts.Now = nil
	if _, err := tools.NewDispatcher(opts); err != nil {
		t.Fatal(err)
	}
}

func TestNewDispatcherRequiresInjectedFetchers(t *testing.T) {
	_, opts, _ := setup(t)
	for name, mutate := range map[string]func(*tools.Options){
		"trends":     func(o *tools.Options) { o.FetchTrends = nil },
		"web_search": func(o *tools.Options) { o.Template.WebSearch.Enabled = true },
	} {
		t.Run(name, func(t *testing.T) {
			bad := opts
			mutate(&bad)
			if _, err := tools.NewDispatcher(bad); err == nil {
				t.Fatal("missing fetcher accepted")
			}
		})
	}
}
func TestExternalWrappersAndDryRunActions(t *testing.T) {
	st, opts, _ := setup(t)
	now := opts.Now()
	opts.Now = func() time.Time { now = now.Add(time.Second); return now }
	opts.Scope = guard.Scope{Store: st, Template: opts.Template, WakeID: 1, Now: opts.Now}
	d, _ := tools.NewDispatcher(opts)
	target := readRef(t, d.Guard(), "https://www.threads.com/@synthetic_author/post/thread", "id-thread")
	for _, tc := range []struct{ name, args string }{{"fetch_trends", "{}"}, {"find_posts", `{"source":"feed"}`}, {"find_posts", `{"source":"search","query":"golang"}`}, {"read_post", `{"id":"fixture-3"}`}, {"read_own_metrics", "{}"}, {"post", `{"text":"synthetic own post"}`}, {"reply", `{"text":"synthetic own reply","target":"` + target + `"}`}} {
		out := dispatch(t, d, tc.name, tc.args)
		var envelope struct {
			Untrusted bool            `json:"untrusted"`
			Source    string          `json:"source"`
			Data      json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &envelope); err != nil || !envelope.Untrusted || envelope.Source != tc.name || len(envelope.Data) == 0 {
			t.Fatalf("wrapper %s %s %v", tc.name, out, err)
		}
		if tc.name != "fetch_trends" && tc.name != "read_own_metrics" && !strings.Contains(string(envelope.Data), "synthetic") {
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

// repeatDispatcher serves search from its Memory backend; the repeat tests use
// it because the read tools capped per wake would end the wake on their own.
func repeatDispatcher(t *testing.T) (*store.Store, *tools.Dispatcher, *threads.Memory) {
	st, opts, _ := setup(t)
	mem := opts.Threads.(*threads.Memory)
	opts.Scope = guard.Scope{Store: st, Template: opts.Template, WakeID: 1, Now: opts.Now}
	d, _ := tools.NewDispatcher(opts)
	return st, d, mem
}
func TestRepeatGuard(t *testing.T) {
	blockedCount := func(st *store.Store, reason string) (n int) {
		st.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE blocked_by=?`, reason).Scan(&n)
		return
	}
	t.Run("changing results never stop", func(t *testing.T) {
		_, d, mem := repeatDispatcher(t)
		for i := 0; i < 25; i++ {
			mem.Pages[threads.OpSearch] = threads.Page{Kind: "search", Posts: []threads.Post{{ID: fmt.Sprint("id", i), URL: fmt.Sprint("https://www.threads.com/@a/post/p", i), Username: "a", Text: fmt.Sprint("t", i)}}}
			if _, err := d.Dispatch(context.Background(), call("find_posts", `{"source":"search","query":"q"}`)); err != nil {
				t.Fatalf("call %d: %v", i, err)
			}
		}
	})
	t.Run("three identical failures refuse without ending", func(t *testing.T) {
		st, d, mem := repeatDispatcher(t)
		mem.Errors[threads.OpSearch] = errors.New("source down")
		for i := 0; i < 3; i++ {
			if _, err := d.Dispatch(context.Background(), call("find_posts", `{"source":"search","query":"q"}`)); err == nil || errors.Is(err, tools.ErrStallStop) {
				t.Fatalf("failure %d: %v", i, err)
			}
		}
		_, err := d.Dispatch(context.Background(), call("find_posts", ` {"source":"search","query":"q"} `))
		var b *guard.BlockedError
		if !errors.As(err, &b) || b.Reason != "repeat_refused" || errors.Is(err, tools.ErrStallStop) {
			t.Fatalf("want one refusal, got %v", err)
		}
		if _, err := d.Dispatch(context.Background(), call("find_posts", `{"source":"feed"}`)); err != nil {
			t.Fatalf("other call refused: %v", err)
		}
		if n := blockedCount(st, "repeat_refused"); n != 1 {
			t.Fatal(n)
		}
	})
	t.Run("two refusals end the wake", func(t *testing.T) {
		st, d, mem := repeatDispatcher(t)
		mem.Errors[threads.OpSearch] = errors.New("source down")
		for i := 0; i < 5; i++ {
			d.Dispatch(context.Background(), call("find_posts", `{"source":"search","query":"q"}`))
		}
		_, err := d.Dispatch(context.Background(), call("find_posts", `{"source":"feed"}`))
		if !errors.Is(err, tools.ErrStallStop) {
			t.Fatal("stopped dispatcher accepted more calls:", err)
		}
		if n := blockedCount(st, "repeat_refused"); n != 2 {
			t.Fatal(n)
		}
	})
	t.Run("ten identical successes end the wake", func(t *testing.T) {
		_, d, _ := repeatDispatcher(t)
		for i := 1; i <= 10; i++ {
			_, err := d.Dispatch(context.Background(), call("find_posts", `{"source":"search","query":"q"}`))
			if (i == 10) != errors.Is(err, tools.ErrStallStop) || (i < 10 && err != nil) {
				t.Fatalf("call %d: %v", i, err)
			}
		}
	})
}
func TestInvalidToolsAreAuditedAndCannotMutate(t *testing.T) {
	for _, tc := range []struct{ name, args string }{{"unknown", "{}"}, {"post", "{"}, {"post", "[]"}, {"post", "null"}, {"post", `{"text":""}`}, {"post", `{"text":null}`}, {"post", `{"text":"x","extra":true}`}, {"post", `{"text":"x"} {}`}, {"reply", `{"text":"x"}`}, {"find_posts", `{"source":"search","query":""}`}, {"read_post", `{"id":""}`}, {"note_write", `{"text":"x"}`}, {"playbook_read", `{}`}, {"read_own_metrics", `{}`}, {"read_feed", `{"query":"x"}`}, {"post", `{"text":"x","hypothesis_ids":[1]}`}, {"post", `{"text":"x","explore":true}`}} {
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
			opts.FetchTrends = func(context.Context) ([]trends.Trend, error) { calls++; return nil, nil }
			opts.Scope = guard.Scope{Store: st, Template: opts.Template, WakeID: 1, Now: opts.Now}
			d, _ := tools.NewDispatcher(opts)
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
func TestGuardClientAuditsRejectedAndRefusesRepeatedFailures(t *testing.T) {
	for _, tc := range []struct{ name, args, kind string }{{"unknown", "{}", "function"}, {"post", "{", "function"}, {"post", "{}", "bad"}, {"read_post", `{"id":""}`, "function"}} {
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
			if !errors.Is(err, tools.ErrStallStop) || requests != 5 || res.Steps != 5 {
				t.Fatalf("requests %d steps %d err %v", requests, res.Steps, err)
			}
			var count, blocked int
			st.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls`).Scan(&count)
			st.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE blocked_by='repeat_refused'`).Scan(&blocked)
			if count != 5 || blocked != 2 {
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
	opts.Scope = guard.Scope{Store: st, Template: opts.Template, WakeID: 1, Now: opts.Now, Logger: logger}
	d, _ := tools.NewDispatcher(opts)
	dispatch(t, d, "fetch_trends", "{}")
	d.Dispatch(context.Background(), call("unknown-secret-canary", "{}"))
	dispatch(t, d, "reply", `{"text":"own-post-canary","target":"`+readRef(t, d.Guard(), "https://threads.com/@stranger_canary/post/private-url-canary", "tid")+`"}`)
	for _, canary := range []string{"untrusted-external-text", "unknown-secret-canary", "own-post-canary", "private-url-canary", "stranger_canary"} {
		if strings.Contains(buf.String(), canary) {
			t.Fatalf("log leaked %s", canary)
		}
	}
	if strings.Contains(buf.String(), `"tool":"unknown"`) {
		t.Fatal("unknown tool must be omitted from operational metadata")
	}
	for _, event := range []string{"tool.started", "tool.finished", "guard.checked", "audit.recorded", "tool.blocked", "trends.fetched"} {
		if !strings.Contains(buf.String(), event) {
			t.Errorf("missing %s", event)
		}
	}
}

func TestBuiltinFailuresAreAuditedWithoutResultsOrEffects(t *testing.T) {
	for _, mode := range []string{"trends", "action"} {
		t.Run(mode, func(t *testing.T) {
			st, opts, _ := setup(t)
			name, args := "fetch_trends", "{}"
			switch mode {
			case "trends":
				opts.FetchTrends = func(context.Context) ([]trends.Trend, error) { return nil, errors.New("private-source-error") }
			case "action":
				name, args = "post", `{"text":"post"}`
				st.DB().Exec(`CREATE TRIGGER fail_action BEFORE INSERT ON actions BEGIN SELECT RAISE(FAIL,'private-store-error'); END`)
			}
			opts.Scope = guard.Scope{Store: st, Template: opts.Template, WakeID: 1, Now: opts.Now}
			d, _ := tools.NewDispatcher(opts)
			_, err := d.Dispatch(context.Background(), call(name, args))
			if err == nil {
				t.Fatal("accepted failure")
			}
			if mode == "trends" && strings.Contains(err.Error(), "private-source") {
				t.Fatal("source diagnostics leaked")
			}
			var actions, count int
			st.DB().QueryRow(`SELECT COUNT(*) FROM actions`).Scan(&actions)
			if mode == "action" {
				st.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE result_json=''`).Scan(&count)
			} else {
				st.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE result_json='{"outcome":"error","bytes":0,"reason":"operation_failed"}'`).Scan(&count)
			}
			if actions != 0 || count != 1 {
				t.Fatalf("actions %d audits %d", actions, count)
			}
		})
	}
}
func TestDispatcherKeepsConfiguredSecretsOutOfEffectsAndAudit(t *testing.T) {
	st, opts, _ := setup(t)
	opts.Template.LLM.APIKey = config.NewSecret("private-key-canary")
	opts.Scope = guard.Scope{Store: st, Template: opts.Template, WakeID: 1, Now: opts.Now}
	d, _ := tools.NewDispatcher(opts)
	for _, args := range []string{`{"text":"private-key-canary"}`, `{"text":"private-key-can\u0061ry"}`} {
		if _, err := d.Dispatch(context.Background(), call("post", args)); err == nil {
			t.Fatal("accepted secret in action")
		}
	}
	if _, err := d.Dispatch(context.Background(), call("mark_do_not_contact", `{"username":"private-key-canary"}`)); err == nil {
		t.Fatal("accepted secret in mark_do_not_contact")
	}
	var actions, leaked int
	st.DB().QueryRow(`SELECT COUNT(*) FROM actions`).Scan(&actions)
	st.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE args_json LIKE '%private-key%' OR result_json LIKE '%private-key%'`).Scan(&leaked)
	if actions != 0 || leaked != 0 {
		t.Fatal("secret persisted")
	}
}
func TestLocalValidationBeforeEffects(t *testing.T) {
	_, _, d := setup(t)
	for _, tc := range []struct{ name, args string }{{"post", `{"text":1}`}, {"post", `{"text":"x","parts":[""]}`}, {"find_posts", `{"source":"search","query":"x","recent":"1"}`}, {"reply", `{"text":"x","answers_note":0}`}, {"reply", `{"text":"x","answers_note":"1"}`}} {
		if _, err := d.Dispatch(context.Background(), call(tc.name, tc.args)); err == nil {
			t.Fatal("accepted invalid typed input")
		}
	}
	c := call("fetch_trends", `{}`)
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
func TestDispatcherRejectsConfiguredSecretsInToolArguments(t *testing.T) {
	st, opts, _ := setup(t)
	opts.Template.LLM.APIKey = config.NewSecret("llm-key-canary")
	opts.Template.Telegram.BotToken = config.NewSecret("bot-token-canary")
	opts.Scope = guard.Scope{Store: st, Template: opts.Template, WakeID: 1, Now: opts.Now}
	d, _ := tools.NewDispatcher(opts)
	for _, canary := range []string{"llm-key-canary", "bot-token-canary"} {
		if _, err := d.Dispatch(context.Background(), call("post", `{"text":"`+canary+`"}`)); err == nil {
			t.Fatalf("accepted %s in action", canary)
		}
	}
}

func TestReplyTargetMustComeFromAReadResult(t *testing.T) {
	st, opts, _ := setup(t)
	now := opts.Now()
	opts.Now = func() time.Time { now = now.Add(time.Minute); return now }
	opts.Scope = guard.Scope{Store: st, Template: opts.Template, WakeID: 1, Now: opts.Now}
	d, _ := tools.NewDispatcher(opts)
	reply := `{"text":"synthetic reply","target":"p1"}`
	if _, err := d.Dispatch(context.Background(), call("reply", reply)); !errors.Is(err, guard.ErrBlocked) {
		t.Fatalf("reply before any read: %v", err)
	}
	dispatch(t, d, "find_posts", `{"source":"search","query":"golang"}`)
	dispatch(t, d, "reply", reply)
}

func TestReconcileUnpublishedFailsOnlyProvenUnpublished(t *testing.T) {
	st, opts, d := setup(t)
	mem := opts.Threads.(*threads.Memory)
	mem.Pages[threads.OpReadOwnPosts] = threads.Page{Kind: "own_posts", Posts: []threads.Post{{Text: "went out"}}}
	for _, text := range []string{"went out", "never sent"} {
		if _, err := st.DB().Exec(`INSERT INTO actions(agent_id,kind,text,dry_run,posted_at) VALUES('agent','post',?,0,'2026-11-10T00:00:00Z')`, text); err != nil {
			t.Fatal(err)
		}
	}
	d.ReconcileUnpublished(context.Background())
	pending, err := st.UnpublishedPosts(context.Background(), "agent")
	if err != nil || len(pending) != 1 || pending[0].Text != "went out" {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
	// A failed listing proves nothing.
	mem.Errors[threads.OpReadOwnPosts] = errors.New("down")
	d.ReconcileUnpublished(context.Background())
	if pending, _ = st.UnpublishedPosts(context.Background(), "agent"); len(pending) != 1 {
		t.Fatalf("pending=%v", pending)
	}
}

func TestIdeasToolSeesWhatWasRead(t *testing.T) {
	_, opts, _ := setup(t)
	var seen []quality.Item
	opts.Ideas = func(_ context.Context, items []quality.Item) ([]quality.Idea, error) {
		seen = items
		return []quality.Idea{}, nil
	}
	d, err := tools.NewDispatcher(opts)
	if err != nil {
		t.Fatal(err)
	}
	dispatch(t, d, "fetch_trends", "{}")
	if out := dispatch(t, d, "ideas", "{}"); !strings.Contains(out, "[]") {
		t.Fatalf("empty ideas = %s", out)
	}
	if len(seen) == 0 || seen[0].Ref != "t1" {
		t.Fatalf("ideas saw %v", seen)
	}
}

func TestOpenCandidatesLeaveOutOutsideWindowPosts(t *testing.T) {
	h := newBrowserHarness(t, true, func(o *tools.Options) { o.Template.Targets = config.DefaultTargets })
	count := func(v int64) *int64 { return &v }
	live := threads.Post{ID: "live", Username: "synthetic_author", Text: "rising", URL: "https://www.threads.com/@synthetic_author/post/live", Likes: count(100), Replies: count(2), AgeMinutes: count(10)}
	dead := threads.Post{ID: "dead", Username: "synthetic_author", Text: "nobody cares", URL: "https://www.threads.com/@synthetic_author/post/dead", Likes: count(1), Replies: count(0), AgeMinutes: count(60)}
	h.backend.Pages[threads.OpReadFeed] = threads.Page{Kind: "feed", Posts: []threads.Post{live, dead}}
	h.mustCall("find_posts", `{"source":"feed"}`)
	open, err := guard.OpenCandidates(context.Background(), h.store, h.opts.Template, nil, h.now, false)
	if err != nil || len(open) != 1 || len(h.guard.Seen()) != 2 {
		t.Fatalf("open candidates %v, %v; seen %+v", open, err, h.guard.Seen())
	}
	for _, it := range h.guard.Seen() {
		if it.URL == dead.URL && (it.Score != 0 || !strings.Contains(it.Facts, "outside window")) {
			t.Fatalf("dead post must stay listed with score 0: %+v", it)
		}
		if it.URL == live.URL && it.Ref != open[0].Ref {
			t.Fatalf("live post %+v is not the open candidate %v", it, open)
		}
	}
	// Five hours on, the live post has aged out of the target window.
	later, err := guard.OpenCandidates(context.Background(), h.store, h.opts.Template, nil, h.now.Add(5*time.Hour), false)
	if err != nil || len(later) != 0 {
		t.Fatalf("a post past the target window is still a candidate: %v, %v", later, err)
	}
}
