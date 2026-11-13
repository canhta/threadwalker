// Package telegram owns owner control, a durable outbox and Bot API polling.
// It does not import tools or agent; model calls can only enqueue local messages.
package telegram

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/update"
)

const defaultOwnerTimezone = "Asia/Ho_Chi_Minh"

type BotAPI interface {
	GetUpdates(context.Context, int64, int) ([]Update, error)
	SendMessage(context.Context, int64, string, int64) (int64, error)
	SetMyCommands(context.Context, []BotCommand) error
	// DeleteMessage removes a message from the owner's chat.
	DeleteMessage(ctx context.Context, chatID, messageID int64) error
}
type ServiceOptions struct {
	Redactor *config.Redactor
	Store    *store.Store
	Bot      BotAPI
	// BotKey is a one-way digest of bot identity, never the credential itself.
	BotKey      string
	OwnerChatID int64
	Templates   []config.Template
	Now         func() time.Time
	Wait        func(context.Context, time.Duration) error
	Logger      *slog.Logger
	// RequestUpdate asks the root updater to run; it defaults to update.RequestFile.
	RequestUpdate func() error
}
type Service struct {
	opts         ServiceOptions
	mu           sync.Mutex
	templates    map[string]config.Template
	ids          []string
	locations    map[string]*time.Location
	redactor     *config.Redactor
	sequence     atomic.Int64
	deliveryKey  string
	cookieImport func(ctx context.Context, agentID, raw string) string
}

// SetCookieImport installs the cookie import behind /cookie. It returns a
// status such as "ok" or "logged_out" (see CookieResultText). Call it before Run.
func (s *Service) SetCookieImport(f func(ctx context.Context, agentID, raw string) string) {
	s.cookieImport = f
}

func NewService(opts ServiceOptions) (*Service, error) {
	if opts.Store == nil || opts.Bot == nil || opts.BotKey == "" || opts.OwnerChatID == 0 || len(opts.Templates) == 0 {
		return nil, errors.New("configuring Telegram service: store, bot, identity and agents required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Wait == nil {
		opts.Wait = wait
	}
	if opts.RequestUpdate == nil {
		opts.RequestUpdate = update.RequestFile
	}
	scope := sha256.Sum256([]byte(opts.BotKey + "\x00" + strconv.FormatInt(opts.OwnerChatID, 10)))
	s := &Service{opts: opts, templates: map[string]config.Template{}, locations: map[string]*time.Location{}, deliveryKey: hex.EncodeToString(scope[:])}
	for _, t := range opts.Templates {
		if t.ID == "" || s.templates[t.ID].ID != "" || t.Telegram.DigestHour < 0 || t.Telegram.DigestHour > 23 {
			return nil, errors.New("configuring Telegram service: invalid agent or digest hour")
		}
		zone := strings.TrimSpace(t.Telegram.Timezone)
		if zone == "" {
			zone = defaultOwnerTimezone
		}
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return nil, errors.New("configuring Telegram service: invalid owner timezone")
		}
		s.locations[t.ID] = loc
		s.templates[t.ID] = t
		s.ids = append(s.ids, t.ID)
	}
	s.redactor = config.NewRedactor(opts.Redactor, opts.Templates)
	sort.Strings(s.ids)
	return s, nil
}
func (s *Service) redact(text string) string { return s.redactor.String(text) }
func (s *Service) event(ctx context.Context, id, op, outcome, reason string, correlation int64) {
	localID := fmt.Sprintf("owner-message/%d", correlation)
	if correlation == 0 {
		localID = fmt.Sprintf("owner-operation/%d", s.sequence.Add(1))
	}
	logging.Emit(ctx, s.opts.Logger, slog.LevelInfo, logging.Record{Event: logging.TelegramOperationFinished, AgentID: id, Operation: op, Outcome: outcome, Reason: reason, ToolCallID: localID})
}

// QueueOwner returns after SQLite persistence, without waiting for polling,
// Telegram delivery or an answer. Disabled agents have no service binding.
func (s *Service) QueueOwner(ctx context.Context, id, text string, question bool) (int64, error) {
	if _, ok := s.templates[id]; !ok {
		return 0, errors.New("queueing owner message: unconfigured agent")
	}
	if utf8.RuneCountInString(text) > 3500 {
		return 0, errors.New("queueing owner message: text too long")
	}
	kind := "notification"
	if question {
		kind = "question"
	}
	n, err := s.opts.Store.QueueOwner(ctx, id, kind, s.redact(text), "", s.opts.Now())
	outcome, reason := "queued", "persisted"
	if err != nil {
		outcome, reason = "error", "queue_failed"
	}
	s.event(ctx, id, kind, outcome, reason, n)
	return n, err
}

// Poll serializes offset processing for a bot. Commands/replies are persisted
// before advancing the offset; command receipts and answers are idempotent.
func (s *Service) Poll(ctx context.Context, waitSeconds int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	offset, err := s.opts.Store.OwnerOffset(ctx, s.ids[0], s.opts.BotKey)
	if err != nil {
		return err
	}
	updates, err := s.opts.Bot.GetUpdates(ctx, offset, waitSeconds)
	if err != nil {
		s.event(ctx, "", "poll", "error", "poll_failed", 0)
		return safeBotError("polling owner updates: request failed", err)
	}
	sort.SliceStable(updates, func(i, j int) bool { return updates[i].UpdateID < updates[j].UpdateID })
	for _, u := range updates {
		if u.UpdateID < offset {
			continue
		}
		if err = s.handle(ctx, u); err != nil {
			return err
		}
		if err = s.opts.Store.SetOwnerOffset(ctx, s.ids[0], s.opts.BotKey, u.UpdateID+1); err != nil {
			return err
		}
		offset = u.UpdateID + 1
	}
	s.event(ctx, "", "poll", "success", "processed", 0)
	return nil
}
func (s *Service) handle(ctx context.Context, u Update) error {
	if u.Message == nil {
		return nil
	}
	m := u.Message
	if m.Chat.ID != s.opts.OwnerChatID {
		s.event(ctx, "", "authorization", "ignored", "unauthorized_chat", 0)
		return nil
	}
	key := s.opts.BotKey + ":update:" + strconv.FormatInt(u.UpdateID, 10)
	// Cookies are credentials: with or without /cookie they are imported, never
	// stored as a note, answer or log field, and the message is deleted.
	if raw, ok := cookieText(m.Text); ok {
		return s.handleCookies(ctx, m, raw, key)
	}
	if !strings.HasPrefix(m.Text, "/") {
		if m.ReplyToMessage == nil {
			return s.handleNote(ctx, m.Text, key)
		}
		ok, err := s.opts.Store.AnswerOwner(ctx, s.ids, s.deliveryKey, m.ReplyToMessage.MessageID, key, s.redact(m.Text), s.opts.Now())
		outcome, reason := "ignored", "unknown_question"
		if ok {
			outcome, reason = "success", "answer_saved"
		}
		if err != nil {
			outcome, reason = "error", "answer_failed"
		}
		agentID := ""
		var correlation int64
		if ok {
			for _, id := range s.ids {
				if n, e := s.opts.Store.OwnerEventID(ctx, id, key); e == nil {
					agentID, correlation = id, n
					break
				}
			}
		}
		s.event(ctx, agentID, "answer", outcome, reason, correlation)
		return err
	}
	fields := strings.Fields(m.Text)
	command := strings.TrimPrefix(fields[0], "/")
	// Qualified bot command names are not guessed; only the documented surface is
	// accepted, and arbitrary command text never becomes logging metadata.
	switch command {
	case "help", "start":
		return s.handleHelp(ctx, fields[1:], key)
	case "update":
		outcome, reason := "success", "update_requested"
		if err := s.opts.RequestUpdate(); err != nil {
			outcome, reason = "error", "update_request_failed"
		}
		s.event(ctx, "", "update", outcome, reason, 0)
		return nil
	case "restore":
		return s.handleRestore(ctx, fields[1:], key)
	case "pause", "resume", "kill", "dnc", "status":
	default:
		s.event(ctx, "", "command", "ignored", "unknown_command", 0)
		return nil
	}
	want := 0
	if command == "dnc" {
		want = 1
	}
	args := fields[1:]
	id := ""
	if len(s.ids) == 1 {
		id = s.ids[0]
		if len(args) == want+1 && args[0] == id {
			args = args[1:]
		}
	} else if len(args) > 0 {
		if _, ok := s.templates[args[0]]; ok {
			id = args[0]
			args = args[1:]
		}
	}
	if id == "" {
		s.event(ctx, "", "command", "ignored", "ambiguous_agent", 0)
		return nil
	}
	if len(args) != want {
		s.event(ctx, id, "command", "ignored", "invalid_command", 0)
		return nil
	}
	username := ""
	if command == "dnc" {
		normalized, err := store.NormalizeUsername(args[0])
		if err != nil {
			s.event(ctx, id, "dnc", "ignored", "invalid_command", 0)
			return nil
		}
		username = normalized
		if err = s.opts.Store.MarkDoNotContact(ctx, id, username, "owner_request"); err != nil {
			s.event(ctx, id, "dnc", "error", "contact_failed", 0)
			return errors.New("processing owner contact control: invalid or unavailable contact")
		}
	}
	text := ""
	if command == "status" {
		summary, err := s.summary(ctx, id)
		if err != nil {
			return err
		}
		text, err = s.summaryText(id, summary, false)
		if err != nil {
			return err
		}
	} else {
		var err error
		text, err = s.receiptText(id, command, username)
		if err != nil {
			return err
		}
	}
	if err := s.opts.Store.OwnerControl(ctx, id, command, key, text, s.opts.Now()); err != nil {
		s.event(ctx, id, command, "error", "control_failed", 0)
		return err
	}
	correlation, err := s.opts.Store.OwnerEventID(ctx, id, key)
	if err != nil {
		return err
	}
	s.event(ctx, id, command, "success", "control_persisted", correlation)
	if command == "kill" {
		return s.Notice(ctx, id, "killed", "terminal")
	}
	return nil
}

// handleHelp answers /help and /start with the documented command surface. It
// never changes state, so a repeated delivery only repeats the persisted reply.
func (s *Service) handleHelp(ctx context.Context, args []string, key string) error {
	id := s.ids[0]
	if len(s.ids) > 1 && len(args) > 0 {
		if _, ok := s.templates[args[0]]; ok {
			id = args[0]
		}
	}
	text, err := s.helpText(id)
	if err != nil {
		return err
	}
	n, err := s.opts.Store.QueueOwner(ctx, id, "command", text, key, s.opts.Now())
	outcome, reason := "success", "help_queued"
	if err != nil {
		outcome, reason = "error", "help_failed"
	}
	s.event(ctx, id, "help", outcome, reason, n)
	return err
}

// handleRestore lists the last archived memory entries and replaced strategy
// revisions with short ids, or restores one: /restore m<journal id> puts a
// memory entry back, /restore s<revision> re-writes that strategy as the
// newest revision. Nothing is deleted either way.
func (s *Service) handleRestore(ctx context.Context, args []string, key string) error {
	id := s.ids[0]
	if len(s.ids) > 1 && len(args) > 0 {
		if _, ok := s.templates[args[0]]; ok {
			id, args = args[0], args[1:]
		}
	}
	lang, now := messageLanguage(s.templates[id]), s.opts.Now()
	reply := func(reason, text string) error {
		n, err := s.opts.Store.QueueOwner(ctx, id, "command", text, "restore:"+key, now)
		outcome := "success"
		if err != nil {
			outcome, reason = "error", "restore_failed"
		}
		s.event(ctx, id, "restore", outcome, reason, n)
		return err
	}
	if len(args) == 0 {
		journal, err := s.opts.Store.MemoryJournal(ctx, id, 50)
		if err != nil {
			return err
		}
		history, err := s.opts.Store.StrategyHistory(ctx, id)
		if err != nil {
			return err
		}
		var lines []string
		for _, j := range journal {
			if len(lines) < 5 && (j.Change == store.ChangeOverflow || j.Change == store.ChangeExpired || j.Change == store.ChangeSuperseded) {
				lines = append(lines, fmt.Sprintf("m%d %s", j.ID, snippet(j.OldText)))
			}
		}
		for i, n := len(history)-2, 0; i >= 0 && n < 5; i, n = i-1, n+1 {
			lines = append(lines, fmt.Sprintf("s%d %s", history[i].Version, snippet(history[i].Text)))
		}
		return reply("restore_listed", restoreListText(lang, lines))
	}
	if len(args) != 1 || len(args[0]) < 2 || (args[0][0] != 'm' && args[0][0] != 's') {
		return reply("restore_invalid", restoreResultText(lang, "invalid"))
	}
	n, err := strconv.ParseInt(args[0][1:], 10, 64)
	if err != nil || n < 1 {
		return reply("restore_invalid", restoreResultText(lang, "invalid"))
	}
	if args[0][0] == 'm' {
		err = s.opts.Store.RestoreMemory(ctx, id, n, now)
	} else {
		_, err = s.opts.Store.RevertStrategy(ctx, id, n, now)
	}
	if err != nil {
		return reply("restore_refused", restoreResultText(lang, "failed"))
	}
	return reply("restore_done", restoreResultText(lang, "ok"))
}

// handleNote keeps an owner's free text for the next wake without spending a
// model call. One acknowledgement is queued; the update key makes both the note
// and the acknowledgement idempotent across redelivery and restarts.
func (s *Service) handleNote(ctx context.Context, text, key string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if s.redactor.Contains(text) {
		s.event(ctx, s.ids[0], "note", "ignored", "credential_refused", 0)
		_, err := s.opts.Store.QueueOwner(ctx, s.ids[0], "notification", credentialRefused, "note-refused:"+key, s.opts.Now())
		return err
	}
	if err := s.opts.Store.OwnerNote(ctx, s.ids, key, s.redact(text), s.opts.Now()); err != nil {
		s.event(ctx, "", "note", "error", "note_failed", 0)
		return err
	}
	s.event(ctx, s.ids[0], "note", "success", "note_saved", 0)
	ack, err := s.noteAck(s.ids[0])
	if err != nil {
		return err
	}
	_, err = s.opts.Store.QueueOwner(ctx, s.ids[0], "notification", ack, "note-ack:"+key, s.opts.Now())
	return err
}

// Flush claims each message before HTTP. Ambiguous outcomes are retained as
// failed/sending and never replayed automatically; all other queued messages
// remain available. Content is private delivery data, never operational logs.
func (s *Service) Flush(ctx context.Context) error {
	messages, err := s.opts.Store.PendingOwner(ctx, s.ids)
	if err != nil {
		return err
	}
	for _, m := range messages {
		ok, err := s.opts.Store.ClaimOwner(ctx, m.ID, s.deliveryKey)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		text := m.Text
		if m.Kind == "question" {
			if hint, hintErr := s.render(m.AgentID, "question_hint", s.ownerData(m.AgentID, store.OwnerSummary{})); hintErr == nil && hint != "" {
				text += "\n\n" + hint
			}
		}
		// A shared bot names the speaker once; a private one-agent chat reads as
		// a single teammate and needs no label.
		if len(s.ids) > 1 {
			speaker := s.templates[m.AgentID].Persona.Name
			if speaker == "" {
				speaker = m.AgentID
			}
			text = speaker + ": " + text
		}
		messageID, sendErr := s.opts.Bot.SendMessage(ctx, s.opts.OwnerChatID, text, 0)
		finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err = s.opts.Store.FinishOwner(finalCtx, m.ID, messageID, sendErr == nil)
		cancel()
		outcome, reason := "success", "delivered"
		if sendErr != nil {
			outcome, reason = "error", "delivery_failed"
		}
		s.event(ctx, m.AgentID, m.Kind, outcome, reason, m.ID)
		if err != nil {
			return err
		}
		if sendErr != nil {
			return safeBotError("delivering owner message: request failed", sendErr)
		}
	}
	return nil
}

// Notice deduplicates a named event across polls/restarts. Producers supply a
// stable internal event identity (day, wake ID or browser event counter), never
// message content. Only code-owned notice kinds can be sent.
func (s *Service) Notice(ctx context.Context, id, kind, event string) error {
	return s.notice(ctx, id, kind, event, "")
}

func (s *Service) notice(ctx context.Context, id, kind, event, extra string) error {
	if _, ok := s.templates[id]; !ok {
		return errors.New("queueing owner notice: unconfigured agent")
	}
	if event == "" || len(event) > 200 {
		return errors.New("queueing owner notice: event identity required")
	}
	notice := noticeText(messageLanguage(s.templates[id]), kind)
	if notice == "" {
		return errors.New("queueing owner notice: unknown kind")
	}
	return s.queueNotice(ctx, id, kind, event, notice+extra)
}

func (s *Service) queueNotice(ctx context.Context, id, kind, event, notice string) error {
	data := s.ownerData(id, store.OwnerSummary{})
	data.Notice = notice
	text, err := s.render(id, "notice", data)
	if err != nil {
		return err
	}
	n, err := s.opts.Store.QueueOwner(ctx, id, "notice", text, "notice:"+kind+":"+event, s.opts.Now())
	outcome, reason := "queued", kind
	if err != nil {
		outcome, reason = "error", "notice_failed"
	}
	s.event(ctx, id, "notice", outcome, reason, n)
	return err
}

// BrowserStopped is the browser adapter's safe event seam. The caller supplies
// only a code and local event ID; no DOM, page text, session or URL is accepted.
func (s *Service) BrowserStopped(ctx context.Context, id, kind, event string) error {
	if (kind != "logged_out" && kind != "captcha") || event == "" || len(event) > 200 {
		return errors.New("stopping owner browser: unsupported event")
	}
	if _, ok := s.templates[id]; !ok {
		return errors.New("stopping owner browser: unconfigured agent")
	}
	if err := s.opts.Store.PauseOwnerAgent(ctx, id); err != nil {
		return err
	}
	return s.notice(ctx, id, kind, event, "\n"+CookieHowTo(messageLanguage(s.templates[id])))
}

// BrowserRestored resumes an agent whose profile is logged in again after a
// cookie import, and tells the owner once.
func (s *Service) BrowserRestored(ctx context.Context, id, event string) error {
	if _, ok := s.templates[id]; !ok {
		return errors.New("restoring owner browser: unconfigured agent")
	}
	if err := s.opts.Store.ResumePausedAgent(ctx, id); err != nil {
		return err
	}
	return s.notice(ctx, id, "logged_in", event, "")
}

func (s *Service) summary(ctx context.Context, id string) (store.OwnerSummary, error) {
	now := s.opts.Now().UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return s.opts.Store.OwnerSummary(ctx, id, s.templates[id].DryRun, day, day.Add(24*time.Hour))
}

// Tick observes persisted state, thresholds and UTC digest schedule. A pending
// outbox item is its durable event marker, so even a failed delivery is not
// repeated for the same event. Metrics use each action's latest daily sample.
func (s *Service) Tick(ctx context.Context) error {
	now := s.opts.Now().UTC()
	day := now.Format("2006-01-02")
	for _, id := range s.ids {
		tmpl := s.templates[id]
		summary, err := s.summary(ctx, id)
		if err != nil {
			return err
		}
		if summary.Status == "killed" {
			if err = s.Notice(ctx, id, "killed", "terminal"); err != nil {
				return err
			}
		}
		if summary.CostUSD >= tmpl.Limits.MaxUSDPerDay*.8 && tmpl.Limits.MaxUSDPerDay > 0 {
			if err = s.Notice(ctx, id, "budget_80", day); err != nil {
				return err
			}
		}
		if summary.CostUSD >= tmpl.Limits.MaxUSDPerDay && tmpl.Limits.MaxUSDPerDay > 0 {
			if err = s.Notice(ctx, id, "budget_100", day); err != nil {
				return err
			}
		}
		if (tmpl.Limits.PostsPerDay > 0 && summary.Posts >= tmpl.Limits.PostsPerDay) || (tmpl.Limits.RepliesPerDay > 0 && summary.Replies >= tmpl.Limits.RepliesPerDay) {
			if err = s.Notice(ctx, id, "daily_limit", day); err != nil {
				return err
			}
		}
		if err = s.silentAgent(ctx, id, summary, now); err != nil {
			return err
		}
		if now.Hour() >= tmpl.Telegram.DigestHour {
			text, renderErr := s.summaryText(id, summary, true)
			if renderErr != nil {
				return renderErr
			}
			if line := failureLine(messageLanguage(tmpl), summary); line != "" {
				text += "\n" + line
			}
			n, err := s.opts.Store.QueueOwner(ctx, id, "digest", text, "digest:"+day, now)
			outcome, reason := "queued", "daily_digest"
			if err != nil {
				outcome, reason = "error", "digest_failed"
			}
			s.event(ctx, id, "digest", outcome, reason, n)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// silentAgent queues one notice per local day while a running agent has
// published nothing for longer than kpi.max_hours_without_publish. With nothing
// ever published the clock starts at its first wake.
func (s *Service) silentAgent(ctx context.Context, id string, summary store.OwnerSummary, now time.Time) error {
	tmpl := s.templates[id]
	limit := time.Duration(tmpl.KPI.MaxHoursWithoutPublish) * time.Hour
	if limit <= 0 || summary.Status != "running" {
		return nil
	}
	since, lastReason, err := s.opts.Store.OwnerSilence(ctx, id, tmpl.DryRun)
	if err != nil || since.IsZero() || now.Sub(since) <= limit {
		return err
	}
	text := silentText(messageLanguage(tmpl), int(now.Sub(since).Hours()), lastReason, summary.Refusals)
	return s.queueNotice(ctx, id, "no_publish", now.In(s.location(id)).Format(time.DateOnly), text)
}

// ownerCommands is the menu the bot advertises. Descriptions stay short; the
// agent-ID form is named only when one bot serves several agents. Wording is
// selected from the first agent's persona language.
func ownerCommands(lang string, multiAgent bool) []BotCommand {
	vi := strings.HasPrefix(strings.ToLower(lang), "vi")
	suffix := ""
	if multiAgent {
		if vi {
			suffix = " (kèm agent_id)"
		} else {
			suffix = " (with agent_id)"
		}
	}
	if vi {
		dnc := "né username: /dnc <username>"
		if multiAgent {
			dnc = "né username: /dnc <agent_id> <username>"
		}
		return []BotCommand{
			{Command: "help", Description: "xem danh sách lệnh"},
			{Command: "status", Description: "xem đang làm gì" + suffix},
			{Command: "pause", Description: "tạm dừng" + suffix},
			{Command: "resume", Description: "chạy tiếp" + suffix},
			{Command: "kill", Description: "tắt hẳn, ko bật lại" + suffix},
			{Command: "dnc", Description: dnc},
			{Command: "restore", Description: "xem và khôi phục thứ agent đã xóa hoặc sửa"},
			{Command: "cookie", Description: "đăng nhập lại Threads bằng cookie"},
			{Command: "update", Description: "kiểm tra bản mới và cập nhật"},
		}
	}
	dnc := "ignore username: /dnc <username>"
	if multiAgent {
		dnc = "ignore username: /dnc <agent_id> <username>"
	}
	return []BotCommand{
		{Command: "help", Description: "list commands"},
		{Command: "status", Description: "what i'm doing" + suffix},
		{Command: "pause", Description: "pause" + suffix},
		{Command: "resume", Description: "resume" + suffix},
		{Command: "kill", Description: "stop for good" + suffix},
		{Command: "dnc", Description: dnc},
		{Command: "restore", Description: "list and restore what the agent archived or replaced"},
		{Command: "cookie", Description: "log in to Threads again with cookies"},
		{Command: "update", Description: "check for a release and update"},
	}
}

// registerCommands replaces the bot menu once per service start. Telegram
// persists the menu, so a failed registration is logged with a fixed reason and
// the poller continues; neither owner commands nor wakes depend on it.
func (s *Service) registerCommands(ctx context.Context) {
	err := s.opts.Bot.SetMyCommands(ctx, ownerCommands(messageLanguage(s.templates[s.ids[0]]), len(s.ids) > 1))
	outcome, reason := "success", "menu_registered"
	if err != nil {
		outcome, reason = "error", "menu_failed"
	}
	s.event(ctx, "", "commands", outcome, reason, 0)
}

func (s *Service) Run(ctx context.Context) error {
	s.registerCommands(ctx)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := s.Tick(ctx)
		if err == nil {
			err = s.Flush(ctx)
		}
		if err == nil {
			err = s.Poll(ctx, 20)
		}
		if err != nil {
			s.event(ctx, "", "service", "error", "iteration_failed", 0)
			if e := s.opts.Wait(ctx, time.Second); e != nil {
				return e
			}
		}
	}
}
func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func safeBotError(message string, cause error) error {
	err := errors.New(message)
	if errors.Is(cause, context.Canceled) {
		return errors.Join(err, context.Canceled)
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return errors.Join(err, context.DeadlineExceeded)
	}
	return err
}

// cookieText reports whether an owner message carries cookies and returns the
// cookie text: after /cookie, or the whole message when it looks like an export
// (a JSON array naming sessionid, or text with sessionid=).
func cookieText(text string) (string, bool) {
	t := strings.TrimSpace(text)
	if f := strings.Fields(t); len(f) > 0 && f[0] == "/cookie" {
		return strings.TrimSpace(strings.TrimPrefix(t, f[0])), true
	}
	l := strings.ToLower(t)
	if strings.Contains(l, "sessionid=") || (strings.Contains(l, `"sessionid"`) && strings.Contains(l, `"name"`)) {
		return t, true
	}
	return "", false
}

// handleCookies deletes the owner's message first, whatever follows, then
// imports the cookies once and answers once. Nothing from the message is kept.
func (s *Service) handleCookies(ctx context.Context, m *Message, raw, key string) error {
	if err := s.opts.Bot.DeleteMessage(ctx, m.Chat.ID, m.MessageID); err != nil {
		s.event(ctx, "", "cookie", "error", "delete_failed", 0)
	}
	id := ""
	if len(s.ids) == 1 {
		id = s.ids[0]
	}
	if len(s.ids) > 1 {
		if f := strings.Fields(raw); len(f) > 1 {
			if _, ok := s.templates[f[0]]; ok {
				id, raw = f[0], strings.TrimSpace(strings.TrimPrefix(raw, f[0]))
			}
		}
	}
	reply := func(id, text string) error {
		if id == "" {
			id = s.ids[0]
		}
		_, err := s.opts.Store.QueueOwner(ctx, id, "command", text, "cookie:"+key, s.opts.Now())
		return err
	}
	if id == "" {
		s.event(ctx, "", "cookie", "ignored", "ambiguous_agent", 0)
		return reply("", "send /cookie <agent_id> <cookies>")
	}
	lang := messageLanguage(s.templates[id])
	if s.cookieImport == nil {
		s.event(ctx, id, "cookie", "ignored", "unavailable", 0)
		return reply(id, CookieResultText(lang, "failed"))
	}
	status := s.cookieImport(ctx, id, raw)
	raw = ""
	outcome := "error"
	if status == "ok" {
		outcome = "success"
	}
	s.event(ctx, id, "cookie", outcome, "import_"+status, 0)
	if status == "ok" {
		// BrowserRestored resumes the agent and sends the one confirmation.
		return s.BrowserRestored(ctx, id, key)
	}
	return reply(id, CookieResultText(lang, status))
}
