// Package threads provides the bounded synthetic-v1 browser backend. The
// harness must guard every call and reserve action intents before write calls.
package threads

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/canhta/threadwalker/internal/logging"
)

// ErrUnknownOutcome means a submission may have reached the site. The harness
// must retain its reserved intent and stop the wake without retrying it.
var ErrUnknownOutcome = errors.New("unknown submission outcome")

// Backend is owned by one wake. Writes require an already guarded action intent.
type Backend interface {
	ReadFeed(context.Context) (Page, error)
	Search(context.Context, string) (Page, error)
	ReadPost(context.Context, string) (Page, error)
	ReadOwnMetrics(context.Context) (Metrics, error)
	Post(context.Context, string) (string, error)
	Reply(context.Context, string, string) (string, error)
	Close(context.Context) error
}

type Options struct {
	AgentID        string
	BaseURL        string
	ProfileDir     string
	ExecutablePath string
	Timeout        time.Duration
	StartupTimeout time.Duration
	Logger         *slog.Logger
	Now            func() time.Time
	NoSandbox      bool
}

type transport interface {
	navigate(context.Context, string) error
	snapshot(context.Context) ([]byte, error)
	submit(context.Context, string) (bool, error)
	acknowledgement(context.Context) ([]byte, error)
	close(context.Context) error
}

// Browser serializes its page operations; closing interrupts an active call.
type Browser struct {
	base      *url.URL
	driver    transport
	timeout   time.Duration
	agentID   string
	logger    *slog.Logger
	now       func() time.Time
	gate      chan struct{}
	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

var _ Backend = (*Browser)(nil)

// operationError preserves only safe error identity, never transport diagnostics.
func operationError(op string, err error) error {
	if err == nil {
		return nil
	}
	cause := errors.New("transport_failure")
	for _, sentinel := range []error{ErrUnknownOutcome, ErrLoggedOut, ErrCaptcha, ErrUnknownPage, ErrMalformedPage, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, sentinel) {
			cause = sentinel
			break
		}
	}
	if errors.Is(err, ErrUnknownOutcome) {
		for _, sentinel := range []error{ErrLoggedOut, ErrCaptcha, context.Canceled, context.DeadlineExceeded} {
			if errors.Is(err, sentinel) {
				cause = errors.Join(ErrUnknownOutcome, sentinel)
				break
			}
		}
	}
	return fmt.Errorf("threads %s: %w", op, cause)
}
func (b *Browser) record(ctx context.Context, op string, start time.Time, err error) {
	reason, outcome := "completed", "success"
	if err != nil {
		outcome = "error"
		reason = "transport_failure"
		switch {
		case errors.Is(err, ErrUnknownOutcome):
			reason = "unknown_outcome"
		case errors.Is(err, ErrLoggedOut):
			reason = "logged_out"
		case errors.Is(err, ErrCaptcha):
			reason = "captcha"
		case errors.Is(err, ErrUnknownPage):
			reason = "unknown_page"
		case errors.Is(err, ErrMalformedPage):
			reason = "malformed_page"
		case errors.Is(err, context.DeadlineExceeded):
			reason = "deadline"
		case errors.Is(err, context.Canceled):
			reason = "canceled"
		}
	}
	logging.Emit(ctx, b.logger, slog.LevelInfo, logging.Record{Event: "browser.operation.finished", AgentID: b.agentID, Operation: op, Outcome: outcome, Reason: reason, DurationMS: max(0, b.now().Sub(start).Milliseconds())})
}
func (b *Browser) begin(ctx context.Context) (context.Context, func(), error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	select {
	case b.gate <- struct{}{}:
	case <-ctx.Done():
		cancel()
		return nil, nil, ctx.Err()
	}
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		<-b.gate
		cancel()
		return nil, nil, errors.New("closed")
	}
	return ctx, func() { <-b.gate; cancel() }, nil
}
func (b *Browser) target(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", ErrMalformedPage
	}
	u = b.base.ResolveReference(u)
	if !sameOrigin(b.base, u) || u.Fragment != "" || strings.ContainsAny(raw, "\\\r\n\t") {
		return "", ErrMalformedPage
	}
	return u.String(), nil
}
func sameOrigin(base, u *url.URL) bool {
	return u != nil && u.User == nil && (u.Scheme == "http" || u.Scheme == "https") && strings.EqualFold(u.Scheme, base.Scheme) && strings.EqualFold(u.Hostname(), base.Hostname()) && effectivePort(u) == effectivePort(base) && u.Hostname() != ""
}
func effectivePort(u *url.URL) string {
	if u.Port() != "" {
		return u.Port()
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}
func (b *Browser) read(ctx context.Context, op, path, kind string) (page Page, err error) {
	start := b.now()
	defer func() { err = operationError(op, err); b.record(ctx, op, start, err) }()
	dest, err := b.target(path)
	if err != nil {
		return Page{}, err
	}
	call, done, err := b.begin(ctx)
	if err != nil {
		return Page{}, err
	}
	defer done()
	if err = b.driver.navigate(call, dest); err != nil {
		return Page{}, err
	}
	raw, err := b.driver.snapshot(call)
	if err != nil {
		return Page{}, err
	}
	return ParseSnapshot(raw, kind, b.base.String())
}
func (b *Browser) ReadFeed(ctx context.Context) (Page, error) {
	return b.read(ctx, "read_feed", "/feed", "feed")
}
func (b *Browser) Search(ctx context.Context, q string) (Page, error) {
	return b.read(ctx, "search", "/search?q="+url.QueryEscape(q), "search")
}
func (b *Browser) ReadPost(ctx context.Context, target string) (Page, error) {
	return b.read(ctx, "read_post", target, "post")
}
func (b *Browser) ReadOwnMetrics(ctx context.Context) (Metrics, error) {
	p, err := b.read(ctx, "read_own_metrics", "/metrics", "metrics")
	if err != nil {
		return Metrics{}, err
	}
	if p.Metrics == nil {
		return Metrics{}, operationError("read_own_metrics", ErrMalformedPage)
	}
	return *p.Metrics, nil
}
func (b *Browser) Post(ctx context.Context, text string) (string, error) {
	return b.write(ctx, "post", "/compose", "compose", text)
}
func (b *Browser) Reply(ctx context.Context, target, text string) (string, error) {
	return b.write(ctx, "reply", target, "post", text)
}
func (b *Browser) write(ctx context.Context, op, path, kind, text string) (threadURL string, err error) {
	start := b.now()
	defer func() { err = operationError(op, err); b.record(ctx, op, start, err) }()
	dest, err := b.target(path)
	if err != nil {
		return "", err
	}
	call, done, err := b.begin(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	if err = b.driver.navigate(call, dest); err != nil {
		return "", err
	}
	raw, err := b.driver.snapshot(call)
	if err != nil {
		return "", err
	}
	page, err := ParseSnapshot(raw, kind, b.base.String())
	if err != nil {
		return "", err
	}
	if page.Form == nil || !page.Form.Composer || !page.Form.Submit || !strings.EqualFold(page.Form.Method, "POST") {
		return "", ErrMalformedPage
	}
	if _, err = b.target(page.Form.Action); err != nil {
		return "", err
	}
	attempted, err := b.driver.submit(call, text)
	if err != nil {
		return "", errors.Join(ErrUnknownOutcome, err)
	}
	if !attempted {
		return "", ErrMalformedPage
	}
	raw, err = b.driver.acknowledgement(call)
	if err != nil {
		return "", errors.Join(ErrUnknownOutcome, err)
	}
	page, err = ParseSnapshot(raw, "submitted", b.base.String())
	if err != nil {
		return "", errors.Join(ErrUnknownOutcome, err)
	}
	return page.ThreadURL, nil
}

// Close always initiates cleanup even if its caller has already been canceled.
// It never removes the configured profile or any operator data.
func (b *Browser) Close(ctx context.Context) (err error) {
	start := b.now()
	defer func() { err = operationError("close", err); b.record(ctx, "close", start, err) }()
	b.closeOnce.Do(func() { b.mu.Lock(); b.closed = true; b.mu.Unlock(); b.closeErr = b.driver.close(ctx) })
	return b.closeErr
}
