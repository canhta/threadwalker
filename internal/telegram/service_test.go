package telegram_test

import (
	"bytes"
	"context"
	"errors"
	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/store"
	"github.com/canhta/threadwalker/internal/telegram"
	"strings"
	"testing"
	"time"
)

type fakeBot struct {
	pollErr, sendErr error
	updates          []telegram.Update
	texts            []string
}

func (b *fakeBot) GetUpdates(context.Context, int64, int) ([]telegram.Update, error) {
	if b.pollErr != nil {
		return nil, b.pollErr
	}
	u := b.updates
	b.updates = nil
	return u, nil
}
func (b *fakeBot) SendMessage(_ context.Context, _ int64, text string, _ int64) (int64, error) {
	b.texts = append(b.texts, text)
	return int64(len(b.texts)), b.sendErr
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

func TestInvalidContactCommandDoesNotBlockFollowingOwnerControls(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.EnsureAgent(ctx, "one")
	bot := &fakeBot{updates: []telegram.Update{{UpdateID: 1, Message: &telegram.Message{Chat: telegram.Chat{ID: 77}, Text: "/dnc invalid/username"}}, {UpdateID: 2, Message: &telegram.Message{Chat: telegram.Chat{ID: 77}, Text: "/dnc one"}}, {UpdateID: 3, Message: &telegram.Message{Chat: telegram.Chat{ID: 77}, Text: "/pause"}}}}
	svc, err := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{{ID: "one"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Poll(ctx, 0); err != nil {
		t.Fatal(err)
	}
	state, _ := st.AgentState(ctx, "one")
	if state.Status != "paused" {
		t.Fatal("invalid command stalled following pause")
	}
	var n int
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM contacts WHERE agent_id='one' AND username='one' AND do_not_contact=1`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("single agent username ambiguity %d %v", n, err)
	}
}

func TestServiceFailuresHaveRedactedLogsAndNeverReplay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.EnsureAgent(ctx, "one")
	raw := errors.New("synthetic-secret https://bot.invalid/owner/77 private-body")
	bot := &fakeBot{sendErr: raw}
	var logs bytes.Buffer
	logger, _ := logging.New(&logs, "debug", "json")
	svc, err := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{{ID: "one"}}, Logger: logger, Wait: func(context.Context, time.Duration) error { cancel(); return context.Canceled }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.QueueOwner(ctx, "one", "private-body", false); err != nil {
		t.Fatal(err)
	}
	if err = svc.Flush(ctx); err == nil {
		t.Fatal("delivery failure hidden")
	}
	if err = svc.Flush(ctx); err != nil || len(bot.texts) != 1 {
		t.Fatalf("uncertain send replayed %v %d", err, len(bot.texts))
	}
	bot.pollErr = context.DeadlineExceeded
	if err = svc.Poll(ctx, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout identity lost %v", err)
	}
	bot.pollErr = raw
	if err = svc.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("service cancellation %v", err)
	}
	for _, secret := range []string{"synthetic-secret", "https://", "private-body", "owner/77"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("logs leak %s", secret)
		}
	}
	for _, reason := range []string{"delivery_failed", "poll_failed", "iteration_failed"} {
		if !strings.Contains(logs.String(), reason) {
			t.Fatalf("missing reason %s", reason)
		}
	}
}

func TestServiceConfigurationAndInvalidMessages(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.EnsureAgent(ctx, "one")
	bot := &fakeBot{}
	valid := telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{{ID: "one"}}}
	for _, change := range []func(*telegram.ServiceOptions){func(o *telegram.ServiceOptions) { o.Store = nil }, func(o *telegram.ServiceOptions) { o.Bot = nil }, func(o *telegram.ServiceOptions) { o.BotKey = "" }, func(o *telegram.ServiceOptions) { o.OwnerChatID = 0 }, func(o *telegram.ServiceOptions) { o.Templates = nil }, func(o *telegram.ServiceOptions) { o.Templates = []config.Template{{ID: ""}} }, func(o *telegram.ServiceOptions) { o.Templates = []config.Template{{ID: "one"}, {ID: "one"}} }, func(o *telegram.ServiceOptions) {
		o.Templates = []config.Template{{ID: "one", Telegram: config.TelegramConfig{DigestHour: 24}}}
	}} {
		o := valid
		change(&o)
		if _, err = telegram.NewService(o); err == nil {
			t.Fatal("invalid service accepted")
		}
	}
	svc, err := telegram.NewService(valid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.QueueOwner(ctx, "unknown", "text", false); err == nil {
		t.Fatal("unknown agent")
	}
	if _, err = svc.QueueOwner(ctx, "one", strings.Repeat("x", 3501), false); err == nil {
		t.Fatal("oversized message")
	}
	for _, tc := range []struct{ id, kind, event string }{{"unknown", "captcha", "event"}, {"one", "unknown", "event"}, {"one", "captcha", ""}} {
		if err = svc.Notice(ctx, tc.id, tc.kind, tc.event); err == nil {
			t.Fatal("invalid notice")
		}
	}
	if err = svc.BrowserStopped(ctx, "one", "unknown", "event"); err == nil {
		t.Fatal("invalid browser event")
	}
	if err = svc.BrowserStopped(ctx, "unknown", "captcha", "event"); err == nil {
		t.Fatal("unknown browser agent")
	}
	bot.updates = []telegram.Update{{UpdateID: 1}, {UpdateID: 2, Message: &telegram.Message{Chat: telegram.Chat{ID: 77}, Text: "unmatched"}}, {UpdateID: 3, Message: &telegram.Message{Chat: telegram.Chat{ID: 77}, Text: "unmatched", ReplyToMessage: &telegram.Message{MessageID: 888}}}}
	if err = svc.Poll(ctx, 0); err != nil {
		t.Fatal(err)
	}
	n, err := st.OwnerOffset(ctx, "one", "fake")
	if err != nil || n != 4 {
		t.Fatalf("ignored updates stalled offset %d %v", n, err)
	}
	bot.updates = []telegram.Update{{UpdateID: 3, Message: &telegram.Message{Chat: telegram.Chat{ID: 77}, Text: "/kill"}}}
	if err = svc.Poll(ctx, 0); err != nil {
		t.Fatal(err)
	}
	state, _ := st.AgentState(ctx, "one")
	if state.Status != "running" {
		t.Fatal("stale update applied")
	}
}

func TestOwnerQueueConcurrentWithControls(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.EnsureAgent(ctx, "one")
	bot := &fakeBot{updates: []telegram.Update{{UpdateID: 1, Message: &telegram.Message{Chat: telegram.Chat{ID: 77}, Text: "/pause"}}, {UpdateID: 2, Message: &telegram.Message{Chat: telegram.Chat{ID: 77}, Text: "/resume"}}}}
	svc, err := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{{ID: "one"}}})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 21)
	for i := 0; i < 20; i++ {
		go func() {
			<-start
			_, err := svc.QueueOwner(ctx, "one", "Synthetic concurrent question?", true)
			results <- err
		}()
	}
	go func() { <-start; results <- svc.Poll(ctx, 0) }()
	close(start)
	for i := 0; i < 21; i++ {
		if err = <-results; err != nil {
			t.Error(err)
		}
	}
	pending, err := st.PendingOwner(ctx, []string{"one"})
	if err != nil || len(pending) != 22 {
		t.Fatalf("lost queued messages %d %v", len(pending), err)
	}
}

func TestOwnerControlPersistenceFailureDoesNotConsumeUpdate(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.EnsureAgent(ctx, "one")
	update := telegram.Update{UpdateID: 1, Message: &telegram.Message{Chat: telegram.Chat{ID: 77}, Text: "/pause"}}
	bot := &fakeBot{updates: []telegram.Update{update}}
	svc, err := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{{ID: "one"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec(`CREATE TRIGGER reject_owner BEFORE INSERT ON owner_msgs BEGIN SELECT RAISE(ABORT,'synthetic storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = svc.Poll(ctx, 0); err == nil {
		t.Fatal("storage failure hidden")
	}
	offset, err := st.OwnerOffset(ctx, "one", "fake")
	if err != nil || offset != 0 {
		t.Fatal("failed command consumed")
	}
	state, _ := st.AgentState(ctx, "one")
	if state.Status != "running" {
		t.Fatal("control without receipt")
	}
	if _, err = svc.QueueOwner(ctx, "one", "Synthetic question?", true); err == nil {
		t.Fatal("queue failure hidden")
	}
	if err = svc.Notice(ctx, "one", "captcha", "event"); err == nil {
		t.Fatal("notice failure hidden")
	}
	if err = svc.Tick(ctx); err == nil {
		t.Fatal("digest persistence failure hidden")
	}
	if _, err = st.DB().Exec(`DROP TRIGGER reject_owner`); err != nil {
		t.Fatal(err)
	}
	bot.updates = []telegram.Update{update}
	if err = svc.Poll(ctx, 0); err != nil {
		t.Fatal(err)
	}
	state, _ = st.AgentState(ctx, "one")
	if state.Status != "paused" {
		t.Fatal("control could not recover")
	}
}

func TestServiceDefaultWaitPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.EnsureAgent(ctx, "one")
	bot := &cancelBot{cancel: cancel}
	svc, err := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{{ID: "one"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled service %v", err)
	}
}

type cancelBot struct{ cancel context.CancelFunc }

func (b *cancelBot) GetUpdates(context.Context, int64, int) ([]telegram.Update, error) {
	b.cancel()
	return nil, context.Canceled
}
func (b *cancelBot) SendMessage(context.Context, int64, string, int64) (int64, error) { return 1, nil }

func TestQuestionCorrelationIncludesConfiguredOwnerChat(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.EnsureAgent(ctx, "one")
	bot := &fakeBot{}
	options := telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{{ID: "one"}}}
	svc, err := telegram.NewService(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.QueueOwner(ctx, "one", "Question for original owner?", true); err != nil {
		t.Fatal(err)
	}
	if err = svc.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	options.OwnerChatID = 88
	svc, err = telegram.NewService(options)
	if err != nil {
		t.Fatal(err)
	}
	bot.updates = []telegram.Update{{UpdateID: 1, Message: &telegram.Message{Chat: telegram.Chat{ID: 88}, Text: "Unrelated answer", ReplyToMessage: &telegram.Message{MessageID: 1}}}}
	if err = svc.Poll(ctx, 0); err != nil {
		t.Fatal(err)
	}
	answers, err := st.OwnerAnswers(ctx, "one", 10)
	if err != nil || len(answers) != 0 {
		t.Fatal("new owner chat correlated an old chat question")
	}
}
