package eval

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/telegram"
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
// runtime and Bot API client. They share the reproducibility/report contract.
func runOwnerCase(ctx context.Context, c Case, p Profile, repeat int, opts Options) (r CaseResult) {
	run, err := beginCase(&r, c, p, repeat, "owner", "owner_error", "owner_setup_failed", nil)
	if err != nil {
		return r
	}
	defer run.end()
	fail := run.fail
	if err = run.Open(ctx, "owner.db"); err != nil {
		return r
	}
	st, logger, logs := run.Store, run.Logger, &run.Logs
	now := opts.Now().UTC().Truncate(24 * time.Hour).Add(18 * time.Hour)
	clock := func() time.Time { return now }
	tmpl := agent.OfflineTemplate()
	tmpl.LLM = p.Config
	tmpl.LLM.BaseURL = "https://fixture.invalid"
	tmpl.LLM.APIKey = config.NewSecret("synthetic-owner-private-key")
	tmpl.Telegram = config.TelegramConfig{ReportHour: 0}
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
	switch c.Scenario {
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
		if dnc, err := st.IsDoNotContact(ctx, tmpl.ID, "synthetic_user"); err != nil || !dnc {
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
		if _, err = st.DB().ExecContext(ctx, `INSERT INTO ledger_phases(agent_id,day,phase,tokens_in,tokens_out,cost_usd) VALUES(?,?,'agent',10,20,1)`, tmpl.ID, day); err != nil {
			return r
		}
		if _, err = st.SeedAction(ctx, store.SeededAction{AgentID: tmpl.ID, Kind: "post", Text: "Synthetic eval action", DryRun: tmpl.DryRun, PostedAt: now.Add(-time.Minute)}); err != nil {
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
			if strings.Contains(text, "Posts: 1\n") && strings.Contains(text, "$1.00 of $1.00 (100%)") {
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
	if run.loadToolCalls(ctx, tmpl.ID, 0) != nil {
		return fail("owner_audit_read_failed")
	}
	totals, err := st.WakeTotals(ctx, tmpl.ID, 0)
	if err != nil {
		return fail("owner_usage_read_failed")
	}
	r.Steps, r.TokensIn, r.TokensOut, r.CostUSD = totals.Steps, totals.TokensIn, totals.TokensOut, totals.CostUSD
	r.EndReason = "completed"
	r.Passed, r.Reason = matches(c.Expect, r, "")
	return r
}
