package threads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/canhta/threadwalker/internal/agentbrowser"
)

// AgentBrowserOptions configures the agent-browser CLI transport. It keeps the
// existing browser template fields: BaseURL, ProfileDir, ExecutablePath (Chrome)
// and NoSandbox. AgentBrowserPath is the pinned CLI binary.
type AgentBrowserOptions struct {
	AgentID          string
	BaseURL          string
	ProfileDir       string
	ExecutablePath   string
	AgentBrowserPath string
	Timeout          time.Duration
	StartupTimeout   time.Duration
	Logger           *slog.Logger
	Now              func() time.Time
	NoSandbox        bool
	// PageProfile selects the measured page contract: "fixture" (default) or
	// "threads" for read-only live Threads accessibility mapping.
	PageProfile string
}

// agentBrowserCLI is the bounded surface the transport may use. It deliberately
// has no JavaScript, generic command, cookie, storage or credential method.
type agentBrowserCLI interface {
	Start(context.Context) error
	Open(context.Context, string) (agentbrowser.Page, error)
	Read(context.Context) (agentbrowser.Document, error)
	Snapshot(context.Context) (agentbrowser.Snapshot, error)
	Fill(context.Context, string, string) error
	Click(context.Context, string) error
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
	start := opts.Now()
	b := &Browser{timeout: opts.Timeout, agentID: opts.AgentID, logger: opts.Logger, now: opts.Now, gate: make(chan struct{}, 1), feedPath: "/feed", metricsPath: "/metrics", postPath: "/compose"}
	defer func() { err = operationError("launch", err); b.record(ctx, "launch", start, err) }()
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
	if opts.PageProfile == "" {
		opts.PageProfile = pageProfileFixture
	}
	switch opts.PageProfile {
	case pageProfileFixture:
	case pageProfileThreads:
		if !isThreadsHost(base) {
			return nil, ErrMalformedPage
		}
		b.feedPath = "/"
		b.metricsPath = "/insights/"
		b.postPath = "/"
	default:
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
	d := &agentBrowserDriver{cli: client, base: base, timeout: b.timeout, release: release.close, sleep: sleepContext, profile: opts.PageProfile}
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

type agentBrowserDriver struct {
	cli      agentBrowserCLI
	base     *url.URL
	timeout  time.Duration
	release  func()
	sleep    func(context.Context, time.Duration) error
	profile  string
	once     sync.Once
	closeErr error

	// Live write acknowledgement state, set immediately before the submit click.
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

func (d *agentBrowserDriver) page(ctx context.Context, kind string) (Page, error) {
	if d.profile == pageProfileThreads {
		return d.threadsPage(ctx, kind)
	}
	document, err := d.cli.Read(ctx)
	if err != nil {
		return Page{}, err
	}
	if document.Truncated {
		return Page{}, ErrMalformedPage
	}
	pageURL := document.FinalURL
	if pageURL == "" {
		pageURL = document.URL
	}
	u, parseErr := url.Parse(pageURL)
	if parseErr != nil || !sameOrigin(d.base, u) {
		return Page{}, ErrMalformedPage
	}
	return parseAgentBrowserDocument(document.Content, kind, d.base.String())
}

// threadsPage uses the full compact accessibility tree, not an interactive-only
// snapshot, so post bodies are present.
func (d *agentBrowserDriver) threadsPage(ctx context.Context, kind string) (Page, error) {
	if kind == "compose" {
		return d.threadsComposePage(ctx)
	}
	for {
		snapshot, err := d.cli.Snapshot(ctx)
		if err != nil {
			return Page{}, err
		}
		u, parseErr := url.Parse(snapshot.Origin)
		if parseErr != nil || !sameOrigin(d.base, u) {
			return Page{}, ErrMalformedPage
		}
		page, parseErr := parseThreadsPage(snapshot.Text, snapshot.Origin, kind, d.base.String())
		if parseErr != nil {
			return Page{}, parseErr
		}
		if len(page.Posts) > 0 || kind == "metrics" || kind == "action_metrics" || !threadsLoadingText(snapshot.Text) {
			return page, nil
		}
		if err = d.sleep(ctx, 150*time.Millisecond); err != nil {
			return Page{}, err
		}
	}
}

// threadsComposePage opens the measured New thread dialog from the live feed or
// profile page, then returns the recognized composer form. The dialog is opened
// only after the guard has already reserved and audited the write intent.
func (d *agentBrowserDriver) threadsComposePage(ctx context.Context) (Page, error) {
	opened := false
	for {
		snapshot, err := d.cli.Snapshot(ctx)
		if err != nil {
			return Page{}, err
		}
		root, err := d.threadsRoot(snapshot)
		if err != nil {
			return Page{}, err
		}
		if form, ok := threadsComposer(root, snapshot.Origin, d.base); ok {
			return Page{Kind: "compose", Form: &form}, nil
		}
		if !opened {
			ref, ok := threadsButtonRef(root, "New thread")
			if !ok {
				return Page{}, ErrUnknownPage
			}
			if err = d.cli.Click(ctx, ref); err != nil {
				return Page{}, err
			}
			opened = true
		}
		if err = d.sleep(ctx, 150*time.Millisecond); err != nil {
			return Page{}, err
		}
	}
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

func threadsButtonRef(root *axNode, name string) (string, bool) {
	var refs []string
	root.walk(func(n *axNode) bool {
		if n.role == "button" && strings.EqualFold(n.name, name) && validAgentBrowserRef(n.ref) {
			refs = append(refs, n.ref)
		}
		return true
	})
	if len(refs) != 1 {
		return "", false
	}
	return refs[0], true
}

// threadsSubmitLive fills the recognized composer once, re-reads the same
// container for its submit control, records the pre-click page state, clicks
// once, and arms acknowledgement polling. It never retries a click.
func (d *agentBrowserDriver) threadsSubmitLive(ctx context.Context, text string) (bool, error) {
	refuse := func(err error) (bool, error) {
		return false, errors.Join(ErrWriteRefused, err)
	}
	if strings.TrimSpace(text) == "" || len([]rune(text)) > 500 {
		return refuse(ErrMalformedPage)
	}
	snapshot, err := d.cli.Snapshot(ctx)
	if err != nil {
		return refuse(err)
	}
	root, err := d.threadsRoot(snapshot)
	if err != nil {
		return refuse(err)
	}
	container, ok := threadsComposerContainer(root)
	if !ok {
		return refuse(ErrUnknownPage)
	}
	textbox, ok := threadsComposerTextbox(container)
	if !ok || !validAgentBrowserRef(textbox.ref) {
		return refuse(ErrMalformedPage)
	}
	if err = d.cli.Fill(ctx, textbox.ref, text); err != nil {
		return refuse(err)
	}
	var submit *axNode
	for attempt := 0; ; attempt++ {
		snapshot, err = d.cli.Snapshot(ctx)
		if err != nil {
			return refuse(err)
		}
		root, err = d.threadsRoot(snapshot)
		if err != nil {
			return refuse(err)
		}
		textbox, submit, ok = threadsComposerControls(root)
		if ok && textbox != nil && validAgentBrowserRef(textbox.ref) && validAgentBrowserRef(submit.ref) {
			break
		}
		if attempt >= 9 {
			return refuse(ErrMalformedPage)
		}
		if err = d.sleep(ctx, 150*time.Millisecond); err != nil {
			return refuse(err)
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

// threadsAcknowledgement polls the measured acknowledgement shapes after one
// click. A missing acknowledgement is returned as the caller's deadline error,
// which the backend records as an unknown outcome and never retries.
func (d *agentBrowserDriver) threadsAcknowledgement(ctx context.Context) (Page, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Page{}, err
		}
		snapshot, err := d.cli.Snapshot(ctx)
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

// submit uses the compact accessibility snapshot only for interaction refs. It
// requires exactly one textbox and one button, so a page cannot redirect the
// click to an unrelated control.
func (d *agentBrowserDriver) submit(ctx context.Context, text string) (bool, error) {
	if d.profile == pageProfileThreads {
		return d.threadsSubmitLive(ctx, text)
	}
	snapshot, err := d.cli.Snapshot(ctx)
	if err != nil {
		return false, err
	}
	origin, parseErr := url.Parse(snapshot.Origin)
	if parseErr != nil || !sameOrigin(d.base, origin) {
		return false, nil
	}
	textbox, button, err := composerRefs(snapshot)
	if err != nil {
		return false, nil
	}
	if err = d.cli.Fill(ctx, textbox, text); err != nil {
		return false, err
	}
	if err = d.cli.Click(ctx, button); err != nil {
		return false, err
	}
	return true, nil
}

func (d *agentBrowserDriver) acknowledgement(ctx context.Context) (Page, error) {
	if d.profile == pageProfileThreads {
		return d.threadsAcknowledgement(ctx)
	}
	for {
		if err := ctx.Err(); err != nil {
			return Page{}, err
		}
		document, err := d.cli.Read(ctx)
		if err == nil {
			if document.Truncated {
				return Page{}, ErrMalformedPage
			}
			pageURL := document.FinalURL
			if pageURL == "" {
				pageURL = document.URL
			}
			if u, parseErr := url.Parse(pageURL); parseErr != nil || !sameOrigin(d.base, u) {
				return Page{}, ErrMalformedPage
			}
			page, parseErr := parseAgentBrowserDocument(document.Content, "submitted", d.base.String())
			switch {
			case parseErr == nil:
				return page, nil
			case errors.Is(parseErr, ErrUnknownPage):
				// The acknowledgement page has not appeared yet.
			default:
				return Page{}, parseErr
			}
		} else if ctx.Err() != nil {
			return Page{}, ctx.Err()
		}
		if err = d.sleep(ctx, 100*time.Millisecond); err != nil {
			return Page{}, err
		}
	}
}

func (d *agentBrowserDriver) close(ctx context.Context) error {
	d.once.Do(func() {
		closeTimeout := d.timeout
		if closeTimeout < 20*time.Second {
			closeTimeout = 20 * time.Second
		}
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancel()
		d.closeErr = d.cli.Close(closeCtx)
		d.release()
	})
	return d.closeErr
}

func composerRefs(snapshot agentbrowser.Snapshot) (textbox, button string, err error) {
	textboxes, buttons := 0, 0
	for ref, info := range snapshot.Refs {
		if !validAgentBrowserRef(ref) {
			continue
		}
		switch info.Role {
		case "textbox":
			textboxes++
			textbox = ref
		case "button":
			buttons++
			button = ref
		}
	}
	if textboxes != 1 || buttons != 1 {
		return "", "", ErrMalformedPage
	}
	return textbox, button, nil
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

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
