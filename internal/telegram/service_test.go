package telegram_test

import (
	"context"
	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/store"
	"github.com/canhta/threadwalker/internal/telegram"
	"strings"
	"testing"
	"time"
)

type fakeBot struct {
	updates []telegram.Update
	texts   []string
}

func (b *fakeBot) GetUpdates(context.Context, int64, int) ([]telegram.Update, error) {
	u := b.updates
	b.updates = nil
	return u, nil
}
func (b *fakeBot) SendMessage(_ context.Context, _ int64, text string, _ int64) (int64, error) {
	b.texts = append(b.texts, text)
	return int64(len(b.texts)), nil
}
func TestOwnerOnlyCommandState(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.EnsureAgent(ctx, "one")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	bot := &fakeBot{}
	svc, err := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{{ID: "one", Limits: config.LimitsConfig{MaxUSDPerDay: 1, PostsPerDay: 5, RepliesPerDay: 10}}}, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct {
		chat            int64
		command, status string
	}{{99, "/kill", "running"}, {77, "/pause", "paused"}, {77, "/resume", "running"}, {77, "/kill", "killed"}, {77, "/resume", "killed"}} {
		bot.updates = []telegram.Update{{UpdateID: int64(i + 1), Message: &telegram.Message{Chat: telegram.Chat{ID: tc.chat}, Text: tc.command}}}
		if err := svc.Poll(ctx, 0); err != nil {
			t.Fatal(err)
		}
		state, err := st.AgentState(ctx, "one")
		if err != nil || state.Status != tc.status {
			t.Fatalf("command %d: %+v %v", i, state, err)
		}
	}
}

func TestMultiAgentCommandsFailSafelyAndDNCIsPermanent(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.EnsureAgent(ctx, "one")
	st.EnsureAgent(ctx, "two")
	bot := &fakeBot{}
	svc, err := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{{ID: "one"}, {ID: "two"}}})
	if err != nil {
		t.Fatal(err)
	}
	commands := []string{"/pause", "/kill unknown", "/resume one extra", "/dnc one", "/pause two", "/dnc two @Example_User", "/status two", "/unsupported two"}
	for i, command := range commands {
		bot.updates = []telegram.Update{{UpdateID: int64(i + 1), Message: &telegram.Message{Chat: telegram.Chat{ID: 77}, Text: command}}}
		if err = svc.Poll(ctx, 0); err != nil {
			t.Fatal(err)
		}
	}
	one, _ := st.AgentState(ctx, "one")
	two, _ := st.AgentState(ctx, "two")
	if one.Status != "running" || two.Status != "paused" {
		t.Fatalf("isolation %+v %+v", one, two)
	}
	var n int
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM contacts WHERE agent_id='two' AND username='example_user' AND do_not_contact=1 AND reason='owner_request'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("dnc %d %v", n, err)
	}
	if err = svc.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(bot.texts) != 3 {
		t.Fatalf("receipts %v", bot.texts)
	}
}

func TestNoticesAndDigestOncePerEventWithContent(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.EnsureAgent(ctx, "one")
	now := time.Date(2026, 1, 1, 18, 0, 0, 0, time.UTC)
	bot := &fakeBot{}
	tmpl := config.Template{ID: "one", Telegram: config.TelegramConfig{DigestHour: 18}, Limits: config.LimitsConfig{PostsPerDay: 1, RepliesPerDay: 1, MaxUSDPerDay: 1}}
	makeService := func() *telegram.Service {
		svc, err := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{tmpl}, Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		return svc
	}
	svc := makeService()
	if _, err = st.DB().Exec(`INSERT INTO actions(id,agent_id,kind,text,posted_at) VALUES(1,'one','post','synthetic','2026-01-01T12:00:00Z');INSERT INTO ledger(agent_id,day,tokens_in,tokens_out,cost_usd) VALUES('one','2026-01-01',10,20,1);INSERT INTO metrics(agent_id,action_id,measured_at,views,likes,replies,reposts) VALUES('one',1,'2026-01-01T13:00:00Z',5,1,1,1),('one',1,'2026-01-01T17:00:00Z',10,2,3,4);INSERT INTO playbook(agent_id,text,status,created_at) VALUES('one','synthetic','testing','2026-01-01T13:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"logged_out", "captcha", "killed"} {
		if err = svc.Notice(ctx, "one", kind, "event-1"); err != nil {
			t.Fatal(err)
		}
		if err = svc.Notice(ctx, "one", kind, "event-1"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err = svc.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err = svc.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(bot.texts) != 7 {
		t.Fatalf("notices/digest %d %v", len(bot.texts), bot.texts)
	}
	found := false
	for _, text := range bot.texts {
		if strings.Contains(text, "Daily digest") {
			found = true
			for _, part := range []string{"posts 1", "replies 0", "spend $1.0000", "views 10, likes 2, replies 3, reposts 4", "playbook changes 1"} {
				if !strings.Contains(text, part) {
					t.Fatalf("digest missing %s: %s", part, text)
				}
			}
		}
	}
	if !found {
		t.Fatal("digest missing")
	}
	svc = makeService()
	if err = svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err = svc.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(bot.texts) != 7 {
		t.Fatal("restart duplicated events")
	}
	now = now.Add(24 * time.Hour)
	if err = svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err = svc.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(bot.texts) != 8 {
		t.Fatal("next day digest missing")
	}
}

func TestAskOwnerDoesNotCallNetworkAndReplyIsScoped(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.EnsureAgent(ctx, "one")
	st.EnsureAgent(ctx, "two")
	bot := &fakeBot{}
	tmpl := config.Template{ID: "one", LLM: config.LLMConfig{APIKey: config.NewSecret("dummy-private-value")}}
	svc, err := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{tmpl}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := svc.QueueOwner(ctx, "one", "Which topic?", true)
	if err != nil || id == 0 || len(bot.texts) != 0 {
		t.Fatalf("blocking/network ask %d %v", id, err)
	}
	if err = svc.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	for i, chat := range []int64{99, 77, 77} {
		bot.updates = []telegram.Update{{UpdateID: int64(i + 1), Message: &telegram.Message{Chat: telegram.Chat{ID: chat}, Text: "topic dummy-private-value", ReplyToMessage: &telegram.Message{MessageID: 1}}}}
		if err = svc.Poll(ctx, 0); err != nil {
			t.Fatal(err)
		}
	}
	answers, err := st.OwnerAnswers(ctx, "one", 10)
	if err != nil || len(answers) != 1 || answers[0].Text != "topic [REDACTED]" || answers[0].AnsweredID != id {
		t.Fatalf("answer %+v %v", answers, err)
	}
	other, err := st.OwnerAnswers(ctx, "two", 10)
	if err != nil || len(other) != 0 {
		t.Fatal("cross-agent answer")
	}
}
