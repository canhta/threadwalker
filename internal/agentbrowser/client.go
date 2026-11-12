// Package agentbrowser is the harness-owned client for the pinned
// vercel-labs/agent-browser CLI. It exposes only fixed, validated commands and
// bounded machine-readable output. It never exposes a shell, JavaScript
// evaluation, arbitrary flags, CDP attachment or credential access.
package agentbrowser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Version is the pinned release this adapter was verified against. A different
// binary fails closed instead of silently changing protocol or flags.
const Version = "0.42.0"

// DefaultMaxOutputBytes bounds one CLI response. A page document is a control
// envelope plus bounded text, never a page dump.
const DefaultMaxOutputBytes = 1 << 20

// Stable safe errors. Callers map these to threads sentinels; raw CLI
// diagnostics, page text and URLs never enter an error message.
var (
	ErrUnavailable = errors.New("agent-browser unavailable")
	ErrUnsafe      = errors.New("agent-browser unsafe configuration")
	ErrVersion     = errors.New("agent-browser version mismatch")
	ErrProtocol    = errors.New("agent-browser protocol error")
	ErrOutputLimit = errors.New("agent-browser output limit")
	ErrCommand     = errors.New("agent-browser command failed")
)

// Options configures one harness-owned session. BinaryPath, ChromePath and
// ProfileDir are required. Session and Namespace are derived by the caller and
// must already be safe identifiers.
type Options struct {
	BinaryPath     string
	ChromePath     string
	ProfileDir     string
	Session        string
	Namespace      string
	Timeout        time.Duration
	StartupTimeout time.Duration
	MaxOutputBytes int
	NoSandbox      bool
	Headed         bool
}

// Page is the bounded result of a navigation.
type Page struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

// Document is the bounded rendered-DOM read of the active tab. Content is
// untrusted page text and is parsed only by the versioned fixture contract.
type Document struct {
	Content   string `json:"content"`
	URL       string `json:"url"`
	FinalURL  string `json:"finalUrl"`
	Source    string `json:"source"`
	Truncated bool   `json:"truncated"`
}

// Ref is one accessibility element reference.
type Ref struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// Snapshot is a compact, noninteractive accessibility snapshot.
type Snapshot struct {
	Origin string         `json:"origin"`
	Text   string         `json:"snapshot"`
	Refs   map[string]Ref `json:"refs"`
}

type envelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   *string         `json:"error"`
	Warning *string         `json:"warning"`
}

type openData struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

type readData struct {
	Content   string `json:"content"`
	URL       string `json:"url"`
	FinalURL  string `json:"finalUrl"`
	Source    string `json:"source"`
	Truncated bool   `json:"truncated"`
}

type snapshotData struct {
	Origin string         `json:"origin"`
	Text   string         `json:"snapshot"`
	Refs   map[string]Ref `json:"refs"`
}

type sessionData struct {
	Sessions []string `json:"sessions"`
}

type infoData struct {
	PID    int  `json:"pid"`
	Active bool `json:"active"`
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Client owns one agent-browser session and its daemon for one wake.
type Client struct {
	binary     string
	chrome     string
	profile    string
	session    string
	namespace  string
	configPath string
	timeout    time.Duration
	startup    time.Duration
	maxOutput  int
	noSandbox  bool
	headed     bool

	mu        sync.Mutex
	started   bool
	closed    bool
	daemonPID int
	kill      func(int) error
	once      sync.Once
	closeErr  error
}

// New validates configuration and writes the harness-owned config file. It does
// not launch a browser.
func New(opts Options) (*Client, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = 15 * time.Second
	}
	if opts.StartupTimeout <= 0 {
		opts.StartupTimeout = 30 * time.Second
	}
	if opts.MaxOutputBytes == 0 {
		opts.MaxOutputBytes = DefaultMaxOutputBytes
	}
	if opts.MaxOutputBytes < 4096 || opts.MaxOutputBytes > 8<<20 {
		return nil, ErrUnsafe
	}
	for _, path := range []string{opts.BinaryPath, opts.ChromePath} {
		if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
			return nil, ErrUnsafe
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return nil, ErrUnsafe
		}
	}
	if !filepath.IsAbs(opts.ProfileDir) || strings.ContainsAny(opts.ProfileDir, "\x00\r\n") {
		return nil, ErrUnsafe
	}
	if !identifier.MatchString(opts.Session) || !identifier.MatchString(opts.Namespace) {
		return nil, ErrUnsafe
	}
	if err := os.MkdirAll(opts.ProfileDir, 0700); err != nil {
		return nil, ErrUnsafe
	}
	configPath := filepath.Join(opts.ProfileDir, ".threadwalker-agent-browser.json")
	// --config bypasses user/project discovery so an operator file cannot add
	// plugins, extensions, state replay or a provider. CLI flags override it.
	if err := os.WriteFile(configPath, []byte("{}\n"), 0600); err != nil {
		return nil, ErrUnsafe
	}
	return &Client{
		binary:     opts.BinaryPath,
		chrome:     opts.ChromePath,
		profile:    opts.ProfileDir,
		session:    opts.Session,
		namespace:  opts.Namespace,
		configPath: configPath,
		timeout:    opts.Timeout,
		startup:    opts.StartupTimeout,
		maxOutput:  opts.MaxOutputBytes,
		noSandbox:  opts.NoSandbox,
		headed:     opts.Headed,
		kill:       killPID,
	}, nil
}

// Start verifies the pinned version and launches the session on about:blank.
func (c *Client) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrUnavailable
	}
	if c.started {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	call, cancel := context.WithTimeout(ctx, c.startup)
	defer cancel()
	version, err := c.runRaw(call, "--version")
	if err != nil {
		return err
	}
	if strings.TrimSpace(version) != "agent-browser "+Version {
		return ErrVersion
	}
	// Mark started before the first launch so a partial startup still runs the
	// owned close path instead of leaving a daemon behind.
	c.mu.Lock()
	c.started = true
	c.mu.Unlock()
	if _, err = c.run(call, "open"); err != nil {
		return err
	}
	// The daemon PID is the only handle available for bounded cleanup when a
	// pending page operation makes the daemon unresponsive to close.
	raw, err := c.run(call, "session", "info")
	if err != nil {
		return err
	}
	var info infoData
	if err = decodeData(raw, &info); err != nil || info.PID <= 0 {
		return ErrProtocol
	}
	c.mu.Lock()
	c.daemonPID = info.PID
	c.mu.Unlock()
	return nil
}

// Open navigates the session and returns the bounded final page identity.
func (c *Client) Open(ctx context.Context, target string) (Page, error) {
	if len(target) > 8<<10 || strings.ContainsAny(target, "\x00\r\n") {
		return Page{}, ErrUnsafe
	}
	call, cancel := c.call(ctx)
	defer cancel()
	raw, err := c.run(call, "open", target)
	if err != nil {
		return Page{}, err
	}
	var data openData
	if err = decodeData(raw, &data); err != nil {
		return Page{}, err
	}
	return Page{URL: data.URL, Title: data.Title}, nil
}

// Read returns the bounded rendered text of the active tab. It deliberately
// reads the full rendered DOM, not an interactive-only snapshot, so post bodies
// are present. Truncation is reported, never hidden.
func (c *Client) Read(ctx context.Context) (Document, error) {
	call, cancel := c.call(ctx)
	defer cancel()
	raw, err := c.run(call, "read")
	if err != nil {
		return Document{}, err
	}
	var data readData
	if err = decodeData(raw, &data); err != nil {
		return Document{}, err
	}
	return Document{Content: data.Content, URL: data.URL, FinalURL: data.FinalURL, Source: data.Source, Truncated: data.Truncated}, nil
}

// Snapshot returns a compact noninteractive accessibility tree plus refs. It
// must never request interactive-only output for content reads.
func (c *Client) Snapshot(ctx context.Context) (Snapshot, error) {
	call, cancel := c.call(ctx)
	defer cancel()
	raw, err := c.run(call, "snapshot", "-c")
	if err != nil {
		return Snapshot{}, err
	}
	var data snapshotData
	if err = decodeData(raw, &data); err != nil {
		return Snapshot{}, err
	}
	if data.Text == "" || len(data.Text) > c.maxOutput {
		return Snapshot{}, ErrProtocol
	}
	return Snapshot{Origin: data.Origin, Text: data.Text, Refs: data.Refs}, nil
}

// Fill clears and fills one element reference with untrusted text. The text is
// passed as a positional argument after "--" so leading dashes cannot become
// flags.
func (c *Client) Fill(ctx context.Context, ref, text string) error {
	if !validRef(ref) || len(text) > 64<<10 || strings.ContainsRune(text, 0) {
		return ErrUnsafe
	}
	call, cancel := c.call(ctx)
	defer cancel()
	_, err := c.run(call, "fill", ref, "--", text)
	return err
}

// Click activates one element reference.
func (c *Client) Click(ctx context.Context, ref string) error {
	if !validRef(ref) {
		return ErrUnsafe
	}
	call, cancel := c.call(ctx)
	defer cancel()
	_, err := c.run(call, "click", ref)
	return err
}

// Close shuts down the owned session and verifies the daemon no longer lists it.
// It is idempotent and never touches another session.
func (c *Client) Close(ctx context.Context) error {
	c.once.Do(func() {
		c.mu.Lock()
		c.closed = true
		started := c.started
		c.mu.Unlock()
		if !started {
			return
		}
		// A killed CLI can leave a page operation pending in the owned daemon,
		// which then ignores close. Try a short graceful close, then kill only
		// this agent's daemon. The namespace is per agent, so no other session
		// shares it.
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_, closeErr := c.runClosing(closeCtx, "close")
		cancel()
		if closeErr != nil {
			c.killOwnedDaemon()
		}
		verifyCtx, verifyCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer verifyCancel()
		raw, err := c.runClosing(verifyCtx, "session", "list")
		if err != nil {
			c.closeErr = ErrCommand
			return
		}
		var data sessionData
		if err = decodeData(raw, &data); err != nil {
			c.closeErr = ErrProtocol
			return
		}
		for _, name := range data.Sessions {
			if name == c.session {
				c.closeErr = ErrCommand
				return
			}
		}
	})
	return c.closeErr
}

func (c *Client) killOwnedDaemon() {
	c.mu.Lock()
	pid := c.daemonPID
	kill := c.kill
	c.daemonPID = 0
	c.mu.Unlock()
	if pid <= 0 || kill == nil {
		return
	}
	_ = kill(pid)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func validRef(ref string) bool {
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

func (c *Client) call(ctx context.Context) (context.Context, context.CancelFunc) {
	call, cancel := context.WithTimeout(ctx, c.timeout)
	if deadline, ok := ctx.Deadline(); ok {
		previous := cancel
		call, cancel = context.WithDeadline(call, deadline)
		return call, func() { cancel(); previous() }
	}
	return call, cancel
}

func (c *Client) args(command ...string) []string {
	args := []string{
		"--json",
		"--session", c.session,
		"--namespace", c.namespace,
		"--profile", c.profile,
		"--executable-path", c.chrome,
		"--config", c.configPath,
		"--no-auto-connect",
		"--no-webmcp",
		"--idle-timeout", "30m",
	}
	if c.noSandbox {
		args = append(args, "--args", "--no-sandbox")
	}
	if c.headed {
		args = append(args, "--headed")
	}
	return append(args, command...)
}

func (c *Client) environment() []string {
	env := []string{"LANG=C.UTF-8"}
	for _, name := range []string{"PATH", "HOME", "TMPDIR", "USER"} {
		if value := os.Getenv(name); value != "" {
			env = append(env, name+"="+value)
		}
	}
	env = append(env, "AGENT_BROWSER_DEFAULT_TIMEOUT="+strconv.FormatInt(c.timeout.Milliseconds(), 10))
	return env
}

// run executes one fixed command and decodes its JSON envelope.
func (c *Client) run(ctx context.Context, command ...string) (json.RawMessage, error) {
	return c.runCommand(ctx, false, command...)
}

// runClosing permits cleanup commands after the client is marked closed.
func (c *Client) runClosing(ctx context.Context, command ...string) (json.RawMessage, error) {
	return c.runCommand(ctx, true, command...)
}

func (c *Client) runCommand(ctx context.Context, closing bool, command ...string) (json.RawMessage, error) {
	argv := append([]string{c.binary}, c.args(command...)...)
	stdout, _, err := c.execute(ctx, argv, closing)
	if err != nil {
		return nil, err
	}
	if stdout.overflow {
		return nil, ErrOutputLimit
	}
	var env envelope
	decoder := json.NewDecoder(bytes.NewReader(stdout.buf.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&env); err != nil {
		return nil, ErrProtocol
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, ErrProtocol
	}
	if !env.Success || env.Error != nil && strings.TrimSpace(*env.Error) != "" {
		return nil, ErrCommand
	}
	if len(env.Data) == 0 {
		return nil, ErrProtocol
	}
	return env.Data, nil
}

// runRaw executes --version, whose output is plain text.
func (c *Client) runRaw(ctx context.Context, command ...string) (string, error) {
	stdout, _, err := c.execute(ctx, append([]string{c.binary}, command...), false)
	if err != nil {
		return "", err
	}
	if stdout.overflow {
		return "", ErrOutputLimit
	}
	return stdout.buf.String(), nil
}

func (c *Client) execute(ctx context.Context, argv []string, closing bool) (*limitedBuffer, *limitedBuffer, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed && !closing {
		return nil, nil, ErrUnavailable
	}
	if _, err := os.Stat(c.binary); err != nil {
		return nil, nil, ErrUnavailable
	}
	cmd := exec.CommandContext(ctx, c.binary, argv[1:]...)
	cmd.Env = c.environment()
	cmd.Stdin = nil
	stdout := &limitedBuffer{limit: c.maxOutput}
	stderr := &limitedBuffer{limit: 64 << 10}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = 2 * time.Second
	configureProcess(cmd)
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = killGroup(cmd.Process.Pid)
		}
		return nil
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, ErrUnavailable
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return stdout, stderr, ctx.Err()
	}
	if waitErr != nil {
		if stdout.overflow {
			return stdout, stderr, ErrOutputLimit
		}
		return stdout, stderr, ErrCommand
	}
	return stdout, stderr, nil
}

func decodeData(raw json.RawMessage, target any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return ErrProtocol
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return ErrProtocol
	}
	return nil
}

type limitedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (w *limitedBuffer) Write(p []byte) (int, error) {
	remaining := w.limit - w.buf.Len()
	if remaining <= 0 {
		w.overflow = true
		return 0, ErrOutputLimit
	}
	if len(p) > remaining {
		_, _ = w.buf.Write(p[:remaining])
		w.overflow = true
		return remaining, ErrOutputLimit
	}
	return w.buf.Write(p)
}
