//go:build agent_browser_integration

// Package threads_test exercises the real pinned agent-browser CLI against the
// public Threads site. It is opt-in because it needs an installed binary and
// Chrome, and it reaches the network without an account.
package threads_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/threads"
)

func agentBrowserBinary(t *testing.T) (binary, chrome string) {
	t.Helper()
	binary = os.Getenv("AGENT_BROWSER_BIN")
	chrome = os.Getenv("CHROME_BIN")
	if binary == "" || chrome == "" {
		t.Skip("set AGENT_BROWSER_BIN and CHROME_BIN for local agent-browser integration evidence")
	}
	for name, path := range map[string]string{"AGENT_BROWSER_BIN": binary, "CHROME_BIN": chrome} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			t.Skipf("%s is not an executable file: %v", name, err)
		}
	}
	return binary, chrome
}

func TestAgentBrowserLiveLoggedOutFailsClosed(t *testing.T) {
	binary, chrome := agentBrowserBinary(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	browser, err := threads.NewAgentBrowser(ctx, threads.AgentBrowserOptions{
		AgentID:          "live-logged-out",
		BaseURL:          "https://www.threads.com",
		ProfileDir:       t.TempDir(),
		ExecutablePath:   chrome,
		AgentBrowserPath: binary,
		Timeout:          20 * time.Second,
		StartupTimeout:   30 * time.Second,
		NoSandbox:        os.Getenv("AGENT_BROWSER_NO_SANDBOX") == "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close(context.Background())
	if _, err = browser.ReadFeed(ctx); !errors.Is(err, threads.ErrLoggedOut) {
		t.Fatalf("public Threads page did not fail closed as logged out: %v", err)
	}
}
