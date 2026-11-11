package telegram_test

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
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/agent"
	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/llm"
	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/store"
	"github.com/canhta/threadwalker/internal/telegram"
)

func TestFakeBotPauseResumeWakeAndRedactedTrace(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "owner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	tmpl := agent.OfflineTemplate()
	tmpl.Telegram = config.TelegramConfig{DigestHour: 18, BotToken: config.NewSecret("synthetic-private-bot"), OwnerChatID: config.NewSecret("700007")}
	var mu sync.Mutex
	var updates []telegram.Update
	sent := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": updates})
			updates = []telegram.Update{}
			return
		}
		var body struct{ Text string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		sent = append(sent, body.Text)
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": len(sent), "chat": map[string]int64{"id": 700007}}})
	}))
	defer server.Close()
	client, err := telegram.NewClient(telegram.ClientOptions{BaseURL: server.URL, Token: tmpl.Telegram.BotToken, HTTPClient: server.Client(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var events bytes.Buffer
	logger, _ := logging.New(&events, "debug", "json")
	svc, err := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: client, BotKey: "synthetic-bot", OwnerChatID: 700007, Templates: []config.Template{tmpl}, Now: clock, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	fixture := agent.NewOfflineFixture(clock)
	fixture.Calls = []llm.ToolCall{agent.SyntheticCall("ask_owner", `{"text":"Synthetic private question?"}`), agent.SyntheticCall("notify_owner", `{"text":"Synthetic private notification."}`)}
	runtime, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: clock, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends, Owner: svc, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	command := func(n int64, chat int64, text string, reply int64) {
		t.Helper()
		m := &telegram.Message{Chat: telegram.Chat{ID: chat}, Text: text}
		if reply > 0 {
			m.ReplyToMessage = &telegram.Message{MessageID: reply}
		}
		mu.Lock()
		updates = []telegram.Update{{UpdateID: n, Message: m}}
		mu.Unlock()
		if err := svc.Poll(ctx, 0); err != nil {
			t.Fatal(err)
		}
	}
	command(1, 900009, "/kill", 0)
	command(2, 700007, "/pause", 0)
	if err = (agent.Scheduler{Agents: []*agent.Runtime{runtime}, Now: clock}).Run(ctx, true); err != nil {
		t.Fatal(err)
	}
	var wakes int
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM wakes`).Scan(&wakes); err != nil || wakes != 0 {
		t.Fatalf("paused wake ran %d %v", wakes, err)
	}
	command(3, 700007, "/resume", 0)
	if err = (agent.Scheduler{Agents: []*agent.Runtime{runtime}, Now: clock}).Run(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM wakes`).Scan(&wakes); err != nil || wakes != 1 {
		t.Fatalf("resumed wake missing %d %v", wakes, err)
	}
	mu.Lock()
	before := len(sent)
	mu.Unlock()
	if before != 0 {
		t.Fatal("ask_owner waited for HTTP")
	}
	if err = svc.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	// The first two delivered messages are the pause/resume receipts, then the question.
	command(4, 900009, "Synthetic forged answer", 3)
	command(5, 700007, "Synthetic answer offline-dummy-key", 3)
	now = now.Add(time.Hour)
	fixture.Calls = nil
	seen := false
	fixture.Observe = func(messages []llm.Message) {
		for _, m := range messages {
			if strings.Contains(m.Content, "Synthetic answer [REDACTED]") {
				seen = true
			}
		}
	}
	if _, err = runtime.Wake(ctx); err != nil || !seen {
		t.Fatalf("answer absent next wake %v %v", seen, err)
	}
	command(6, 700007, "/dnc @synthetic_user", 0)
	command(7, 700007, "/status", 0)
	for _, kind := range []string{"daily_limit", "budget_80", "budget_100", "killed"} {
		for i := 0; i < 2; i++ {
			if err = svc.Notice(ctx, tmpl.ID, kind, "synthetic-event"); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, kind := range []string{"logged_out", "captcha"} {
		for i := 0; i < 2; i++ {
			if err = svc.BrowserStopped(ctx, tmpl.ID, kind, "synthetic-browser"); err != nil {
				t.Fatal(err)
			}
		}
	}
	now = time.Date(2026, 1, 1, 18, 0, 0, 0, time.UTC)
	if err = svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err = svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err = svc.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	trace := events.String()
	for _, secret := range []string{"synthetic-private-bot", "700007", "900009", "offline-dummy-key", "Synthetic private", "Synthetic answer", "synthetic_user", "https://"} {
		if strings.Contains(trace, secret) {
			t.Fatalf("trace leaked %q", secret)
		}
	}
	for _, event := range []string{"telegram.operation.finished", "tool.started", "guard.checked", "audit.recorded", "wake.started", "wake.finished"} {
		if !strings.Contains(trace, `"event":"`+event+`"`) {
			t.Fatalf("missing event %s", event)
		}
	}
	// Explicit capture records synthetic events only; ordinary test runs write nothing.
	if path := os.Getenv("THREADWALKER_OWNER_TRACE"); path != "" {
		if err = os.WriteFile(path, events.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
