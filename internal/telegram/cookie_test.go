package telegram_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/telegram"
)

const syntheticSession = "synthetic-session-value"

type cookieRig struct {
	st     *store.Store
	bot    *fakeBot
	svc    *telegram.Service
	logs   *bytes.Buffer
	gotRaw []string
	status string
}

func newCookieRig(t *testing.T, redactor *config.Redactor, texts ...string) *cookieRig {
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
	if err = st.PauseOwnerAgent(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	r := &cookieRig{st: st, bot: &fakeBot{}, logs: &bytes.Buffer{}, status: "ok"}
	for i, text := range texts {
		r.bot.updates = append(r.bot.updates, telegram.Update{UpdateID: int64(i + 1), Message: &telegram.Message{MessageID: int64(100 + i), Chat: telegram.Chat{ID: 77}, Text: text}})
	}
	logger := slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r.svc, err = telegram.NewService(telegram.ServiceOptions{Store: st, Bot: r.bot, BotKey: "fake", OwnerChatID: 77, Redactor: redactor, Logger: logger, Templates: []config.Template{{ID: "one", Persona: config.PersonaConfig{Language: "en"}}}})
	if err != nil {
		t.Fatal(err)
	}
	r.svc.SetCookieImport(func(_ context.Context, id, raw string) string {
		r.gotRaw = append(r.gotRaw, raw)
		if r.status == "ok" {
			// The importer resumes the agent; BrowserRestored repeats it harmlessly.
			_ = st.ResumePausedAgent(ctx, id)
		}
		return r.status
	})
	return r
}

func (r *cookieRig) run(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if err := r.svc.Poll(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Flush(ctx); err != nil {
		t.Fatal(err)
	}
}

// nothingKept proves the cookie text is in no note, no sent message and no log.
func (r *cookieRig) nothingKept(t *testing.T) {
	t.Helper()
	notes, err := r.st.OpenOwnerNotes(context.Background(), "one", time.Now())
	if err != nil || len(notes) != 0 {
		t.Fatalf("notes %v %v", notes, err)
	}
	for _, s := range append([]string{r.logs.String()}, r.bot.texts...) {
		if strings.Contains(s, syntheticSession) {
			t.Fatalf("cookie value leaked: %q", s)
		}
	}
	if r.logs.Len() == 0 {
		t.Fatal("nothing logged, so the check proves nothing")
	}
}

func TestCookieCommandImportsDeletesAndAnswersOnce(t *testing.T) {
	r := newCookieRig(t, nil, "/cookie sessionid="+syntheticSession+"; ds_user_id=1")
	r.run(t)
	state, _ := r.st.AgentState(context.Background(), "one")
	if len(r.bot.deleted) != 1 || r.bot.deleted[0] != 100 {
		t.Fatalf("deleted %v", r.bot.deleted)
	}
	if len(r.gotRaw) != 1 || r.gotRaw[0] != "sessionid="+syntheticSession+"; ds_user_id=1" {
		t.Fatalf("import got %q", r.gotRaw)
	}
	if state.Status != "running" || len(r.bot.texts) != 1 || !strings.Contains(r.bot.texts[0], "signed in again. The agent has resumed.") {
		t.Fatalf("state %s texts %q", state.Status, r.bot.texts)
	}
	r.nothingKept(t)
}

func TestCookieExportWithoutThePrefixIsStillACookieImport(t *testing.T) {
	export := `[{"domain":".threads.com","name":"sessionid","value":"` + syntheticSession + `"}]`
	r := newCookieRig(t, nil, export)
	r.run(t)
	if len(r.bot.deleted) != 1 || len(r.gotRaw) != 1 || r.gotRaw[0] != export {
		t.Fatalf("deleted %v import %q", r.bot.deleted, r.gotRaw)
	}
	r.nothingKept(t)
	// A Cookie header pasted as plain text too.
	r = newCookieRig(t, nil, "Cookie: sessionid="+syntheticSession+"; ds_user_id=1")
	r.run(t)
	if len(r.bot.deleted) != 1 || len(r.gotRaw) != 1 {
		t.Fatalf("header not imported: %v %q", r.bot.deleted, r.gotRaw)
	}
	r.nothingKept(t)
}

func TestFailedCookieImportIsDeletedAndAnsweredOnce(t *testing.T) {
	for status, want := range map[string]string{"missing_sessionid": "missing sessionid", "logged_out": "still shows the session as logged out", "busy": "using the browser"} {
		r := newCookieRig(t, nil, "/cookie sessionid="+syntheticSession)
		r.status = status
		r.run(t)
		state, _ := r.st.AgentState(context.Background(), "one")
		if len(r.bot.deleted) != 1 || state.Status != "paused" || len(r.bot.texts) != 1 || !strings.Contains(r.bot.texts[0], want) {
			t.Fatalf("%s: deleted %v state %s texts %q", status, r.bot.deleted, state.Status, r.bot.texts)
		}
		r.nothingKept(t)
	}
}

func TestCookieMessageIsDeletedEvenWhenDeleteFails(t *testing.T) {
	r := newCookieRig(t, nil, "/cookie sessionid="+syntheticSession)
	r.bot.deleteErr = context.DeadlineExceeded
	r.run(t)
	if len(r.bot.deleted) != 1 || len(r.gotRaw) != 1 {
		t.Fatalf("delete attempted %v, import %q", r.bot.deleted, r.gotRaw)
	}
}

func TestNoteWithAConfiguredSecretIsRefused(t *testing.T) {
	const secret = "synthetic-configured-secret"
	r := newCookieRig(t, config.NewRedactor(config.NewSecret(secret)), "remember "+secret+" for later", "a normal note")
	r.run(t)
	notes, err := r.st.OpenOwnerNotes(context.Background(), "one", time.Now())
	if err != nil || len(notes) != 1 || notes[0].Text != "a normal note" {
		t.Fatalf("notes %+v %v", notes, err)
	}
	refused := 0
	for _, s := range r.bot.texts {
		if strings.Contains(s, secret) {
			t.Fatalf("secret echoed: %q", s)
		}
		if strings.Contains(s, "looked like a credential") {
			refused++
		}
	}
	if refused != 1 {
		t.Fatalf("texts %q", r.bot.texts)
	}
}

func TestLoggedOutNoticeSaysHowToSendCookies(t *testing.T) {
	r := newCookieRig(t, nil)
	if err := r.svc.BrowserStopped(context.Background(), "one", "logged_out", "wake-1"); err != nil {
		t.Fatal(err)
	}
	r.run(t)
	if len(r.bot.texts) != 1 || !strings.Contains(r.bot.texts[0], "/cookie") || !strings.Contains(r.bot.texts[0], "Cookie-Editor") || !strings.Contains(r.bot.texts[0], "Cookie header") {
		t.Fatalf("notice %q", r.bot.texts)
	}
}

func TestBrowserRestoredResumesAndTellsOnce(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.EnsureAgent(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	if err = st.PauseOwnerAgent(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	bot := &fakeBot{}
	svc, err := telegram.NewService(telegram.ServiceOptions{Store: st, Bot: bot, BotKey: "fake", OwnerChatID: 77, Templates: []config.Template{{ID: "one", Persona: config.PersonaConfig{Language: "vi"}}}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = svc.BrowserRestored(ctx, "one", "login-1"); err != nil {
			t.Fatal(err)
		}
	}
	if err = svc.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := st.AgentState(ctx, "one")
	if err != nil || state.Status != "running" || len(bot.texts) != 1 || !strings.Contains(bot.texts[0], "signed in again") {
		t.Fatalf("state %+v texts %q", state, bot.texts)
	}
}
