// Package telegram is the owner-contact integration (spec section 9).
//
// client.go is the minimal Bot API transport used by the service layer. It
// implements getUpdates, sendMessage and setMyCommands, makes exactly one HTTP
// request per call, and never retries: retry and outbox scheduling belong to the
// service. The client decodes only the fields in the shared DTOs, so usernames,
// names, photos and other profile fields never reach the agent context.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/canhta/threadwalker/internal/config"
)

const (
	defaultBaseURL = "https://api.telegram.org"
	defaultTimeout = 10 * time.Second

	// maxLongPollWaitSeconds bounds the server-side long-poll wait. The
	// effective deadline is raised above the poll wait so a healthy long poll
	// is never cut short by the normal request timeout.
	maxLongPollWaitSeconds = 50
	longPollMargin         = 10 * time.Second

	// maxResponseBytes bounds one Bot API response. The largest legitimate
	// response is a batch of updates, so this leaves room while keeping a
	// misbehaving endpoint from consuming arbitrary memory.
	maxResponseBytes = 4 << 20

	// maxMessageRunes is the Bot API limit on sendMessage text after entity
	// parsing.
	maxMessageRunes = 4096

	// Bot API limits for one command menu entry and for the menu itself.
	maxCommandNameRunes   = 32
	maxCommandDescRunes   = 256
	maxBotCommandsPerMenu = 100
)

// ClientOptions configures a Client. Zero values select the official Bot API
// endpoint and a 10-second per-call timeout. HTTPClient is cloned, so its
// transport is reused while redirects are always disabled.
type ClientOptions struct {
	BaseURL    string
	Token      config.Secret
	HTTPClient *http.Client
	Timeout    time.Duration
}

// Chat is the only chat metadata the client keeps: the id needed to authorize
// an owner command. Titles, usernames and member data are not decoded.
type Chat struct {
	ID int64 `json:"id"`
}

// Message is the subset of a Telegram message used by the service: its id, its
// chat, its text and an optional replied-to message. Profile fields are
// deliberately absent and are dropped by the JSON decoder.
type Message struct {
	MessageID      int64    `json:"message_id"`
	Chat           Chat     `json:"chat"`
	Text           string   `json:"text"`
	ReplyToMessage *Message `json:"reply_to_message,omitempty"`
}

// Update is one Bot API update. Updates that carry no `message` (for example
// edited_message or callback_query) are returned with a nil Message so callers
// can still advance the offset past them.
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message,omitempty"`
}

// Client calls the Telegram Bot API over HTTPS with standard-library HTTP.
// It is safe for concurrent use; it keeps no mutable state.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
	timeout    time.Duration
}

// NewClient validates configuration and builds a client. It never performs a
// network call, so tests and offline runs cannot contact Telegram by accident.
func NewClient(opts ClientOptions) (*Client, error) {
	token := opts.Token.Expose()
	if err := validateToken(token); err != nil {
		return nil, err
	}

	baseURL := strings.TrimSpace(opts.BaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	parsed, err := url.Parse(baseURL)
	if err != nil ||
		(!strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https")) ||
		parsed.Host == "" ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" ||
		parsed.User != nil {
		return nil, errors.New("telegram: invalid base URL")
	}

	timeout := opts.Timeout
	if timeout < 0 {
		return nil, errors.New("telegram: timeout must not be negative")
	}
	if timeout == 0 {
		timeout = defaultTimeout
	}

	httpClient := &http.Client{}
	if opts.HTTPClient != nil {
		cloned := *opts.HTTPClient
		httpClient = &cloned
	}
	// Telegram never redirects a Bot API method; refusing redirects keeps the
	// token-bearing URL from being replayed to an unexpected host. Returning
	// ErrUseLastResponse makes the redirect response itself the result.
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	return &Client{
		baseURL:    baseURL,
		token:      token,
		httpClient: httpClient,
		timeout:    timeout,
	}, nil
}

func validateToken(token string) error {
	if token == "" || strings.TrimSpace(token) != token {
		return errors.New("telegram: bot token is required")
	}
	for _, r := range token {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '/' || r == '?' || r == '#' {
			return errors.New("telegram: bot token is invalid")
		}
	}
	return nil
}

// GetUpdates performs one getUpdates call. waitSeconds is clamped to
// [0, maxLongPollWaitSeconds]; the request deadline is raised above the poll
// wait when needed. Non-message updates are returned with a nil Message so the
// caller can move the offset past them.
func (c *Client) GetUpdates(ctx context.Context, offset int64, waitSeconds int) ([]Update, error) {
	const operation = "telegram get updates"
	if ctx == nil {
		return nil, fmt.Errorf("%s: nil context", operation)
	}

	wait := waitSeconds
	if wait < 0 {
		wait = 0
	}
	if wait > maxLongPollWaitSeconds {
		wait = maxLongPollWaitSeconds
	}

	timeout := c.timeout
	if wait > 0 {
		required := time.Duration(wait)*time.Second + longPollMargin
		if timeout < required {
			timeout = required
		}
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := c.post(requestCtx, "getUpdates", getUpdatesPayload{
		Offset:         offset,
		Timeout:        wait,
		AllowedUpdates: []string{"message"},
	}, operation)
	if err != nil {
		return nil, err
	}
	if !hasResult(result) {
		return nil, fmt.Errorf("%s: missing result", operation)
	}

	var updates []Update
	if err := json.Unmarshal(result, &updates); err != nil {
		return nil, fmt.Errorf("%s: decode result", operation)
	}
	return updates, nil
}

// SendMessage sends one text message and returns its Telegram message id. It
// returns no response body, chat metadata or user profile. The current Bot API
// spells replies as reply_parameters; the legacy reply_to_message_id field is
// not sent.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, replyTo int64) (int64, error) {
	const operation = "telegram send message"
	if ctx == nil {
		return 0, fmt.Errorf("%s: nil context", operation)
	}
	if chatID == 0 {
		return 0, fmt.Errorf("%s: chat id is required", operation)
	}
	if text == "" {
		return 0, fmt.Errorf("%s: text is required", operation)
	}
	if utf8.RuneCountInString(text) > maxMessageRunes {
		return 0, fmt.Errorf("%s: text exceeds %d characters", operation, maxMessageRunes)
	}
	if replyTo < 0 {
		return 0, fmt.Errorf("%s: reply message id is invalid", operation)
	}

	payload := sendMessagePayload{ChatID: chatID, Text: text}
	if replyTo > 0 {
		payload.ReplyParameters = &replyParameters{MessageID: replyTo}
	}

	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	result, err := c.post(requestCtx, "sendMessage", payload, operation)
	if err != nil {
		return 0, err
	}
	if !hasResult(result) {
		return 0, fmt.Errorf("%s: missing result", operation)
	}

	var message Message
	if err := json.Unmarshal(result, &message); err != nil {
		return 0, fmt.Errorf("%s: decode result", operation)
	}
	if message.MessageID <= 0 {
		return 0, fmt.Errorf("%s: missing message id", operation)
	}
	return message.MessageID, nil
}

type getUpdatesPayload struct {
	Offset  int64 `json:"offset"`
	Timeout int   `json:"timeout"`
	// Telegram persists the last allowed_updates filter per bot; sending it on
	// every call stops a stale filter from silently hiding owner commands.
	AllowedUpdates []string `json:"allowed_updates"`
}

// BotCommand is one entry in the bot's command menu. The menu carries no
// argument slots, so any accepted arguments are named in Description.
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// SetMyCommands replaces the default-scope command menu the owner sees in
// Telegram. It validates the documented Bot API limits locally, making no
// network call for an invalid menu, and reports only a stable failure class.
func (c *Client) SetMyCommands(ctx context.Context, commands []BotCommand) error {
	const operation = "telegram set my commands"
	if ctx == nil {
		return fmt.Errorf("%s: nil context", operation)
	}
	if len(commands) == 0 || len(commands) > maxBotCommandsPerMenu {
		return fmt.Errorf("%s: between 1 and %d commands are required", operation, maxBotCommandsPerMenu)
	}
	seen := make(map[string]bool, len(commands))
	for _, command := range commands {
		if !validCommandName(command.Command) {
			return fmt.Errorf("%s: command name is invalid", operation)
		}
		if seen[command.Command] {
			return fmt.Errorf("%s: duplicate command name", operation)
		}
		seen[command.Command] = true
		if n := utf8.RuneCountInString(command.Description); n == 0 || n > maxCommandDescRunes {
			return fmt.Errorf("%s: command description must be 1..%d characters", operation, maxCommandDescRunes)
		}
	}

	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	result, err := c.post(requestCtx, "setMyCommands", setMyCommandsPayload{Commands: commands}, operation)
	if err != nil {
		return err
	}
	if !hasResult(result) {
		return fmt.Errorf("%s: missing result", operation)
	}

	var accepted bool
	if err := json.Unmarshal(result, &accepted); err != nil {
		return fmt.Errorf("%s: decode result", operation)
	}
	if !accepted {
		return fmt.Errorf("%s: api returned false", operation)
	}
	return nil
}

// validCommandName enforces the Bot API spelling: 1..32 lowercase ASCII
// letters, digits or underscores.
func validCommandName(name string) bool {
	if name == "" || utf8.RuneCountInString(name) > maxCommandNameRunes {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

type setMyCommandsPayload struct {
	Commands []BotCommand `json:"commands"`
}

type sendMessagePayload struct {
	ChatID          int64            `json:"chat_id"`
	Text            string           `json:"text"`
	ReplyParameters *replyParameters `json:"reply_parameters,omitempty"`
}

type replyParameters struct {
	MessageID int64 `json:"message_id"`
}

type apiEnvelope struct {
	OK        bool            `json:"ok"`
	ErrorCode int             `json:"error_code"`
	Result    json.RawMessage `json:"result"`
}

// post performs exactly one HTTP request. Every error it returns names only the
// operation, a stable class and, for API rejections, the numeric error code.
// Tokens, endpoint URLs, chat ids, message text, response bodies and Telegram
// descriptions are never included.
func (c *Client) post(ctx context.Context, method string, payload any, operation string) (json.RawMessage, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%s: encode request", operation)
	}

	endpoint := c.baseURL + "/bot" + c.token + "/" + method
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("%s: build request", operation)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "threadwalker/telegram")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, transportError(ctx, operation, err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%s: unexpected HTTP status %d", operation, response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		if cause := cancellationCause(ctx, err); cause != nil {
			return nil, fmt.Errorf("%s: %w", operation, cause)
		}
		return nil, fmt.Errorf("%s: read response", operation)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("%s: response exceeds %d bytes", operation, maxResponseBytes)
	}

	var envelope apiEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("%s: decode response", operation)
	}
	if !envelope.OK {
		return nil, fmt.Errorf("%s: api error code %d", operation, envelope.ErrorCode)
	}
	return envelope.Result, nil
}

// transportError keeps cancellation identity while dropping transport details
// that can contain the token-bearing URL or an injected payload.
func transportError(ctx context.Context, operation string, err error) error {
	if cause := cancellationCause(ctx, err); cause != nil {
		return fmt.Errorf("%s: %w", operation, cause)
	}
	return fmt.Errorf("%s: request failed", operation)
}

func cancellationCause(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return context.DeadlineExceeded
	}
	return nil
}

func hasResult(result json.RawMessage) bool {
	trimmed := bytes.TrimSpace(result)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}
