// Package telegram owns owner control, a durable outbox and Bot API polling.
// It does not import tools or agent; model calls can only enqueue local messages.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/store"
)

type BotAPI interface {
	GetUpdates(context.Context, int64, int) ([]Update, error)
	SendMessage(context.Context, int64, string, int64) (int64, error)
}
type ServiceOptions struct {
	Store *store.Store
	Bot   BotAPI
	// BotKey is a one-way digest of bot identity, never the credential itself.
	BotKey      string
	OwnerChatID int64
	Templates   []config.Template
	Now         func() time.Time
	Wait        func(context.Context, time.Duration) error
	Logger      *slog.Logger
}
type Service struct {
	opts      ServiceOptions
	mu        sync.Mutex
	templates map[string]config.Template
	ids       []string
	secrets   []string
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
	s := &Service{opts: opts, templates: map[string]config.Template{}}
	for _, t := range opts.Templates {
		if t.ID == "" || s.templates[t.ID].ID != "" || t.Telegram.DigestHour < 0 || t.Telegram.DigestHour > 23 {
			return nil, errors.New("configuring Telegram service: invalid agent or digest hour")
		}
		s.templates[t.ID] = t
		s.ids = append(s.ids, t.ID)
		for _, secret := range []config.Secret{t.LLM.APIKey, t.Telegram.BotToken, t.Telegram.OwnerChatID} {
			if v := secret.Expose(); v != "" {
				s.secrets = append(s.secrets, v)
			}
		}
	}
	sort.Strings(s.ids)
	return s, nil
}
func (s *Service) redact(text string) string {
	for _, secret := range s.secrets {
		text = strings.ReplaceAll(text, secret, "[REDACTED]")
	}
	return text
}
func (s *Service) event(ctx context.Context, id, op, outcome, reason string, correlation int64) {
	logging.Emit(ctx, s.opts.Logger, slog.LevelInfo, logging.Record{Event: logging.TelegramOperationFinished, AgentID: id, Operation: op, Outcome: outcome, Reason: reason, ToolCallID: fmt.Sprintf("owner/%d", correlation)})
}

// QueueOwner returns after SQLite persistence, without waiting for polling,
// Telegram delivery or an answer. Disabled agents have no service binding.
func (s *Service) QueueOwner(ctx context.Context, id, text string, question bool) (int64, error) {
	if _, ok := s.templates[id]; !ok {
		return 0, errors.New("queueing owner message: unconfigured agent")
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
		return errors.New("polling owner updates: request failed")
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
	if !strings.HasPrefix(m.Text, "/") {
		if m.ReplyToMessage == nil {
			return nil
		}
		ok, err := s.opts.Store.AnswerOwner(ctx, s.ids, m.ReplyToMessage.MessageID, key, s.redact(m.Text), s.opts.Now())
		outcome, reason := "ignored", "unknown_question"
		if ok {
			outcome, reason = "success", "answer_saved"
		}
		if err != nil {
			outcome, reason = "error", "answer_failed"
		}
		s.event(ctx, "", "answer", outcome, reason, 0)
		return err
	}
	fields := strings.Fields(m.Text)
	command := strings.TrimPrefix(fields[0], "/")
	// Qualified bot command names are not guessed; only the documented surface is
	// accepted, and arbitrary command text never becomes logging metadata.
	switch command {
	case "pause", "resume", "kill", "dnc", "status":
	default:
		s.event(ctx, "", "command", "ignored", "unknown_command", 0)
		return nil
	}
	args := fields[1:]
	id := ""
	if len(s.ids) == 1 {
		id = s.ids[0]
		if len(args) > 0 && args[0] == id {
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
	want := 0
	if command == "dnc" {
		want = 1
	}
	if len(args) != want {
		s.event(ctx, id, "command", "ignored", "invalid_command", 0)
		return nil
	}
	text := "Agent " + id + ": " + command + " received."
	if command == "status" {
		summary, err := s.summary(ctx, id)
		if err != nil {
			return err
		}
		text = summaryText(id, summary, false)
	}
	if command == "dnc" {
		contacts, ok := any(s.opts.Store).(interface {
			MarkDoNotContact(context.Context, string, string, string) error
		})
		if !ok {
			return errors.New("processing owner contact control: store unavailable")
		}
		if err := contacts.MarkDoNotContact(ctx, id, args[0], "owner_request"); err != nil {
			s.event(ctx, id, "dnc", "error", "contact_failed", 0)
			return errors.New("processing owner contact control: invalid or unavailable contact")
		}
	}
	if err := s.opts.Store.OwnerControl(ctx, id, command, key, text, s.opts.Now()); err != nil {
		s.event(ctx, id, command, "error", "control_failed", 0)
		return err
	}
	s.event(ctx, id, command, "success", "control_persisted", 0)
	if command == "kill" {
		return s.Notice(ctx, id, "killed", "terminal")
	}
	return nil
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
		ok, err := s.opts.Store.ClaimOwner(ctx, m.ID)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		messageID, sendErr := s.opts.Bot.SendMessage(ctx, s.opts.OwnerChatID, "["+m.AgentID+"] "+m.Text, 0)
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
			return errors.New("delivering owner message: request failed")
		}
	}
	return nil
}

// Notice deduplicates a named event across polls/restarts. Producers supply a
// stable internal event identity (day, wake ID or browser event counter), never
// message content. Only code-owned notice kinds can be sent.
func (s *Service) Notice(ctx context.Context, id, kind, event string) error {
	if _, ok := s.templates[id]; !ok {
		return errors.New("queueing owner notice: unconfigured agent")
	}
	var text string
	switch kind {
	case "daily_limit":
		text = "Daily action limit reached; agent paused."
	case "budget_80":
		text = "Daily budget reached 80%."
	case "budget_100":
		text = "Daily budget reached 100%."
	case "logged_out":
		text = "Browser logged out; agent paused."
	case "captcha":
		text = "Browser captcha detected; agent paused."
	case "killed":
		text = "Agent killed."
	default:
		return errors.New("queueing owner notice: unknown kind")
	}
	if event == "" || len(event) > 200 {
		return errors.New("queueing owner notice: event identity required")
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
	if kind != "logged_out" && kind != "captcha" {
		return errors.New("stopping owner browser: unsupported event")
	}
	if _, ok := s.templates[id]; !ok {
		return errors.New("stopping owner browser: unconfigured agent")
	}
	if err := s.opts.Store.PauseOwnerAgent(ctx, id); err != nil {
		return err
	}
	return s.Notice(ctx, id, kind, event)
}
func (s *Service) summary(ctx context.Context, id string) (store.OwnerSummary, error) {
	now := s.opts.Now().UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return s.opts.Store.OwnerSummary(ctx, id, day, day.Add(24*time.Hour))
}
func summaryText(id string, m store.OwnerSummary, digest bool) string {
	label := "Status"
	if digest {
		label = "Daily digest"
	}
	next := "unscheduled"
	if !m.NextWake.IsZero() {
		next = m.NextWake.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("%s %s: %s; next wake %s; posts %d; replies %d; spend $%.4f; metrics views %d, likes %d, replies %d, reposts %d; playbook changes %d.", label, id, m.Status, next, m.Posts, m.Replies, m.CostUSD, m.Views, m.Likes, m.Responses, m.Reposts, m.PlaybookChanges)
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
			if err = s.opts.Store.PauseOwnerAgent(ctx, id); err != nil {
				return err
			}
			if err = s.Notice(ctx, id, "daily_limit", day); err != nil {
				return err
			}
		}
		if now.Hour() >= tmpl.Telegram.DigestHour {
			n, err := s.opts.Store.QueueOwner(ctx, id, "digest", summaryText(id, summary, true), "digest:"+day, now)
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
func (s *Service) Run(ctx context.Context) error {
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
