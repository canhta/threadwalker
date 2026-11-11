package tools_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/guard"
	"github.com/canhta/threadwalker/internal/llm"
	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/store"
	"github.com/canhta/threadwalker/internal/tools"
)

const ruleAgentNotRunning = "agent_not_running"

type contactClock struct{ at time.Time }

func (c *contactClock) Now() time.Time          { return c.at }
func (c *contactClock) Advance(d time.Duration) { c.at = c.at.Add(d) }

type contactHarness struct {
	t          *testing.T
	st         *store.Store
	clock      *contactClock
	guard      *guard.Guard
	tools      *tools.Dispatcher
	opts       tools.Options
	trendCalls int
	calls      int
}

func contactTemplate() config.Template {
	return config.Template{
		ID:     "guard-agent",
		DryRun: true,
		Persona: config.PersonaConfig{
			ForbiddenPhrases: []string{"Tuy nhiên,"},
		},
		Limits: config.LimitsConfig{
			PostsPerDay:              100,
			RepliesPerDay:            100,
			RepliesPerUser7d:         100,
			MinSecondsBetweenActions: 1,
			MaxStepsPerWake:          20,
			MaxUSDPerDay:             1,
			MinMinutesBetweenWakes:   15,
		},
	}
}

func newContactHarness(t *testing.T, tmpl config.Template, logger *slog.Logger) *contactHarness {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "contacts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.DB().Exec(`INSERT INTO agents(id,status) VALUES('guard-agent','running'); INSERT INTO wakes(id,agent_id,started_at) VALUES(1,'guard-agent','2026-11-11T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	h := &contactHarness{t: t, st: st, clock: &contactClock{at: time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)}}
	g, err := guard.New(guard.Options{Store: st, Template: tmpl, WakeID: 1, Now: h.clock.Now, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	h.guard = g
	h.opts = tools.Options{Store: st, Template: tmpl, WakeID: 1, Now: h.clock.Now, Logger: logger, FetchTrends: func(context.Context) ([]tools.Trend, error) {
		h.trendCalls++
		return []tools.Trend{{Source: "synthetic", Title: "synthetic-trend", URL: "https://example.test/trend"}}, nil
	}}
	d, err := tools.NewDispatcher(g, h.opts)
	if err != nil {
		t.Fatal(err)
	}
	h.tools = d
	return h
}

func (h *contactHarness) dispatch(tool, raw string) (string, error) {
	h.t.Helper()
	h.calls++
	return h.tools.Dispatch(context.Background(), llm.ToolCall{ID: fmt.Sprintf("contact-call-%d", h.calls), Type: "function", Function: llm.CallFunction{Name: tool, Arguments: raw}})
}

func TestPermanentDoNotContactRefusesRepliesWithNoAction(t *testing.T) {
	h := newContactHarness(t, contactTemplate(), nil)
	ctx := context.Background()

	if err := h.st.MarkDoNotContact(ctx, "guard-agent", " @Synthetic.Contact ", "owner_request"); err != nil {
		t.Fatal(err)
	}
	// A second permanent add keeps the first fixed reason and stays add-only.
	if err := h.st.MarkDoNotContact(ctx, "guard-agent", "synthetic.contact", "agent_request"); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err := h.st.DB().QueryRow(`SELECT reason FROM contacts WHERE agent_id='guard-agent' AND username='synthetic.contact' AND do_not_contact=1`).Scan(&reason); err != nil || reason != "owner_request" {
		t.Fatalf("contact reason %q, %v", reason, err)
	}

	// Owner control works while the LLM is paused and applies when it runs.
	if _, err := h.st.DB().Exec(`UPDATE agents SET status='paused'`); err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkDoNotContact(ctx, "guard-agent", "Paused.Target", "owner_request"); err != nil {
		t.Fatalf("owner DNC while paused: %v", err)
	}
	if _, err := h.st.DB().Exec(`UPDATE agents SET status='running'`); err != nil {
		t.Fatal(err)
	}

	for _, username := range []string{"synthetic.contact", "paused.target"} {
		beforeActions := h.actionRows()
		_, err := h.dispatch("reply", fmt.Sprintf(`{"text":"synthetic reply to @%s","target_username":%q,"target_url":"https://threads.test/@%s/post/1"}`, username, username, username))
		if err == nil || !errors.Is(err, guard.ErrBlocked) {
			t.Fatalf("%s reply was not refused by the guard: %v", username, err)
		}
		var blocked, result string
		if err := h.st.DB().QueryRow(`SELECT blocked_by,result_json FROM tool_calls ORDER BY id DESC LIMIT 1`).Scan(&blocked, &result); err != nil {
			t.Fatal(err)
		}
		if blocked != "do_not_contact" || result != "" {
			t.Fatalf("%s blocked_by=%q result=%q", username, blocked, result)
		}
		if after := h.actionRows(); after != beforeActions {
			t.Fatalf("%s refusal changed actions %d -> %d", username, beforeActions, after)
		}
		h.clock.Advance(time.Second)
	}

	for _, tc := range []struct{ agent, username, reason string }{
		{"missing-agent", "user", "owner_request"},
		{"guard-agent", "bad name", "owner_request"},
		{"guard-agent", "user", "message content is not a reason code"},
	} {
		if err := h.st.MarkDoNotContact(ctx, tc.agent, tc.username, tc.reason); err == nil {
			t.Fatalf("invalid permanent contact accepted: %+v", tc)
		}
	}
}

func (h *contactHarness) actionRows() int {
	h.t.Helper()
	var n int
	if err := h.st.DB().QueryRow(`SELECT COUNT(*) FROM actions`).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func TestDispatcherCannotBeConstructedWithoutMatchingGuard(t *testing.T) {
	h := newContactHarness(t, contactTemplate(), nil)
	if _, err := tools.NewDispatcher(nil, h.opts); err == nil {
		t.Fatal("dispatcher accepted a nil guard")
	}
	if _, err := tools.NewDispatcher(&guard.Guard{}, h.opts); err == nil {
		t.Fatal("dispatcher accepted a zero guard")
	}
	otherWake, err := guard.New(guard.Options{Store: h.st, Template: h.opts.Template, WakeID: 2, Now: h.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tools.NewDispatcher(otherWake, h.opts); err == nil {
		t.Fatal("dispatcher accepted a guard for another wake")
	}
	otherAgentTemplate := h.opts.Template
	otherAgentTemplate.ID = "other-agent"
	otherAgent, err := guard.New(guard.Options{Store: h.st, Template: otherAgentTemplate, WakeID: 1, Now: h.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tools.NewDispatcher(otherAgent, h.opts); err == nil {
		t.Fatal("dispatcher accepted a guard for another agent")
	}
	otherStore, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "other.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer otherStore.Close()
	if _, err := otherStore.DB().Exec(`INSERT INTO agents(id,status) VALUES('guard-agent','running'); INSERT INTO wakes(id,agent_id,started_at) VALUES(1,'guard-agent','2026-11-11T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	foreign, err := guard.New(guard.Options{Store: otherStore, Template: h.opts.Template, WakeID: 1, Now: h.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tools.NewDispatcher(foreign, h.opts); err == nil {
		t.Fatal("dispatcher accepted a guard for another store")
	}
	if _, err := tools.NewDispatcher(h.guard, h.opts); err != nil {
		t.Fatalf("matching guard refused: %v", err)
	}
}

func TestPausedOrKilledRefusesEveryRegisteredTool(t *testing.T) {
	for _, status := range []string{"paused", "killed"} {
		t.Run(status, func(t *testing.T) {
			h := newContactHarness(t, contactTemplate(), nil)
			if _, err := h.st.DB().Exec(`UPDATE agents SET status=?`, status); err != nil {
				t.Fatal(err)
			}
			definitions := h.tools.Definitions()
			if len(definitions) == 0 {
				t.Fatal("no registered tools")
			}
			for i, definition := range definitions {
				name := definition.Function.Name
				raw := requiredArguments(h.t, definition.Function.Parameters)
				_, err := h.tools.Dispatch(context.Background(), llm.ToolCall{
					ID:       fmt.Sprintf("%s-call-%d", status, i),
					Type:     "function",
					Function: llm.CallFunction{Name: name, Arguments: raw},
				})
				if err == nil {
					t.Fatalf("%s accepted %s", status, name)
				}
				var blocked, result string
				if err := h.st.DB().QueryRow(`SELECT blocked_by,result_json FROM tool_calls ORDER BY id DESC LIMIT 1`).Scan(&blocked, &result); err != nil {
					t.Fatalf("%s audit for %s: %v", status, name, err)
				}
				if blocked != ruleAgentNotRunning || result != "" {
					t.Fatalf("%s %s blocked_by=%q result=%q", status, name, blocked, result)
				}
			}
			var audits, actions, contacts, notes int
			var note, nextWake string
			h.st.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls`).Scan(&audits)
			h.st.DB().QueryRow(`SELECT COUNT(*) FROM actions`).Scan(&actions)
			h.st.DB().QueryRow(`SELECT COUNT(*) FROM contacts`).Scan(&contacts)
			h.st.DB().QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&notes)
			h.st.DB().QueryRow(`SELECT self_note, COALESCE(next_wake_at,'') FROM agents WHERE id='guard-agent'`).Scan(&note, &nextWake)
			if audits != len(definitions) || actions != 0 || contacts != 0 || notes != 0 || note != "" || nextWake != "" {
				t.Fatalf("%s effects audits=%d tools=%d actions=%d contacts=%d notes=%d note=%q next=%q", status, audits, len(definitions), actions, contacts, notes, note, nextWake)
			}
			if h.trendCalls != 0 {
				t.Fatalf("%s ran fetch_trends %d times", status, h.trendCalls)
			}
		})
	}
}

// requiredArguments synthesizes valid required fields from the registered tool
// schema. It keeps the pause/kill test covering every tool, including owner
// tools added after this test, without knowing their private implementations.
func requiredArguments(t *testing.T, parameters json.RawMessage) string {
	t.Helper()
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(parameters, &schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	args := map[string]any{}
	for _, key := range schema.Required {
		switch schema.Properties[key].Type {
		case "string":
			switch key {
			case "at":
				args[key] = time.Date(2026, 11, 11, 11, 0, 0, 0, time.UTC).Format(time.RFC3339)
			case "url", "target_url":
				args[key] = "https://threads.test/@synthetic/post/1"
			case "target_username":
				args[key] = "synthetic_author"
			default:
				args[key] = "synthetic body"
			}
		case "integer":
			args[key] = 5
		case "array":
			args[key] = []int64{}
		default:
			t.Fatalf("required field %q has unsupported schema type %q", key, schema.Properties[key].Type)
		}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestGuardDecisionLogsKeepRuleIDsAndOmitPayloads(t *testing.T) {
	var logs bytes.Buffer
	logger, err := logging.New(&logs, "debug", "json")
	if err != nil {
		t.Fatal(err)
	}
	tmpl := contactTemplate()
	tmpl.Telegram.BotToken = config.NewSecret("fake-secret-canary-4b8d")
	h := newContactHarness(t, tmpl, logger)

	username := "synthetic-contact-7f3a"
	body := "TUY NHIÊN, synthetic-body-9c1e"
	_, err = h.dispatch("reply", fmt.Sprintf(`{"text":%q,"target_username":%q,"target_url":"https://threads.test/@%s/post/1"}`, body, username, username))
	if err == nil {
		t.Fatal("forbidden phrase reply was allowed")
	}
	var blocked string
	if err := h.st.DB().QueryRow(`SELECT blocked_by FROM tool_calls ORDER BY id DESC LIMIT 1`).Scan(&blocked); err != nil || blocked != "forbidden_phrase" {
		t.Fatalf("blocked_by=%q err=%v", blocked, err)
	}
	captured := logs.String()
	for _, want := range []string{`"event":"tool.blocked"`, `"reason":"forbidden_phrase"`} {
		if !strings.Contains(captured, want) {
			t.Fatalf("log missing %s: %s", want, captured)
		}
	}
	for _, leak := range []string{username, "synthetic-body-9c1e", "TUY NHIÊN"} {
		if strings.Contains(captured, leak) {
			t.Fatalf("log leaked %q: %s", leak, captured)
		}
	}

	logs.Reset()
	secretBody := "synthetic-body-9c1e fake-secret-canary-4b8d"
	_, err = h.dispatch("post", fmt.Sprintf(`{"text":%q}`, secretBody))
	if err == nil {
		t.Fatal("secret-bearing post was allowed")
	}
	if err := h.st.DB().QueryRow(`SELECT blocked_by FROM tool_calls ORDER BY id DESC LIMIT 1`).Scan(&blocked); err != nil || blocked != "sensitive_arguments" {
		t.Fatalf("blocked_by=%q err=%v", blocked, err)
	}
	captured = logs.String()
	for _, want := range []string{`"event":"tool.blocked"`, `"reason":"sensitive_arguments"`} {
		if !strings.Contains(captured, want) {
			t.Fatalf("log missing %s: %s", want, captured)
		}
	}
	for _, leak := range []string{"fake-secret-canary-4b8d", "synthetic-body-9c1e"} {
		if strings.Contains(captured, leak) {
			t.Fatalf("log leaked %q: %s", leak, captured)
		}
	}
}
