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
	b := &Browser{timeout: opts.Timeout, agentID: opts.AgentID, logger: opts.Logger, now: opts.Now, gate: make(chan struct{}, 1)}
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
	d := &agentBrowserDriver{cli: client, base: base, timeout: b.timeout, release: release.close, sleep: sleepContext}
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
	once     sync.Once
	closeErr error
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

// submit uses the compact accessibility snapshot only for interaction refs. It
// requires exactly one textbox and one button, so a page cannot redirect the
// click to an unrelated control.
func (d *agentBrowserDriver) submit(ctx context.Context, text string) (bool, error) {
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
