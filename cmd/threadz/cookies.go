package main

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/threads"
)

// cookieBusy is the import status when a wake or another import has the profile.
const cookieBusy = "busy"

// cookieImporter is the one cookie import, shared by Telegram /cookie and
// `ctl threads cookies`. It owns the profile while it runs: no wake starts
// meanwhile (Held), and it refuses to start while one runs.
type cookieImporter struct {
	mu        sync.Mutex
	busy      map[string]bool
	templates map[string]config.Template
	wakeOpen  func(ctx context.Context, agentID string) (bool, error)
	resume    func(ctx context.Context, agentID string) error
	now       func() time.Time
	// run defaults to threads.ImportCookies; tests replace it.
	run func(ctx context.Context, opts threads.AgentBrowserOptions, raw string) string
}

func newCookieImporter(templates []config.Template, wakeOpen func(context.Context, string) (bool, error), resume func(context.Context, string) error, now func() time.Time) *cookieImporter {
	c := &cookieImporter{busy: map[string]bool{}, templates: map[string]config.Template{}, wakeOpen: wakeOpen, resume: resume, now: now, run: threads.ImportCookies}
	for _, t := range templates {
		if t.Threads.Backend == "api" {
			c.templates[t.ID] = t
		}
	}
	return c
}

// Held reports that an import holds the agent's browser profile.
func (c *cookieImporter) Held(agentID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.busy[agentID]
}

// Import writes the cookies, verifies the feed and, on success, resumes the
// agent. It returns a threads.Cookies* status or cookieBusy; the cookie text is
// never returned, logged or stored.
func (c *cookieImporter) Import(ctx context.Context, agentID, raw string) string {
	t, ok := c.templates[agentID]
	if !ok {
		return threads.CookiesFailed
	}
	c.mu.Lock()
	if c.busy[agentID] {
		c.mu.Unlock()
		return cookieBusy
	}
	c.busy[agentID] = true
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.busy, agentID); c.mu.Unlock() }()
	if open, err := c.wakeOpen(ctx, agentID); err != nil || open {
		return cookieBusy
	}
	th := t.Threads
	status := c.run(ctx, threads.AgentBrowserOptions{AgentID: agentID, BaseURL: th.BaseURL, ProfileDir: th.ProfileDir, ExecutablePath: th.ExecutablePath, AgentBrowserPath: th.AgentBrowserPath, NoSandbox: th.NoSandbox, StartupTimeout: 30 * time.Second, Timeout: 20 * time.Second, Now: c.now}, raw)
	if status == threads.CookiesOK {
		if err := c.resume(context.WithoutCancel(ctx), agentID); err != nil {
			return threads.CookiesFailed
		}
	}
	return status
}

// readCookies reads the cookie text from stdin, bounded.
func readCookies(r io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, 300<<10))
	return string(b), err
}
