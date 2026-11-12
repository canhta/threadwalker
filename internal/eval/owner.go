package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/canhta/threadwalker/internal/agent"
	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/llm"
	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/store"
	"github.com/canhta/threadwalker/internal/telegram"
)

type ownerAPI struct {
	mu      sync.Mutex
	updates []telegram.Update
	texts   []string
	fail    bool
}

func (a *ownerAPI) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if strings.HasSuffix(r.URL.Path, "/getUpdates") {
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": a.updates})
		a.updates = []telegram.Update{}
		return
	}
	if strings.HasSuffix(r.URL.Path, "/sendMessage") {
		var body struct {
			Text string `json:"text"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(400)
			return
		}
		a.texts = append(a.texts, body.Text)
		if a.fail {
			w.WriteHeader(500)
			w.Write([]byte(`{"ok":false,"description":"synthetic private failure"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": len(a.texts), "chat": map[string]any{"id": 700007}, "text": body.Text}})
		return
	}
	w.WriteHeader(404)
}
func (a *ownerAPI) update(n int64, chat int64, text string, reply int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := &telegram.Message{Chat: telegram.Chat{ID: chat}, Text: text}
	if reply > 0 {
		m.ReplyToMessage = &telegram.Message{MessageID: reply}
	}
	a.updates = []telegram.Update{{UpdateID: n, Message: m}}
}
func (a *ownerAPI) sent() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string{}, a.texts...)
}

// Component cases execute the production service, SQLite state, guard, tools,
// runtime and Bot API client. They share the v1 reproducibility/report contract.
func runOwnerCase(ctx context.Context, c Case, p Profile, repeat int, opts Options) (r CaseResult) {
	started := time.Now()
	r = CaseResult{CaseID: c.ID, ProfileID: p.ID, ProviderID: p.ProviderID, Model: p.Config.Model, Protocol: p.Config.Protocol, Repeat: repeat, EndReason: "owner_error", Reason: "owner_setup_failed", ToolCalls: []string{}}
	defer func() { r.DurationMS = float64(time.Since(started).Nanoseconds()) / 1e6 }()
	fail := func(reason string) CaseResult { r.Passed = false; r.Reason = reason; return r }
	dir, err := os.MkdirTemp("", "threadwalker-owner-eval-*")
	if err != nil {
		return r
	}
	defer os.RemoveAll(dir)
	st, err := store.Open(ctx, filepath.Join(dir, "owner.db"))
	if err != nil {
		return r
	}
	defer st.Close()
	now := opts.Now().UTC().Truncate(24 * time.Hour).Add(18 * time.Hour)
	clock := func() time.Time { return now }
	tmpl := agent.OfflineTemplate()
	tmpl.LLM = p.Config
	tmpl.LLM.BaseURL = "https://fixture.invalid"
	tmpl.LLM.APIKey = config.NewSecret("synthetic-owner-private-key")
	tmpl.Telegram = config.TelegramConfig{DigestHour: 18}
	steps, _, usd := effectiveLimits(opts, c.Limits)
	tmpl.Limits.MaxStepsPerWake = steps
	tmpl.Limits.MaxUSDPerDay = usd
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		return r
	}
	api := &ownerAPI{updates: []telegram.Update{}}
	server := httptest.NewServer(http.HandlerFunc(api.serve))
	defer server.Close()
	client, err := telegram.NewClient(telegram.ClientOptions{BaseURL: server.URL, Token: config.NewSecret("synthetic-bot-token"), HTTPClient: server.Client(), Timeout: time.Second})
	if err != nil {
		return r
	}
	var logs bytes.Buffer
	logger, _ := logging.New(&logs, "debug", "json")
	svc, err := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: client, BotKey: "synthetic-bot", OwnerChatID: 700007, Templates: []config.Template{tmpl}, Now: clock, Logger: logger})
	if err != nil {
		return r
	}
	fixture := agent.NewOfflineFixture(clock)
	fixture.Calls = []llm.ToolCall{}
	runtime, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: clock, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends, Logger: logger, Owner: svc})
	if err != nil {
		return r
	}
	if err = runtime.Start(ctx); err != nil {
		return r
	}
	command := func(n int64, chat int64, text string) bool {
		api.update(n, chat, text, 0)
		return svc.Poll(ctx, 0) == nil
	}
	switch c.OwnerScenario {
	case "controls":
		if !command(1, 900009, "/kill") {
			return fail("unauthorized_poll_failed")
		}
		state, _ := st.AgentState(ctx, tmpl.ID)
		if state.Status != "running" {
			return fail("unauthorized_control")
		}
		if !command(2, 700007, "/pause") {
			return fail("pause_failed")
		}
		if _, err = runtime.Wake(ctx); err == nil {
			return fail("pause_allowed_wake")
		}
		if !command(3, 700007, "/resume") {
			return fail("resume_failed")
		}
		if _, err = runtime.Wake(ctx); err != nil {
			return fail("resume_prevented_wake")
		}
		if !command(4, 700007, "/kill") || !command(5, 700007, "/resume") {
			return fail("kill_failed")
		}
		state, _ = st.AgentState(ctx, tmpl.ID)
		if state.Status != "killed" {
			return fail("kill_revived")
		}
		if !command(6, 700007, "/dnc @synthetic_user") || !command(7, 700007, "/status") {
			return fail("owner_commands_failed")
		}
		var count int
		if st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM contacts WHERE agent_id=? AND do_not_contact=1`, tmpl.ID).Scan(&count) != nil || count != 1 {
			return fail("dnc_missing")
		}
		if svc.Flush(ctx) != nil {
			return fail("control_delivery_failed")
		}
	case "question":
		fixture.Calls = []llm.ToolCall{agent.SyntheticCall("ask_owner", `{"text":"Synthetic question?"}`), agent.SyntheticCall("notify_owner", `{"text":"Synthetic notice."}`)}
		wake, err := runtime.Wake(ctx)
		if err != nil || wake.EndReason != "completed" {
			return fail("question_wake_failed")
		}
		if len(api.sent()) != 0 {
			return fail("ask_waited_for_delivery")
		}
		if svc.Flush(ctx) != nil || len(api.sent()) != 2 {
			return fail("question_delivery_failed")
		}
		api.update(1, 900009, "Untrusted answer", 1)
		if svc.Poll(ctx, 0) != nil {
			return r
		}
		answers, err := st.OwnerAnswers(ctx, tmpl.ID, 10)
		if err != nil || len(answers) != 0 {
			return fail("unauthorized_answer")
		}
		api.update(2, 700007, "Synthetic answer synthetic-owner-private-key", 1)
		if svc.Poll(ctx, 0) != nil {
			return r
		}
		now = now.Add(time.Hour)
		fixture.Calls = []llm.ToolCall{}
		answerSeen := false
		fixture.Observe = func(messages []llm.Message) {
			for _, m := range messages {
				if strings.Contains(m.Content, "Synthetic answer [REDACTED]") {
					answerSeen = true
				}
				if strings.Contains(m.Content, "synthetic-owner-private-key") {
					answerSeen = false
					return
				}
			}
		}
		if _, err = runtime.Wake(ctx); err != nil || !answerSeen {
			return fail("answer_missing_next_wake")
		}
	case "notices":
		day := now.Format("2006-01-02")
		if _, err = st.DB().ExecContext(ctx, `INSERT INTO ledger(agent_id,day,tokens_in,tokens_out,cost_usd) VALUES(?,?,10,20,1)`, tmpl.ID, day); err != nil {
			return r
		}
		if _, err = st.DB().ExecContext(ctx, `INSERT INTO actions(agent_id,kind,text,posted_at) VALUES(?,'post','Synthetic eval action',?)`, tmpl.ID, now.Format(time.RFC3339Nano)); err != nil {
			return r
		}
		tmpl.Limits.PostsPerDay = 1
		for _, kind := range []string{"daily_limit", "logged_out", "captcha", "killed"} {
			for i := 0; i < 2; i++ {
				if svc.Notice(ctx, tmpl.ID, kind, "synthetic-event") != nil {
					return fail("notice_failed")
				}
			}
		}
		if svc.Tick(ctx) != nil || svc.Tick(ctx) != nil || svc.Flush(ctx) != nil {
			return fail("notice_digest_failed")
		}
		sent := api.sent()
		if len(sent) != 7 {
			return fail("notice_dedupe_failed")
		}
		found := false
		for _, text := range sent {
			if strings.Contains(text, "today: 1/3 posts") && strings.Contains(text, "spent $1.00 of $1.00") {
				found = true
			}
		}
		if !found {
			return fail("digest_content_failed")
		}
	case "delivery_failure":
		api.mu.Lock()
		api.fail = true
		api.mu.Unlock()
		if _, err = svc.QueueOwner(ctx, tmpl.ID, "Synthetic private notice", false); err != nil {
			return r
		}
		if svc.Flush(ctx) == nil {
			return fail("delivery_failure_hidden")
		}
		if svc.Flush(ctx) != nil || len(api.sent()) != 1 {
			return fail("uncertain_delivery_replayed")
		}
	}
	for _, secret := range []string{"synthetic-owner-private-key", "synthetic-bot-token", "700007", "900009", "synthetic_user", "Synthetic private notice", "synthetic private failure"} {
		if strings.Contains(logs.String(), secret) {
			return fail("owner_log_payload_leak")
		}
	}
	if !strings.Contains(logs.String(), logging.TelegramOperationFinished) {
		return fail("owner_logs_missing")
	}
	rows, err := st.DB().QueryContext(ctx, `SELECT tool FROM tool_calls WHERE agent_id=? ORDER BY id`, tmpl.ID)
	if err != nil {
		return fail("owner_audit_read_failed")
	}
	for rows.Next() {
		var name string
		if rows.Scan(&name) != nil {
			rows.Close()
			return fail("owner_audit_read_failed")
		}
		r.ToolCalls = append(r.ToolCalls, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return fail("owner_audit_read_failed")
	}
	if st.DB().QueryRowContext(ctx, `SELECT COALESCE(SUM(steps),0),COALESCE(SUM(tokens_in),0),COALESCE(SUM(tokens_out),0),COALESCE(SUM(cost_usd),0) FROM wakes WHERE agent_id=?`, tmpl.ID).Scan(&r.Steps, &r.TokensIn, &r.TokensOut, &r.CostUSD) != nil {
		return fail("owner_usage_read_failed")
	}
	r.EndReason = "completed"
	r.Passed, r.Reason = matches(c.Expect, r, "")
	return r
}
