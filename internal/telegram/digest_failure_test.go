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

func TestDigestFailureLine(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.EnsureAgent(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec(`INSERT INTO actions(agent_id,kind,text,posted_at,dry_run) VALUES('one','post','x','2026-01-02T00:00:00Z',0);
		INSERT INTO wakes(agent_id,started_at,ended_at,end_reason) VALUES('one','2026-01-02T08:00:00Z','2026-01-02T08:01:00Z','step_limit');
		INSERT INTO tool_calls(agent_id,wake_id,tool,args_json,blocked_by,at) VALUES('one',1,'post','{}','blocked_phrase','2026-01-02T08:00:30Z')`); err != nil {
		t.Fatal(err)
	}
	bot := &fakeBot{}
	digestTmpl := config.Template{ID: "one", Telegram: config.TelegramConfig{ReportHour: 0}}
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	digest, _ := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{digestTmpl}, Now: func() time.Time { return now }})
	if err = digest.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err = digest.Flush(ctx); err != nil || !strings.Contains(bot.texts[len(bot.texts)-1], "Failed runs: 1 (step_limit ×1)\nBlocked actions: blocked_phrase ×1") {
		t.Fatalf("digest=%q err=%v", bot.texts[len(bot.texts)-1], err)
	}
}
