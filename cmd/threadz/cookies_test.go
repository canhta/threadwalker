package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/threads"
)

const cookieSecret = "synthetic-session-value"

func apiTemplate(id string) config.Template {
	t := config.Template{ID: id}
	t.Threads = config.ThreadsConfig{Backend: "api", BaseURL: "https://www.threads.com", ProfileDir: "/p", ExecutablePath: "/c", AgentBrowserPath: "/a"}
	return t
}

func TestCtlThreadsCookiesReadsStdinAndSharesTheImporter(t *testing.T) {
	s := newCtlServer(t)
	var gotRaw, gotAgent string
	resumed := 0
	imp := newCookieImporter([]config.Template{apiTemplate(s.id)}, func(context.Context, string) (bool, error) { return false, nil },
		func(context.Context, string) error { resumed++; return nil }, time.Now)
	imp.run = func(_ context.Context, o threads.AgentBrowserOptions, raw string) string {
		gotRaw, gotAgent = raw, o.AgentID
		return threads.CookiesOK
	}
	s.service.cookies = imp
	s.deps.stdin = strings.NewReader("sessionid=" + cookieSecret + "; ds_user_id=1\n")
	code, out, errOut := s.ctl("threads", "cookies", "--agent", s.id)
	if code != exitOK || !strings.Contains(out, "logged in") || strings.Contains(out+errOut, cookieSecret) {
		t.Fatalf("code %d out %q err %q", code, out, errOut)
	}
	if gotAgent != s.id || !strings.Contains(gotRaw, cookieSecret) || resumed != 1 {
		t.Fatalf("agent %q raw %q resumed %d", gotAgent, gotRaw, resumed)
	}

	imp.run = func(context.Context, threads.AgentBrowserOptions, string) string { return threads.CookiesLoggedOut }
	s.deps.stdin = strings.NewReader("sessionid=" + cookieSecret)
	code, out, errOut = s.ctl("threads", "cookies", "--agent", s.id)
	if code == exitOK || !strings.Contains(errOut, "logged out") || strings.Contains(out+errOut, cookieSecret) || resumed != 1 {
		t.Fatalf("failure: code %d out %q err %q resumed %d", code, out, errOut, resumed)
	}
}

func TestCtlThreadsCookiesNeedsStdinAndNeverTakesTheValueAsAnArgument(t *testing.T) {
	s := newCtlServer(t)
	s.deps.stdin = strings.NewReader("   \n")
	if code, _, errOut := s.ctl("threads", "cookies", "--agent", s.id); code != exitUsage || !strings.Contains(errOut, "stdin") {
		t.Fatalf("empty stdin: %d %q", code, errOut)
	}
	s.deps.stdin = strings.NewReader("sessionid=x")
	if code, _, _ := s.ctl("threads", "cookies", "--agent", s.id, "sessionid=x"); code != exitUsage {
		t.Fatalf("argument accepted: %d", code)
	}
}

func TestCookieImporterGatesTheProfile(t *testing.T) {
	wake := true
	imp := newCookieImporter([]config.Template{apiTemplate("a")}, func(context.Context, string) (bool, error) { return wake, nil }, func(context.Context, string) error { return nil }, time.Now)
	held := false
	imp.run = func(context.Context, threads.AgentBrowserOptions, string) string {
		held = imp.Held("a")
		return threads.CookiesOK
	}
	if got := imp.Import(context.Background(), "a", "x"); got != cookieBusy || held {
		t.Fatalf("import during a wake: %q", got)
	}
	wake = false
	if got := imp.Import(context.Background(), "a", "x"); got != threads.CookiesOK || !held || imp.Held("a") {
		t.Fatalf("held during %v, after %v, got %q", held, imp.Held("a"), got)
	}
	if got := imp.Import(context.Background(), "other", "x"); got != threads.CookiesFailed {
		t.Fatalf("unknown agent: %q", got)
	}
}
