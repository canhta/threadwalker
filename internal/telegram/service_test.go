package telegram_test

import (
	"bytes"
	"context"
	"errors"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/telegram"
	"strings"
	"testing"
	"time"
)

type fakeBot struct {
	pollErr, sendErr, commandsErr error
	updates                       []telegram.Update
	texts                         []string
	commands                      []telegram.BotCommand
	deleted                       []int64
	deleteErr                     error
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
func (b *fakeBot) SetMyCommands(_ context.Context, commands []telegram.BotCommand) error {
	b.commands = append([]telegram.BotCommand(nil), commands...)
	return b.commandsErr
}

// scriptedPollBot returns one scripted result per GetUpdates call, so a test can
// exercise a transient failure followed by a successful poll deterministically.
type scriptedPollBot struct {
	polls   int
	errors  []error
	updates []telegram.Update
	texts   []string
}

func (b *scriptedPollBot) GetUpdates(context.Context, int64, int) ([]telegram.Update, error) {
	index := b.polls
	b.polls++
	if index < len(b.errors) && b.errors[index] != nil {
		return nil, b.errors[index]
	}
	updates := b.updates
	b.updates = nil
	return updates, nil
}

func (b *scriptedPollBot) SendMessage(_ context.Context, _ int64, text string, _ int64) (int64, error) {
	b.texts = append(b.texts, text)
	return int64(len(b.texts)), nil
}

func (b *scriptedPollBot) SetMyCommands(context.Context, []telegram.BotCommand) error { return nil }

// A transient getUpdates failure is retried by the service loop. No update is
// lost because the persisted offset is advanced only after a successful poll,
// and both failure events stay at info level instead of becoming noisy ERRORs.
func TestServiceTransientPollFailureRetriesWithoutLosingUpdates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.EnsureAgent(ctx, "one")
	bot := &scriptedPollBot{
		// The first call is the transient failure; the third ends the loop so
		// Run reaches its wait path a second time.
		errors:  []error{errors.New("synthetic transient dial failure"), nil, errors.New("synthetic shutdown")},
		updates: []telegram.Update{{UpdateID: 7, Message: &telegram.Message{Chat: telegram.Chat{ID: 77}, Text: "/pause"}}},
	}
	var logs bytes.Buffer
	logger, _ := logging.New(&logs, "debug", "json")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	waits := 0
	svc, err := telegram.NewService(telegram.ServiceOptions{
		Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77,
		Templates: []config.Template{{ID: "one", Telegram: config.TelegramConfig{ReportHour: 23}}},
		Now:       func() time.Time { return now },
		Logger:    logger,
		Wait: func(context.Context, time.Duration) error {
			waits++
			if waits == 1 {
				return nil
			}
			cancel()
			return context.Canceled
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("service run=%v", err)
	}
	if bot.polls != 3 {
		t.Fatalf("poll attempts=%d, want transient failure then successful retry", bot.polls)
	}
	offset, err := st.OwnerOffset(context.Background(), "one", "fake")
	if err != nil || offset != 8 {
		t.Fatalf("offset=%d err=%v, want 8 with no lost update", offset, err)
	}
	state, err := st.AgentState(context.Background(), "one")
	if err != nil || state.Status != "paused" {
		t.Fatalf("owner command applied=%+v err=%v", state, err)
	}
	// One failure for the transient call, one for the synthetic shutdown.
	if got := strings.Count(logs.String(), `"reason":"poll_failed"`); got != 2 {
		t.Fatalf("poll_failed events=%d", got)
	}
	if !strings.Contains(logs.String(), `"reason":"iteration_failed"`) {
		t.Fatalf("missing iteration event: %s", logs.String())
	}
	if strings.Contains(logs.String(), `"level":"ERROR"`) {
		t.Fatalf("transient poll failure logged as ERROR: %s", logs.String())
	}
	if strings.Contains(logs.String(), "synthetic transient dial failure") {
		t.Fatalf("transport detail leaked: %s", logs.String())
	}
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
	// 18:00 UTC is 01:00 the next day in the default owner zone, past report hour 0.
	tmpl := config.Template{ID: "one", Telegram: config.TelegramConfig{ReportHour: 0}, Limits: config.LimitsConfig{PostsPerDay: 1, RepliesPerDay: 1, MaxUSDPerDay: 1}}
	makeService := func() *telegram.Service {
		svc, err := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{tmpl}, Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		return svc
	}
	svc := makeService()
	if _, err = st.DB().Exec(`INSERT INTO actions(id,agent_id,kind,text,posted_at,dry_run) VALUES(1,'one','post','synthetic','2026-01-01T12:00:00Z',0);INSERT INTO ledger_phases(agent_id,day,phase,tokens_in,tokens_out,cost_usd) VALUES('one','2026-01-01','agent',10,20,1);INSERT INTO metrics(agent_id,action_id,measured_at,views,likes,replies,reposts) VALUES('one',1,'2026-01-01T13:00:00Z',5,1,1,1),('one',1,'2026-01-01T17:00:00Z',10,2,3,4);INSERT INTO playbook(agent_id,text,status,created_at) VALUES('one','synthetic','testing','2026-01-01T13:00:00Z')`); err != nil {
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
		if strings.HasPrefix(text, "Daily report, 2 Jan 2026\n") {
			found = true
			for _, part := range []string{"Posts: 1 of 1", "Replies: 0 of 1", "Spend: $1.00 of $1.00 (100%)", "Metrics (1 measured today): 10 views, 2 likes, 3 replies, 4 reposts", "Playbook changes: 1"} {
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
		o.Templates = []config.Template{{ID: "one", Telegram: config.TelegramConfig{ReportHour: 24}}}
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
func (b *cancelBot) SetMyCommands(context.Context, []telegram.BotCommand) error       { return nil }

type menuBot struct {
	commands []telegram.BotCommand
	err      error
	calls    int
	polls    int
	cancel   context.CancelFunc
}

func (b *menuBot) GetUpdates(context.Context, int64, int) ([]telegram.Update, error) {
	b.polls++
	b.cancel()
	return nil, context.Canceled
}
func (b *menuBot) SendMessage(context.Context, int64, string, int64) (int64, error) { return 1, nil }
func (b *menuBot) SetMyCommands(_ context.Context, commands []telegram.BotCommand) error {
	b.calls++
	b.commands = append([]telegram.BotCommand(nil), commands...)
	return b.err
}

func TestDailyReportOncePerLocalDayNamesEachAgent(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, id := range []string{"one", "two"} {
		if err = st.EnsureAgent(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	// 06:00 local in UTC+7 is 23:00 UTC the day before: the report is due by
	// the local clock and keyed by the local date, so it is sent once.
	now := time.Date(2025, 12, 31, 22, 0, 0, 0, time.UTC)
	one := config.Template{ID: "one", Persona: config.PersonaConfig{Name: "Kaito", Language: "vi"}, Telegram: config.TelegramConfig{ReportHour: 6, Timezone: "Asia/Ho_Chi_Minh"}, Limits: config.LimitsConfig{PostsPerDay: 3}}
	two := config.Template{ID: "two", Persona: config.PersonaConfig{Name: "Professor"}, Telegram: config.TelegramConfig{ReportHour: 23, Timezone: "UTC"}}
	bot := &fakeBot{}
	svc, err := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{one, two}, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	tick := func() {
		t.Helper()
		if err = svc.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if err = svc.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}
	tick()
	if len(bot.texts) != 0 {
		t.Fatalf("report before 06:00 local: %v", bot.texts)
	}
	now = now.Add(time.Hour)
	tick()
	if len(bot.texts) != 2 || !strings.HasPrefix(bot.texts[0], "Kaito · Daily report, 1 Jan 2026\n") || !strings.Contains(bot.texts[0], "Posts: 0 of 3") || !strings.HasPrefix(bot.texts[1], "Professor · Daily report, 31 Dec 2025\n") {
		t.Fatalf("reports %q", bot.texts)
	}
	now = now.Add(2 * time.Hour)
	tick()
	if len(bot.texts) != 2 {
		t.Fatalf("report repeated after UTC midnight: %v", bot.texts)
	}
}

func TestReportCommandAnswersEachRequest(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.EnsureAgent(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	bot := &fakeBot{updates: []telegram.Update{
		{UpdateID: 1, Message: &telegram.Message{Chat: telegram.Chat{ID: 77}, Text: "/report"}},
		{UpdateID: 2, Message: &telegram.Message{Chat: telegram.Chat{ID: 77}, Text: "/report one"}},
		{UpdateID: 3, Message: &telegram.Message{Chat: telegram.Chat{ID: 77}, Text: "/report one extra"}},
	}}
	now := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	tmpl := config.Template{ID: "one", Telegram: config.TelegramConfig{ReportHour: 21}}
	svc, err := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{tmpl}, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Poll(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if err = svc.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(bot.texts) != 2 || bot.texts[0] != bot.texts[1] || !strings.HasPrefix(bot.texts[0], "Daily report, 1 Jan 2026\nPeriod: 1 Jan 07:00 to 1 Jan 10:00 UTC+07") {
		t.Fatalf("report replies %q", bot.texts)
	}
	if state, err := st.AgentState(ctx, "one"); err != nil || state.Status != "running" {
		t.Fatalf("/report changed state: %+v %v", state, err)
	}
}

func TestHelpAndFreeTextNotes(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.EnsureAgent(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	bot := &fakeBot{updates: []telegram.Update{
		{UpdateID: 1, Message: &telegram.Message{Chat: telegram.Chat{ID: 77}, Text: "/help"}},
		{UpdateID: 2, Message: &telegram.Message{Chat: telegram.Chat{ID: 77}, Text: "nhớ giữ giọng cà khịa nha"}},
		{UpdateID: 3, Message: &telegram.Message{Chat: telegram.Chat{ID: 77}, Text: "/start"}},
	}}
	tmpl := config.Template{ID: "one", Persona: config.PersonaConfig{Language: "vi"}}
	svc, err := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{tmpl}})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Poll(ctx, 0); err != nil {
		t.Fatal(err)
	}
	notes, err := st.OwnerAnswers(ctx, "one", 10)
	if err != nil || len(notes) != 1 || notes[0].Kind != "note" || notes[0].Text != "nhớ giữ giọng cà khịa nha" {
		t.Fatalf("owner note %+v %v", notes, err)
	}
	state, err := st.AgentState(ctx, "one")
	if err != nil || state.Status != "running" {
		t.Fatalf("free text changed state: %+v %v", state, err)
	}
	if err = svc.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(bot.texts) != 3 {
		t.Fatalf("replies %v", bot.texts)
	}
	if !strings.Contains(bot.texts[0], "/status") || !strings.Contains(bot.texts[0], "/dnc <username>") {
		t.Fatalf("help missing commands: %q", bot.texts[0])
	}
	if bot.texts[1] != "Note saved\nThe agent reads it on its next run." {
		t.Fatalf("note acknowledgement missing: %q", bot.texts[1])
	}
	if !strings.HasPrefix(bot.texts[2], "Threadz owner console\n") || !strings.HasSuffix(bot.texts[2], bot.texts[0]) {
		t.Fatalf("/start differs from /help: %q vs %q", bot.texts[0], bot.texts[2])
	}
}

func TestStatusUsesOwnerTimezoneAndRelativeTime(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.EnsureAgent(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec(`UPDATE agents SET next_wake_at='2026-01-01T07:33:00Z' WHERE id='one'`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 7, 15, 0, 0, time.UTC)
	for i, tc := range []struct{ name, zone, want string }{
		{"default owner zone", "", "Next run: 14:33 (in 18 min)"},
		{"configured zone", "UTC", "Next run: 07:33 (in 18 min)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bot := &fakeBot{}
			tmpl := config.Template{ID: "one", Persona: config.PersonaConfig{Language: "vi"}, Telegram: config.TelegramConfig{Timezone: tc.zone}, Limits: config.LimitsConfig{MaxUSDPerDay: 0.5}}
			bot.updates = []telegram.Update{{UpdateID: int64(i + 1), Message: &telegram.Message{Chat: telegram.Chat{ID: 77}, Text: "/status"}}}
			svc, err := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{tmpl}, Now: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			if err = svc.Poll(ctx, 0); err != nil {
				t.Fatal(err)
			}
			if err = svc.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			if len(bot.texts) != 1 || !strings.Contains(bot.texts[0], tc.want) {
				t.Fatalf("status %v, want %q", bot.texts, tc.want)
			}
			if !strings.Contains(bot.texts[0], "Spend today: $0.00 of $0.50") {
				t.Fatalf("status omitted budget: %q", bot.texts[0])
			}
		})
	}
}

func TestServiceRegistersMenuAtStartAndSurvivesFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		ids    []string
		reason string
	}{
		{"single_agent", nil, []string{"one"}, "menu_registered"},
		{"registration_failure", errors.New("synthetic private failure"), []string{"one"}, "menu_failed"},
		{"multi_agent", nil, []string{"one", "two"}, "menu_registered"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			st, err := store.Open(ctx, ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			templates := []config.Template{}
			for _, id := range tc.ids {
				templates = append(templates, config.Template{ID: id, Telegram: config.TelegramConfig{ReportHour: 18}})
				if err = st.EnsureAgent(ctx, id); err != nil {
					t.Fatal(err)
				}
			}
			var logs bytes.Buffer
			logger, _ := logging.New(&logs, "debug", "json")
			bot := &menuBot{err: tc.err, cancel: cancel}
			svc, err := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: templates, Now: func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }, Logger: logger})
			if err != nil {
				t.Fatal(err)
			}
			if err = svc.Run(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("service cancellation %v", err)
			}
			if bot.calls != 1 {
				t.Fatalf("menu registrations = %d, want one per start", bot.calls)
			}
			if bot.polls != 1 {
				t.Fatal("menu outcome stopped the owner poller")
			}
			if !strings.Contains(logs.String(), tc.reason) {
				t.Fatalf("missing reason %s", tc.reason)
			}
			if strings.Contains(logs.String(), "synthetic private failure") {
				t.Fatal("menu failure text leaked into logs")
			}
			names := []string{}
			for _, command := range bot.commands {
				names = append(names, command.Command)
			}
			if strings.Join(names, ",") != "status,report,pause,resume,kill,dnc,restore,cookie,update,help,start" {
				t.Fatalf("menu = %v, want the handled commands", names)
			}
			dnc := ""
			for _, command := range bot.commands {
				if command.Command == "dnc" {
					dnc = command.Description
				}
			}
			if len(tc.ids) > 1 && !strings.Contains(dnc, "agent ID") {
				t.Fatalf("multi-agent menu omits the agent-id form: %q", dnc)
			}
			if len(tc.ids) == 1 && strings.Contains(dnc, "agent ID") {
				t.Fatalf("single-agent menu documents an unused agent-id form: %q", dnc)
			}
		})
	}
}

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

func TestOwnerAnswersRedactOtherConfiguredAgentSecretsBeforePersistence(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.EnsureAgent(ctx, "one")
	bot := &fakeBot{}
	svc, err := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{{ID: "one"}}, Redactor: config.NewRedactor(config.NewSecret("another-agent-key"))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.QueueOwner(ctx, "one", "Question?", true); err != nil {
		t.Fatal(err)
	}
	if err = svc.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	bot.updates = []telegram.Update{{UpdateID: 1, Message: &telegram.Message{Chat: telegram.Chat{ID: 77}, Text: "Answer another-agent-key", ReplyToMessage: &telegram.Message{MessageID: 1}}}}
	if err = svc.Poll(ctx, 0); err != nil {
		t.Fatal(err)
	}
	answers, err := st.OwnerAnswers(ctx, "one", 10)
	if err != nil || len(answers) != 1 || answers[0].Text != "Answer [REDACTED]" {
		t.Fatalf("cross-agent configured secret persisted %+v %v", answers, err)
	}
}

func TestUpdateCommandRequestsUpdateFromOwnerOnly(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.EnsureAgent(ctx, "one")
	requests := 0
	bot := &fakeBot{}
	svc, err := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{{ID: "one"}}, RequestUpdate: func() error { requests++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	for i, chat := range []int64{99, 77} {
		bot.updates = []telegram.Update{{UpdateID: int64(i + 1), Message: &telegram.Message{Chat: telegram.Chat{ID: chat}, Text: "/update"}}}
		if err = svc.Poll(ctx, 0); err != nil {
			t.Fatal(err)
		}
	}
	if requests != 1 {
		t.Fatalf("update requests = %d, want 1 (owner only)", requests)
	}
}

func (b *fakeBot) DeleteMessage(_ context.Context, _, messageID int64) error {
	b.deleted = append(b.deleted, messageID)
	return b.deleteErr
}
func (b *scriptedPollBot) DeleteMessage(context.Context, int64, int64) error { return nil }
func (b *cancelBot) DeleteMessage(context.Context, int64, int64) error       { return nil }
func (b *menuBot) DeleteMessage(context.Context, int64, int64) error         { return nil }
