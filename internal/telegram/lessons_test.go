package telegram_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/telegram"
)

var lessonsNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// lessonsService has ten measured posts (likes 0 to 9, ids 1 to 10) and three
// lessons: two active, one retired by expiry.
func lessonsService(t *testing.T, texts ...string) (*telegram.Service, *store.Store, *fakeBot) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err = st.EnsureAgent(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	db := st.DB()
	for i := 0; i < 10; i++ {
		posted := lessonsNow.Add(-10*time.Hour - time.Duration(10-i)*time.Minute)
		measured := posted.Add(6 * time.Hour).Format(time.RFC3339Nano)
		res, err := db.Exec(`INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url,media_id) VALUES('one','post',?,0,?,?,?)`, fmt.Sprintf("post number %d", i), posted.Format(time.RFC3339Nano), fmt.Sprintf("https://threads.test/@a/post/%d", i), fmt.Sprintf("m%d", i))
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		if _, err = db.Exec(`INSERT INTO metrics(agent_id,action_id,measured_at,views,likes,replies) VALUES('one',?,?,?,?,0)`, id, measured, 100+i, i); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`INSERT INTO metric_jobs(agent_id,action_id,window_hours,due_at,not_before,completed_at) VALUES('one',?,6,?,?,?)`, id, measured, measured, measured); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(kind, polarity, text, created, retiredAt, retiredBy string) {
		var retired any
		if retiredAt != "" {
			retired = retiredAt
		}
		if _, err = db.Exec(`INSERT INTO lessons(agent_id,kind,polarity,text,evidence_json,created_at,confirmed_at,retired_at,retired_by,retire_reason) VALUES('one',?,?,?,'[1,2]',?,?,?,?,?)`, kind, polarity, text, created, created, retired, nullIfEmpty(retiredBy), ""); err != nil {
			t.Fatal(err)
		}
	}
	old := lessonsNow.Add(-72 * time.Hour).Format(time.RFC3339Nano)
	recent := lessonsNow.Add(-2 * time.Hour).Format(time.RFC3339Nano)
	insert("reply", "do", "Answer with one concrete number.", old, "", "")
	insert("post", "avoid", "Open with a greeting.", recent, "", "")
	insert("any", "do", "Name the tool you used.", old, lessonsNow.Add(-time.Hour).Format(time.RFC3339Nano), store.RetiredByExpiry)
	bot := &fakeBot{}
	for i, text := range texts {
		bot.updates = append(bot.updates, telegram.Update{UpdateID: int64(i + 1), Message: &telegram.Message{Chat: telegram.Chat{ID: 77}, Text: text}})
	}
	svc, err := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{{ID: "one", Telegram: config.TelegramConfig{ReportHour: 21, Timezone: "UTC"}}}, Now: func() time.Time { return lessonsNow }})
	if err != nil {
		t.Fatal(err)
	}
	return svc, st, bot
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func run(t *testing.T, svc *telegram.Service, bot *fakeBot) {
	t.Helper()
	ctx := context.Background()
	if err := svc.Poll(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if err := svc.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	_ = bot
}

func TestLessonsCommandListsActiveWithUseAndRecentlyRetired(t *testing.T) {
	svc, _, bot := lessonsService(t, "/lessons")
	run(t, svc, bot)
	if len(bot.texts) != 1 {
		t.Fatalf("replies %q", bot.texts)
	}
	got := bot.texts[0]
	for _, want := range []string{
		"Lessons (2 active)",
		"#2 post, avoid\nOpen with a greeting.\nNot applied yet",
		"#1 reply, do\nAnswer with one concrete number.\nNot applied yet",
		"Recently retired\n#3 do, retired by expiry on 1 Jan: Name the tool you used.",
		"/forget <id>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "median") {
		t.Fatalf("medians shown without counts:\n%s", got)
	}
}

func TestLessonsCommandShowsMediansOnlyWithCounts(t *testing.T) {
	svc, st, bot := lessonsService(t, "/lessons")
	// Lesson 2 (post) was applied by posts 9 and 10, the two best.
	for _, id := range []int{9, 10} {
		if _, err := st.DB().Exec(`INSERT INTO action_lessons(action_id,lesson_id) VALUES(?,2)`, id); err != nil {
			t.Fatal(err)
		}
	}
	run(t, svc, bot)
	got := bot.texts[0]
	if !strings.Contains(got, "Applied 2 times, median rank 0.94 (2 ranked)\n\n") {
		t.Fatalf("use line missing:\n%s", got)
	}
}

func TestForgetRetiresLessonForGoodAndConfirms(t *testing.T) {
	svc, st, bot := lessonsService(t, "/forget 2", "/forget 2", "/forget x", "/forget 99")
	run(t, svc, bot)
	if len(bot.texts) != 4 {
		t.Fatalf("replies %q", bot.texts)
	}
	for i, want := range []string{
		"Lesson #2 forgotten\nThe agent will not add it again.",
		"Lesson not forgotten\nThere is no active lesson #2.",
		"Lesson not forgotten\nThe id must be a number from /lessons.",
		"Lesson not forgotten\nThere is no active lesson #99.",
	} {
		if !strings.HasSuffix(bot.texts[i], want) {
			t.Fatalf("reply %d = %q, want suffix %q", i, bot.texts[i], want)
		}
	}
	var by string
	if err := st.DB().QueryRow(`SELECT retired_by FROM lessons WHERE id=2`).Scan(&by); err != nil || by != store.RetiredByOwner {
		t.Fatalf("retired_by=%q %v", by, err)
	}
	// The tombstone refuses the same text from the dream.
	_, err := st.AddLesson(context.Background(), "one", "post", "avoid", "Open with a greeting.", []int64{1, 2}, lessonsNow)
	var refusal *store.LessonRefusal
	if err == nil || !errors.As(err, &refusal) || refusal.Reason != "lesson_forgotten" {
		t.Fatalf("tombstone: %v", err)
	}
}

func TestDailyReportNamesEpisodesAndLessonChanges(t *testing.T) {
	svc, _, bot := lessonsService(t, "/report")
	run(t, svc, bot)
	got := bot.texts[0]
	for _, want := range []string{
		"Best episodes\n#10 post at 6h, rank 1.00 of 10: 109 views, 9 likes, 0 replies. post number 9",
		"#9 post at 6h, rank 0.89 of 10",
		"Weakest episodes\n#1 post at 6h, rank 0.00 of 10",
		"Lessons: 1 added, 0 confirmed, 1 retired",
		"Added #2 post, avoid: Open with a greeting.",
		"Retired #3 by expiry: Name the tool you used.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("report lacks %q:\n%s", want, got)
		}
	}
	for _, gone := range []string{"outcome", "label", "playbook", "strategy"} {
		if strings.Contains(strings.ToLower(got), gone) {
			t.Fatalf("report mentions %q:\n%s", gone, got)
		}
	}
}

func TestLessonsCommandFitsOneTelegramMessageWhenFull(t *testing.T) {
	svc, st, bot := lessonsService(t, "/lessons")
	long := strings.Repeat("ồ ", 120)
	for i := 0; i < 15; i++ {
		var retired any
		if i >= 10 {
			retired = lessonsNow.Add(-time.Minute).Format(time.RFC3339Nano)
		}
		if _, err := st.DB().Exec(`INSERT INTO lessons(agent_id,kind,polarity,text,evidence_json,created_at,confirmed_at,retired_at,retired_by,retire_reason) VALUES('one','reply','do',?,'[1,2]',?,?,?,?,'')`, fmt.Sprintf("%d %s", i, long), lessonsNow.Format(time.RFC3339Nano), lessonsNow.Format(time.RFC3339Nano), retired, nullIfEmpty(map[bool]string{true: store.RetiredByDream}[retired != nil])); err != nil {
			t.Fatal(err)
		}
	}
	run(t, svc, bot)
	if len(bot.texts) != 1 {
		t.Fatalf("replies %d, want 1", len(bot.texts))
	}
	if n := len([]rune(bot.texts[0])); n > 4096 {
		t.Fatalf("message is %d runes", n)
	}
}
