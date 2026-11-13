package telegram_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/telegram"
)

func TestSilentAgentNoticeOncePerDayAndDigestFailureLine(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.EnsureAgent(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	if _, err = st.DB().Exec(`INSERT INTO actions(agent_id,kind,text,posted_at,dry_run) VALUES('one','post','x','2026-01-02T00:00:00Z',0);
		INSERT INTO wakes(agent_id,started_at,ended_at,end_reason) VALUES('one','2026-01-02T08:00:00Z','2026-01-02T08:01:00Z','step_limit');
		INSERT INTO tool_calls(agent_id,wake_id,tool,args_json,blocked_by,at) VALUES('one',1,'post','{}','blocked_phrase','2026-01-02T08:00:30Z')`); err != nil {
		t.Fatal(err)
	}
	tmpl := config.Template{ID: "one", Telegram: config.TelegramConfig{DigestHour: 23}, KPI: config.KPIConfig{EngagementsPerDay: 1, MaxHoursWithoutPublish: 8}}
	bot := &fakeBot{}
	svc, err := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{tmpl}, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = svc.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err = svc.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(bot.texts) != 1 || bot.texts[0] != "no post or reply for 10 hours; last wake ended step_limit; top refusal reasons today: blocked_phrase ×1, step_limit ×1" {
		t.Fatalf("texts=%q", bot.texts)
	}
	now = now.Add(24 * time.Hour)
	if err = svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err = svc.Flush(ctx); err != nil || len(bot.texts) != 2 {
		t.Fatalf("next day: err=%v texts=%q", err, bot.texts)
	}
	// Within the limit, a paused agent, or no kpi block: silent.
	for name, mutate := range map[string]func(){
		"within limit": func() {
			now = time.Date(2026, 1, 2, 5, 0, 0, 0, time.UTC).Add(48 * time.Hour)
			tmpl.KPI.MaxHoursWithoutPublish = 100
		},
		"paused": func() { _, _ = st.DB().Exec(`UPDATE agents SET status='paused'`); tmpl.KPI.MaxHoursWithoutPublish = 8 },
	} {
		mutate()
		quiet, _ := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{tmpl}, Now: func() time.Time { return now }})
		before := len(bot.texts)
		if err = quiet.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		_ = quiet.Flush(ctx)
		if len(bot.texts) != before {
			t.Fatalf("%s: sent %q", name, bot.texts[before:])
		}
	}

	digestTmpl := config.Template{ID: "one", Telegram: config.TelegramConfig{DigestHour: 0}}
	now = time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	digest, _ := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{digestTmpl}, Now: func() time.Time { return now }})
	if err = digest.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err = digest.Flush(ctx); err != nil || !strings.Contains(bot.texts[len(bot.texts)-1], "failed wakes: 1 · top refusal reasons: blocked_phrase ×1, step_limit ×1") {
		t.Fatalf("digest=%q err=%v", bot.texts[len(bot.texts)-1], err)
	}
}

func TestRestoreListsAndRestoresMemoryAndStrategy(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.EnsureAgent(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	if _, err = st.DB().Exec(`INSERT INTO notes(agent_id,text,created_at) VALUES('one','n','2026-01-01T00:00:00Z');
		INSERT INTO memory_journal(agent_id,entry_id,change,old_text,created_at) VALUES('one',1,'overflow','old fact','2026-01-01T00:00:00Z'),('one',2,'added','',      '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"first", "second", "third"} {
		if _, err = st.WriteStrategy(ctx, "one", text, []string{"note:1"}, at); err != nil {
			t.Fatal(err)
		}
	}
	bot := &fakeBot{}
	svc, err := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{{ID: "one"}}, Now: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	send := func(chat int64, id int64, text string) string {
		before := len(bot.texts)
		bot.updates = []telegram.Update{{UpdateID: id, Message: &telegram.Message{Chat: telegram.Chat{ID: chat}, Text: text}}}
		if err = svc.Poll(ctx, 0); err != nil {
			t.Fatal(err)
		}
		if err = svc.Flush(ctx); err != nil {
			t.Fatal(err)
		}
		if len(bot.texts) == before {
			return ""
		}
		return bot.texts[len(bot.texts)-1]
	}
	list := send(77, 1, "/restore")
	for _, want := range []string{"m1 old fact", "s2 second", "s1 first"} {
		if !strings.Contains(list, want) {
			t.Fatalf("list missing %q: %s", want, list)
		}
	}
	if strings.Contains(list, "s3") || strings.Contains(list, "m2") {
		t.Fatalf("live or non-archive entry listed: %s", list)
	}
	if got := send(66, 2, "/restore s1"); got != "" {
		t.Fatalf("stranger answered: %s", got)
	}
	if got := send(77, 3, "/restore m1"); got != "restored" {
		t.Fatalf("memory: %s", got)
	}
	if mem, err := st.MemoryIndex(ctx, "one"); err != nil || len(mem) != 1 || mem[0].Text != "old fact" {
		t.Fatalf("memory=%+v err=%v", mem, err)
	}
	if got := send(77, 4, "/restore s1"); got != "restored" {
		t.Fatalf("strategy: %s", got)
	}
	if h, err := st.StrategyHistory(ctx, "one"); err != nil || len(h) != 4 || h[3].Text != "first" {
		t.Fatalf("history=%+v err=%v", h, err)
	}
	for i, bad := range []string{"/restore x1", "/restore m", "/restore m0", "/restore s9", "/restore m99", "/restore s1 s2"} {
		if got := send(77, int64(10+i), bad); !strings.HasPrefix(got, "could not") && !strings.HasPrefix(got, "send /restore") {
			t.Fatalf("%s: %s", bad, got)
		}
	}
}
