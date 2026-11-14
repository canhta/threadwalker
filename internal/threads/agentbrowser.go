package threads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/canhta/threadz/internal/stdx"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/canhta/threadz/internal/agentbrowser"
	"unicode/utf8"
)

const (
	// threadsRenderPoll is the pause between snapshots of a page that Threads is
	// still rendering on the client.
	threadsRenderPoll = 150 * time.Millisecond
	// threadsRenderWait bounds how long one live read waits for the measured
	// navigation and column body to render. It stays inside the 15s per-operation
	// browser timeout the runtime configures, and a rendered page, a logged-out
	// page and a captcha return on the first snapshot.
	threadsRenderWait = 8 * time.Second
	// threadsScrollMax bounds the scroll steps of one list read, and
	// threadsListWant is the post count that ends it early.
	threadsScrollMax = 6
	threadsListWant  = 20
)

// AgentBrowserOptions configures the agent-browser CLI transport: BaseURL (a
// threads.com or threads.net origin), ProfileDir, ExecutablePath (Chrome) and
// NoSandbox. AgentBrowserPath is the pinned CLI binary.
type AgentBrowserOptions struct {
	AgentID          string
	BaseURL          string
	ProfileDir       string
	ExecutablePath   string
	AgentBrowserPath string
	Timeout          time.Duration
	StartupTimeout   time.Duration
	Now              func() time.Time
	NoSandbox        bool
}

// agentBrowserCLI is the bounded surface the transport may use. It deliberately
// has no JavaScript, generic command, cookie, storage or credential method.
type agentBrowserCLI interface {
	Start(context.Context) error
	Open(context.Context, string) (agentbrowser.Page, error)
	Snapshot(context.Context) (agentbrowser.Snapshot, error)
	Fill(context.Context, string, string) error
	Focus(context.Context, string) error
	Type(context.Context, string) error
	PressShiftEnter(context.Context) error
	Click(context.Context, string) error
	ScrollDown(context.Context) error
	SetCookie(context.Context, agentbrowser.Cookie) error
	Close(context.Context) error
}

// NewAgentBrowser starts one owned agent-browser session with a bounded startup.
// The profile is canonicalized and exclusively leased for the wake. The session
// and daemon namespace are derived from the agent ID so agents never share
// cookies, storage or a daemon socket.
func NewAgentBrowser(ctx context.Context, opts AgentBrowserOptions) (result *Browser, err error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	b := &Browser{timeout: opts.Timeout, gate: make(chan struct{}, 1), feedPath: "/"}
	defer func() { err = operationError("launch", err) }()
	if opts.Timeout == 0 {
		b.timeout = 10 * time.Second
	}
	if opts.StartupTimeout == 0 {
		opts.StartupTimeout = 15 * time.Second
	}
	base, parseErr := url.Parse(opts.BaseURL)
	if parseErr != nil || base == nil || base.User != nil || base.Hostname() == "" || (base.Scheme != "https" && base.Scheme != "http") || base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") || strings.ContainsAny(opts.BaseURL, "\\\r\n\t") || opts.ProfileDir == "" || opts.ExecutablePath == "" || opts.AgentBrowserPath == "" || opts.AgentID == "" || b.timeout <= 0 || opts.StartupTimeout <= 0 {
		return nil, ErrMalformedPage
	}
	if !isThreadsHost(base) {
		return nil, ErrMalformedPage
	}
	b.base = base
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	release, err := leaseProfile(opts.ProfileDir, opts.AgentID)
	if err != nil {
		return nil, err
	}
	session, namespace := agentBrowserIdentifiers(opts.AgentID)
	client, err := agentbrowser.New(agentbrowser.Options{
		BinaryPath:     opts.AgentBrowserPath,
		ChromePath:     opts.ExecutablePath,
		ProfileDir:     release.path,
		Session:        session,
		Namespace:      namespace,
		Timeout:        b.timeout,
		StartupTimeout: opts.StartupTimeout,
		NoSandbox:      opts.NoSandbox,
	})
	if err != nil {
		release.close()
		return nil, err
	}
	d := &agentBrowserDriver{cli: client, base: base, timeout: b.timeout, release: release.close, sleep: stdx.Sleep, now: opts.Now, renderWait: threadsRenderWait}
	b.driver = d
	startup, cancel := context.WithTimeout(ctx, opts.StartupTimeout)
	defer cancel()
	if err = client.Start(startup); err != nil {
		_ = d.close(context.Background())
		return nil, err
	}
	return b, nil
}

// agentBrowserIdentifiers derives a stable, charset-safe session and daemon
// namespace from one operator-configured agent ID.
func agentBrowserIdentifiers(agentID string) (string, string) {
	sum := sha256.Sum256([]byte(agentID))
	suffix := hex.EncodeToString(sum[:5])
	var name strings.Builder
	for _, r := range agentID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			name.WriteRune(r)
		} else {
			name.WriteByte('-')
		}
		if name.Len() >= 8 {
			break
		}
	}
	base := strings.Trim(name.String(), "-")
	if base == "" {
		base = "agent"
	}
	return "tw-" + base + "-" + suffix, "tw-" + suffix
}

var profiles = struct {
	sync.Mutex
	active map[string]bool
}{active: map[string]bool{}}

type profileLease struct{ path string }

func leaseProfile(path, agentID string) (*profileLease, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("profile_path")
	}
	if err = os.MkdirAll(absolute, 0700); err != nil {
		return nil, errors.New("profile_create")
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, errors.New("profile_path")
	}
	profiles.Lock()
	defer profiles.Unlock()
	if profiles.active[absolute] {
		return nil, errors.New("profile_busy")
	}
	marker := filepath.Join(absolute, ".threadz-agent")
	owner, createErr := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if createErr == nil {
		_, writeErr := owner.WriteString(agentID)
		closeErr := owner.Close()
		if writeErr != nil || closeErr != nil {
			return nil, errors.New("profile_owner")
		}
	} else if !errors.Is(createErr, os.ErrExist) {
		return nil, errors.New("profile_owner")
	}
	data, readErr := os.ReadFile(marker)
	if readErr != nil || string(data) != agentID {
		return nil, errors.New("profile_owner")
	}
	profiles.active[absolute] = true
	return &profileLease{absolute}, nil
}
func (p *profileLease) close() { profiles.Lock(); delete(profiles.active, p.path); profiles.Unlock() }

type agentBrowserDriver struct {
	cli     agentBrowserCLI
	base    *url.URL
	timeout time.Duration
	release func()
	sleep   func(context.Context, time.Duration) error
	now     func() time.Time
	// renderWait bounds how long a live read retries a page that is still
	// rendering. A zero value means no wait: one snapshot, then the parse result.
	renderWait time.Duration
	last       agentbrowser.Snapshot
	once       sync.Once
	closeErr   error

	// Acknowledgement state, set immediately before the submit click.
	pendingText  string
	pendingOwn   string
	pendingKnown map[string]bool
}

var _ transport = (*agentBrowserDriver)(nil)

func (d *agentBrowserDriver) navigate(ctx context.Context, dest string) error {
	page, err := d.cli.Open(ctx, dest)
	if err != nil {
		return err
	}
	u, parseErr := url.Parse(page.URL)
	if parseErr != nil || !sameOrigin(d.base, u) {
		return ErrMalformedPage
	}
	return nil
}

// take reads a snapshot and keeps it for the evidence of a failure.
func (d *agentBrowserDriver) take(ctx context.Context) (agentbrowser.Snapshot, error) {
	snapshot, err := d.cli.Snapshot(ctx)
	if err == nil {
		d.last = snapshot
	}
	return snapshot, err
}

func (d *agentBrowserDriver) snapshot() (origin, text string) { return d.last.Origin, d.last.Text }

func (d *agentBrowserDriver) page(ctx context.Context, kind string) (Page, error) {
	return d.threadsPage(ctx, kind)
}

// threadsPage uses the full compact accessibility tree, not an interactive-only
// snapshot, so post bodies are present. Threads renders the authenticated
// navigation and the column body on the client, so the first snapshot after open
// can legitimately precede them; those snapshots are retried within a bounded
// window instead of failing closed as unknown_page. Logged-out, captcha and
// malformed pages fail closed on the first snapshot. A feed or topic page renders
// only the first few posts and loads the rest as it scrolls, dropping the ones
// scrolled past, so those reads scroll and accumulate posts.
func (d *agentBrowserDriver) threadsPage(ctx context.Context, kind string) (Page, error) {
	page, err := d.threadsSnapshot(ctx, kind)
	if err != nil || (kind != "feed" && kind != "topic") {
		return page, err
	}
	seen := map[string]bool{}
	for _, p := range page.Posts {
		seen[p.URL] = true
	}
	deadline := d.now().Add(d.renderWait)
	for i := 0; i < threadsScrollMax && len(page.Posts) < threadsListWant && d.now().Before(deadline); i++ {
		if d.cli.ScrollDown(ctx) != nil || d.sleep(ctx, threadsRenderPoll) != nil {
			break
		}
		next, err := d.threadsSnapshot(ctx, kind)
		if err != nil {
			break // keep the posts already read
		}
		fresh := 0
		for _, p := range next.Posts {
			if !seen[p.URL] {
				seen[p.URL] = true
				page.Posts = append(page.Posts, p)
				fresh++
			}
		}
		if fresh == 0 {
			break
		}
	}
	return page, nil
}

// threadsSnapshot takes one parsed snapshot, retrying a page still rendering.
func (d *agentBrowserDriver) threadsSnapshot(ctx context.Context, kind string) (Page, error) {
	deadline := d.now().Add(d.renderWait)
	for {
		snapshot, err := d.take(ctx)
		if err != nil {
			return Page{}, err
		}
		u, parseErr := url.Parse(snapshot.Origin)
		if parseErr != nil || !sameOrigin(d.base, u) {
			return Page{}, ErrMalformedPage
		}
		page, parseErr := parseThreadsPage(snapshot.Text, snapshot.Origin, kind, d.base.String())
		switch {
		case parseErr == nil:
			// A post page shows its posts before the inline composer, which
			// Threads renders after them; a reply needs the composer, so wait
			// for it within the same bounded window.
			if kind == "post" {
				if page.Form != nil {
					return page, nil
				}
			} else if len(page.Posts) > 0 || !threadsLoadingText(snapshot.Text) {
				return page, nil
			}
		case errors.Is(parseErr, ErrUnknownPage):
			// The measured structure is not rendered yet. Keep waiting.
		default:
			return Page{}, parseErr
		}
		if !d.now().Before(deadline) {
			if parseErr != nil {
				return Page{}, parseErr
			}
			// A page that parsed but never left its loading state is returned as
			// read, so an empty feed or search stays a real empty result.
			return page, nil
		}
		if err = d.sleep(ctx, threadsRenderPoll); err != nil {
			return Page{}, err
		}
	}
}

// threadsSubmitLive fills the recognized composer once, re-reads the same
// container for its submit control, records the pre-click page state, clicks
// once, and arms acknowledgement polling. It never retries a click.
func (d *agentBrowserDriver) threadsSubmitLive(ctx context.Context, text string) (bool, error) {
	refuse := func(stage string, err error) (bool, error) {
		return false, errors.Join(ErrWriteRefused, &StageError{Stage: stage, Err: err})
	}
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > stdx.MaxThreadsPostRunes {
		return refuse(StageType, ErrMalformedPage)
	}
	snapshot, err := d.take(ctx)
	if err != nil {
		return refuse(StageReadPage, err)
	}
	root, err := d.threadsRoot(snapshot)
	if err != nil {
		return refuse(StageReadPage, err)
	}
	container, ok := threadsComposerContainer(root)
	if !ok {
		return refuse(StageComposerMissing, ErrUnknownPage)
	}
	textbox, ok := threadsComposerTextbox(container)
	if !ok || !validAgentBrowserRef(textbox.ref) {
		return refuse(StageComposerMissing, ErrMalformedPage)
	}
	if strings.ContainsAny(text, "\r\n") {
		if err = d.threadsTypeMultiline(ctx, textbox.ref, text); err != nil {
			return refuse(StageType, err)
		}
	} else if err = d.cli.Fill(ctx, textbox.ref, text); err != nil {
		return refuse(StageType, err)
	}
	var submit *axNode
	for attempt := 0; ; attempt++ {
		snapshot, err = d.take(ctx)
		if err != nil {
			return refuse(StageSubmit, err)
		}
		root, err = d.threadsRoot(snapshot)
		if err != nil {
			return refuse(StageSubmit, err)
		}
		textbox, submit, ok = threadsComposerControls(root)
		if ok && textbox != nil && validAgentBrowserRef(textbox.ref) && validAgentBrowserRef(submit.ref) {
			break
		}
		if attempt >= 9 {
			return refuse(StageSubmit, ErrMalformedPage)
		}
		if err = d.sleep(ctx, 150*time.Millisecond); err != nil {
			return refuse(StageSubmit, err)
		}
	}
	d.pendingText = text
	d.pendingOwn = threadsOwnUsername(root)
	d.pendingKnown = map[string]bool{}
	root.walk(func(n *axNode) bool {
		if n.role == "generic" {
			if raw, _, ok := threadsPostBlockPermalink(n, d.base); ok {
				d.pendingKnown[raw] = true
			}
		}
		return true
	})
	if err = d.cli.Click(ctx, submit.ref); err != nil {
		return false, err
	}
	return true, nil
}

// threadsTypeMultiline reproduces the measured multi-line input path: focus the
// composer, type each line, and press Shift+Enter between lines. A plain newline
// keystroke submits the inline reply composer instead of inserting a break.
func (d *agentBrowserDriver) threadsTypeMultiline(ctx context.Context, ref, text string) error {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	if !validAgentBrowserRef(ref) || strings.TrimSpace(normalized) == "" {
		return ErrMalformedPage
	}
	lines := strings.Split(normalized, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "--") {
			// The pinned CLI would parse this as an option, not text. Refuse
			// before the click instead of publishing altered content.
			return ErrMalformedPage
		}
	}
	if err := d.cli.Focus(ctx, ref); err != nil {
		return err
	}
	for i, line := range lines {
		if line != "" {
			if err := d.cli.Type(ctx, line); err != nil {
				return err
			}
		}
		if i < len(lines)-1 {
			if err := d.cli.PressShiftEnter(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// threadsAcknowledgement polls the measured acknowledgement shapes after one
// click. A missing acknowledgement is returned as the caller's deadline error,
// which the backend records as an unknown outcome and never retries.
func (d *agentBrowserDriver) threadsAcknowledgement(ctx context.Context) (Page, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Page{}, err
		}
		snapshot, err := d.take(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return Page{}, ctx.Err()
			}
			return Page{}, err
		}
		root, err := d.threadsRoot(snapshot)
		if err != nil {
			if errors.Is(err, ErrLoggedOut) || errors.Is(err, ErrCaptcha) || errors.Is(err, ErrMalformedPage) {
				return Page{}, err
			}
			if err = d.sleep(ctx, 150*time.Millisecond); err != nil {
				return Page{}, err
			}
			continue
		}
		if u, ok := threadsAckURL(root, d.base, d.pendingOwn, d.pendingText, d.pendingKnown); ok {
			return Page{Kind: "submitted", ThreadURL: u}, nil
		}
		if err = d.sleep(ctx, 150*time.Millisecond); err != nil {
			return Page{}, err
		}
	}
}

func (d *agentBrowserDriver) submit(ctx context.Context, text string) (bool, error) {
	return d.threadsSubmitLive(ctx, text)
}

func (d *agentBrowserDriver) acknowledgement(ctx context.Context) (Page, error) {
	return d.threadsAcknowledgement(ctx)
}

// threadsRoot parses and classifies one live snapshot without guessing. A page
// that is neither authenticated, logged out nor a captcha remains unknown to the
// caller.
func (d *agentBrowserDriver) threadsRoot(snapshot agentbrowser.Snapshot) (*axNode, error) {
	u, err := url.Parse(snapshot.Origin)
	if err != nil || !sameOrigin(d.base, u) {
		return nil, ErrMalformedPage
	}
	root, err := parseAXTree(snapshot.Text)
	if err != nil {
		return nil, ErrMalformedPage
	}
	if threadsCaptcha(root) {
		return nil, ErrCaptcha
	}
	if !threadsAuthenticated(root) && threadsLoggedOut(root) {
		return nil, ErrLoggedOut
	}
	return root, nil
}

func (d *agentBrowserDriver) close(ctx context.Context) error {
	d.once.Do(func() {
		closeTimeout := d.timeout
		if closeTimeout < 20*time.Second {
			closeTimeout = 20 * time.Second
		}
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancel()
		// The profile lease belongs to this process, not to the daemon. Release
		// it even when the CLI cannot confirm its own session shutdown.
		if d.release != nil {
			defer d.release()
		}
		if err := d.cli.Close(closeCtx); err != nil {
			d.closeErr = errors.Join(ErrCloseFailed, err)
		}
	})
	return d.closeErr
}

func validAgentBrowserRef(ref string) bool {
	if len(ref) < 2 || len(ref) > 12 || ref[0] != 'e' {
		return false
	}
	for _, r := range ref[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
