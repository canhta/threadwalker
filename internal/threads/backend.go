// Package threads owns the Threads backend: the page reads and writes of one
// wake and the constructor the template's `threads.backend` selects. The
// harness must guard every call and reserve action intents before write calls.
package threads

import (
	"context"
	"errors"
	"fmt"
	"github.com/canhta/threadz/internal/stdx"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/canhta/threadz/internal/config"
)

// ErrUnknownOutcome means a submission may have reached the site. The harness
// must retain its reserved intent and stop the wake without retrying it.
var ErrUnknownOutcome = errors.New("unknown submission outcome")

// Backend is owned by one wake. Writes require an already guarded action
// intent. Every operation but ReadFeed is the official Threads API; the API has
// no home-feed endpoint, so ReadFeed reads the logged-in page.
type Backend interface {
	ReadFeed(context.Context) (Page, error)
	// ReadTopic reads a topic page (a Trend URL from this wake) or, with an empty
	// URL, the search page of query, in the logged-in browser.
	ReadTopic(ctx context.Context, topicURL, query string) (Page, error)
	// Search is the keyword search; recent asks for the newest posts of the
	// last hour instead of the top ones.
	Search(ctx context.Context, query string, recent bool) (Page, error)
	// ProfileLookup reads the public profile of a username.
	ProfileLookup(context.Context, string) (Profile, error)
	// ReadOwnPosts lists the account's own recent threads.
	ReadOwnPosts(context.Context) (Page, error)
	// ProfilePosts reads the recent public posts of a username, with numeric ids.
	ProfilePosts(context.Context, string) (Page, error)
	// ReadPost reads one post by its numeric API id.
	ReadPost(context.Context, string) (Page, error)
	// ReadReplies returns the replies to one own post, or the mentions of the
	// agent when the id is empty.
	ReadReplies(context.Context, string) (Page, error)
	// ReadOwnMetrics returns the account insights and the follower count.
	ReadOwnMetrics(context.Context) (Metrics, error)
	// ReadActionMetrics returns the insights of the own post with a media id.
	ReadActionMetrics(context.Context, string) (Metrics, error)
	PublishingLimit(context.Context) (Quota, error)
	// Post publishes a post under an optional topic tag (text, topic).
	Post(context.Context, string, string) (Published, error)
	// Reply publishes a reply to the numeric post id.
	Reply(context.Context, string, string) (Published, error)
	Close(context.Context) error
}

// Factory opens the backend one wake owns; that wake always closes it. A
// failed open returns no backend.
type Factory func(context.Context, config.Template) (Backend, error)

const (
	operationTimeout = 15 * time.Second
	startupTimeout   = 30 * time.Second
)

// NewBackend returns the factory for the template's configured Threads backend
// and whether that backend can publish. The stub backend serves a Memory and
// cannot publish. tokens is required for the api backend.
func NewBackend(tmpl config.Template, now func() time.Time, tokens *Tokens) (Factory, bool) {
	switch tmpl.Threads.Backend {
	case "api":
		return func(ctx context.Context, tmpl config.Template) (Backend, error) {
			return openLive(ctx, tmpl, now, tokens)
		}, true
	}
	return func(context.Context, config.Template) (Backend, error) { return NewMemory(), nil }, false
}

// Live serves a wake: the API for every own-account operation and, only on the
// first ReadFeed, one browser session for the feed and for replies to feed posts.
type Live struct {
	*API
	openBrowser func(context.Context) (*Browser, error)
	mu          sync.Mutex
	browser     *Browser
}

var _ Backend = (*Live)(nil)

func openLive(ctx context.Context, tmpl config.Template, now func() time.Time, tokens *Tokens) (Backend, error) {
	if tokens == nil {
		return nil, errors.New("opening threads api: token manager required")
	}
	if err := tokens.Ensure(ctx, tmpl.ID); err != nil {
		return nil, fmt.Errorf("opening threads api: %w", err)
	}
	api, err := NewAPI(APIOptions{UserID: tmpl.Threads.UserID, Tokens: tokens.Source(tmpl.ID), Wait: stdx.Sleep, Timeout: operationTimeout, Now: now})
	if err != nil {
		return nil, err
	}
	th := tmpl.Threads
	return &Live{API: api, openBrowser: func(ctx context.Context) (*Browser, error) {
		return NewAgentBrowser(ctx, AgentBrowserOptions{AgentID: tmpl.ID, BaseURL: th.BaseURL, ProfileDir: th.ProfileDir, ExecutablePath: th.ExecutablePath, AgentBrowserPath: th.AgentBrowserPath, Timeout: operationTimeout, StartupTimeout: startupTimeout, Now: now, NoSandbox: th.NoSandbox})
	}}, nil
}

// session returns the wake's one browser, opening it on first use.
func (l *Live) session(ctx context.Context) (*Browser, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.browser == nil {
		b, err := l.openBrowser(ctx)
		if err != nil {
			return nil, err
		}
		l.browser = b
	}
	return l.browser, nil
}

func (l *Live) ReadFeed(ctx context.Context) (Page, error) {
	b, err := l.session(ctx)
	if err != nil {
		return Page{}, err
	}
	return b.ReadFeed(ctx)
}

func (l *Live) ReadTopic(ctx context.Context, topicURL, query string) (Page, error) {
	b, err := l.session(ctx)
	if err != nil {
		return Page{}, err
	}
	return b.ReadTopic(ctx, topicURL, query)
}

// Reply answers a numeric post id through the API and a permalink from this
// wake's feed read through the browser. The reply's media id is read back from
// the account's own replies; when no reply matches, the id is empty and the
// reply is still confirmed.
func (l *Live) Reply(ctx context.Context, target, text string) (Published, error) {
	if digitsOnly(target) {
		return l.API.Reply(ctx, target, text)
	}
	l.mu.Lock()
	b := l.browser
	l.mu.Unlock()
	if b == nil {
		return Published{}, operationError("reply", ErrWriteRefused)
	}
	threadURL, err := b.Reply(ctx, target, text)
	if err != nil {
		return Published{}, err
	}
	id := ""
	if page, err := l.API.OwnReplies(ctx); err == nil {
		for _, p := range page.Posts {
			if strings.TrimSpace(p.Text) == strings.TrimSpace(text) {
				id = p.ID
				break
			}
		}
	}
	return Published{URL: threadURL, ID: id}, nil
}

// Close closes the browser only if a feed read opened one.
func (l *Live) Close(ctx context.Context) error {
	l.mu.Lock()
	b := l.browser
	l.mu.Unlock()
	if b == nil {
		return nil
	}
	return b.Close(ctx)
}

var ErrMetricsUnavailable = errors.New("action metrics unavailable")

// ErrMediaNotFound means the API answered 400 code 100 for a media id: the post
// may be deleted. Only the account's own post listing can confirm it.
var ErrMediaNotFound = errors.New("media not found")

// ErrCloseFailed means browser cleanup did not complete. It is a cleanup-only
// failure after a wake has already decided its outcome, never a wake failure.
var ErrCloseFailed = errors.New("browser close failed")

// ErrWriteRefused means a write stopped before the publish call or submit click. It is safe to
// report as a normal failure because no effect was attempted; it must not be
// promoted to an unknown outcome.
var ErrWriteRefused = errors.New("write refused before submit")

type transport interface {
	navigate(context.Context, string) error
	page(context.Context, string) (Page, error)
	submit(context.Context, string) (bool, error)
	acknowledgement(context.Context) (Page, error)
	// snapshot is the page the driver last read; empty before any read.
	snapshot() (origin, text string)
	close(context.Context) error
}

// StageError names the browser step that failed and carries the page
// snapshot the step last read, as untrusted data for the operator. Its message
// is its cause's: the snapshot never reaches an error string.
type StageError struct {
	Stage, Origin, Snapshot string
	Err                     error
}

// Browser steps a StageError names.
const (
	StageNavigate        = "navigate"
	StageReadPage        = "read_page"
	StageComposerMissing = "composer_missing"
	StageType            = "type"
	StageSubmit          = "submit"
	StageAcknowledgement = "acknowledgement"
)

func (e *StageError) Error() string { return e.Err.Error() }
func (e *StageError) Unwrap() error { return e.Err }

// StageOf returns the stage error err wraps, or nil.
func StageOf(err error) *StageError {
	var se *StageError
	if errors.As(err, &se) {
		return se
	}
	return nil
}

// fail names stage as the step that failed with err and attaches the
// snapshot the driver last read. A stage the driver already named stays.
func (b *Browser) fail(stage string, err error) error {
	if err == nil {
		return nil
	}
	if se := StageOf(err); se != nil {
		stage = se.Stage
	}
	origin, text := b.driver.snapshot()
	return &StageError{Stage: stage, Origin: origin, Snapshot: text, Err: err}
}

// Browser serializes its page operations; closing interrupts an active call.
type Browser struct {
	base      *url.URL
	feedPath  string
	driver    transport
	timeout   time.Duration
	gate      chan struct{}
	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

// Reason codes shared by logs, wake ends, dashboards and evals.
const (
	ReasonUnknownOutcome = "unknown_outcome"
	ReasonLoggedOut      = "logged_out"
	ReasonCaptcha        = "captcha"
	ReasonCancelled      = "cancelled"
	ReasonTimeout        = "timeout"
)

// reasons is ordered: ErrUnknownOutcome joins the causes it hides, so it wins.
var reasons = []struct {
	err    error
	reason string
}{
	{ErrUnknownOutcome, ReasonUnknownOutcome},
	{ErrLoggedOut, ReasonLoggedOut},
	{ErrCaptcha, ReasonCaptcha},
	// A refused write names the stage: the page read before it reports its own cause.
	{ErrWriteRefused, "write_refused"},
	{ErrUnknownPage, "unknown_page"},
	{ErrMalformedPage, "malformed_page"},
	{ErrMetricsUnavailable, "metrics_unavailable"},
	{ErrMediaNotFound, "media_not_found"},
	{ErrQuotaExhausted, "quota_exhausted"},
	{ErrAPI, "api_error"},
	{ErrCloseFailed, "close_failed"},
	{context.Canceled, ReasonCancelled},
	{context.DeadlineExceeded, ReasonTimeout},
}

// Reason returns the reason code of the first sentinel err wraps, or "" when
// err wraps none; callers name their own fallback.
func Reason(err error) string {
	for _, r := range reasons {
		if errors.Is(err, r.err) {
			return r.reason
		}
	}
	return ""
}

// operationError preserves only safe error identity, never transport diagnostics.
func operationError(op string, err error) error {
	if err == nil {
		return nil
	}
	cause := errors.New("transport_failure")
	for _, r := range reasons {
		if errors.Is(err, r.err) {
			cause = r.err
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
	if se := StageOf(err); se != nil {
		cause = &StageError{Stage: se.Stage, Origin: se.Origin, Snapshot: se.Snapshot, Err: cause}
	}
	return fmt.Errorf("threads %s: %w", op, cause)
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
	defer func() { err = operationError(op, err) }()
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
		return Page{}, b.fail(StageNavigate, err)
	}
	page, err = b.driver.page(call, kind)
	return page, b.fail(StageReadPage, err)
}
func (b *Browser) ReadFeed(ctx context.Context) (Page, error) {
	return b.read(ctx, "read_feed", pathOr(b.feedPath, "/feed"), "feed")
}

// topicPostsMax bounds the posts one topic read returns.
const topicPostsMax = 20

// ReadTopic opens a topic page by its Trend URL, or the search page of query.
func (b *Browser) ReadTopic(ctx context.Context, topicURL, query string) (Page, error) {
	path := "/search?serp_type=default&q=" + url.QueryEscape(query)
	if topicURL != "" {
		u, err := url.Parse(topicURL)
		if err != nil || !sameOrigin(b.base, u) || u.Path != "/search" {
			return Page{}, operationError("read_topic", ErrMalformedPage)
		}
		path = "/search?" + u.RawQuery
	}
	page, err := b.read(ctx, "read_topic", path, "topic")
	if len(page.Posts) > topicPostsMax {
		page.Posts = page.Posts[:topicPostsMax]
	}
	return page, err
}

// Reply submits text in the inline composer of the post page at target and
// returns the permalink of the new reply. Once the submit click is sent, any
// failure is ErrUnknownOutcome and is never retried.
func (b *Browser) Reply(ctx context.Context, target, text string) (threadURL string, err error) {
	defer func() { err = operationError("reply", err) }()
	// The guard matched the canonical permalink; only its path is used, so a
	// threads.net link still opens on the configured origin.
	u, err := url.Parse(target)
	if err != nil {
		return "", ErrMalformedPage
	}
	dest, err := b.target(u.Path)
	if err != nil {
		return "", err
	}
	call, done, err := b.begin(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	if err = b.driver.navigate(call, dest); err != nil {
		return "", b.fail(StageNavigate, err)
	}
	page, err := b.driver.page(call, "post")
	if err != nil {
		return "", b.fail(StageReadPage, err)
	}
	if page.Form == nil || !page.Form.Composer || !page.Form.Submit {
		return "", b.fail(StageComposerMissing, ErrMalformedPage)
	}
	attempted, err := b.driver.submit(call, text)
	if err != nil {
		if errors.Is(err, ErrWriteRefused) {
			return "", b.fail(StageSubmit, err)
		}
		return "", b.fail(StageSubmit, errors.Join(ErrUnknownOutcome, err))
	}
	if !attempted {
		return "", b.fail(StageComposerMissing, ErrMalformedPage)
	}
	page, err = b.driver.acknowledgement(call)
	if err == nil && page.ThreadURL == "" {
		err = ErrMalformedPage
	}
	if err != nil {
		return "", b.fail(StageAcknowledgement, errors.Join(ErrUnknownOutcome, err))
	}
	return page.ThreadURL, nil
}

// Close always initiates cleanup even if its caller has already been canceled.
// It never removes the configured profile or any operator data.
func pathOr(configured, fallback string) string {
	if configured != "" {
		return configured
	}
	return fallback
}

func (b *Browser) Close(ctx context.Context) (err error) {
	defer func() { err = operationError("close", err) }()
	b.closeOnce.Do(func() { b.mu.Lock(); b.closed = true; b.mu.Unlock(); b.closeErr = b.driver.close(ctx) })
	return b.closeErr
}
