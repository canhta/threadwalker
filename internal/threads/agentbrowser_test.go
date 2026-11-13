package threads

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agentbrowser"
)

type fakeAgentCLI struct {
	snapshot  agentbrowser.Snapshot
	snapshots []agentbrowser.Snapshot
	openURL   string
	openErr   error
	closeErr  error
	closed    int
	started   int
	snapCalls int
	clicks    int
	scrolls   int
	fills     []string
	cookies   []agentbrowser.Cookie
	cookieErr error
}

func (f *fakeAgentCLI) Start(context.Context) error { f.started++; return nil }
func (f *fakeAgentCLI) Open(context.Context, string) (agentbrowser.Page, error) {
	if f.openErr != nil {
		return agentbrowser.Page{}, f.openErr
	}
	return agentbrowser.Page{URL: f.openURL, Title: "fixture"}, nil
}
func (f *fakeAgentCLI) Snapshot(context.Context) (agentbrowser.Snapshot, error) {
	f.snapCalls++
	if len(f.snapshots) > 0 {
		next := f.snapshots[0]
		f.snapshots = f.snapshots[1:]
		return next, nil
	}
	return f.snapshot, nil
}
func (f *fakeAgentCLI) Fill(_ context.Context, _, text string) error {
	f.fills = append(f.fills, text)
	return nil
}
func (f *fakeAgentCLI) Focus(context.Context, string) error   { return nil }
func (f *fakeAgentCLI) Type(context.Context, string) error    { return nil }
func (f *fakeAgentCLI) PressShiftEnter(context.Context) error { return nil }
func (f *fakeAgentCLI) Click(context.Context, string) error   { f.clicks++; return nil }
func (f *fakeAgentCLI) ScrollDown(context.Context) error      { f.scrolls++; return nil }
func (f *fakeAgentCLI) SetCookie(_ context.Context, c agentbrowser.Cookie) error {
	f.cookies = append(f.cookies, c)
	return f.cookieErr
}
func (f *fakeAgentCLI) Close(context.Context) error { f.closed++; return f.closeErr }

func testAgentBrowser(t *testing.T, cli *fakeAgentCLI, sleep func(context.Context, time.Duration) error) *Browser {
	t.Helper()
	base, _ := url.Parse("https://www.threads.com")
	if sleep == nil {
		sleep = func(context.Context, time.Duration) error { return context.DeadlineExceeded }
	}
	return &Browser{
		base:     base,
		driver:   &agentBrowserDriver{cli: cli, base: base, timeout: time.Second, release: func() {}, sleep: sleep, now: time.Now, renderWait: threadsRenderWait},
		feedPath: "/",
		timeout:  time.Second,
		gate:     make(chan struct{}, 1),
	}
}

func TestAgentBrowserCrossOriginIsRefused(t *testing.T) {
	cli := &fakeAgentCLI{openURL: "https://foreign.test/"}
	if _, err := testAgentBrowser(t, cli, nil).ReadFeed(context.Background()); !errors.Is(err, ErrMalformedPage) {
		t.Fatalf("foreign navigation accepted: %v", err)
	}
	cli = &fakeAgentCLI{openURL: "https://www.threads.com/", snapshot: agentbrowser.Snapshot{Origin: "https://foreign.test/", Text: "- main"}}
	if _, err := testAgentBrowser(t, cli, nil).ReadFeed(context.Background()); !errors.Is(err, ErrMalformedPage) {
		t.Fatalf("foreign page accepted: %v", err)
	}
}

func TestAgentBrowserCloseRunsOnce(t *testing.T) {
	cli := &fakeAgentCLI{}
	b := testAgentBrowser(t, cli, nil)
	for i := 0; i < 2; i++ {
		if err := b.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if cli.closed != 1 {
		t.Fatalf("close ran %d times", cli.closed)
	}
}

func TestAgentBrowserIdentifiersAreStableAndSafe(t *testing.T) {
	first, ns := agentBrowserIdentifiers("solo.builder/one")
	again, nsAgain := agentBrowserIdentifiers("solo.builder/one")
	if first != again || ns != nsAgain {
		t.Fatal("identifiers are not deterministic")
	}
	for _, id := range []string{"a", "fixture", strings.Repeat("x", 200), "agent with spaces"} {
		session, namespace := agentBrowserIdentifiers(id)
		if len(session) > 64 || len(namespace) > 64 || strings.ContainsAny(session, " ./") || strings.ContainsAny(namespace, " ./") {
			t.Fatalf("unsafe identifier %q -> %q %q", id, session, namespace)
		}
	}
	other, _ := agentBrowserIdentifiers("another-agent")
	if other == first {
		t.Fatal("different agents share a session")
	}
}

func mustBase(s string) *url.URL { u, _ := url.Parse(s); return u }

func TestAgentBrowserReplyAcknowledgesToast(t *testing.T) {
	base := "https://www.threads.com"
	target := base + "/@synthetic_author/post/fixtureLiveReply1"
	empty := agentbrowser.Snapshot{Origin: target, Text: liveFixture(t, "reply-live.ax.txt")}
	filled := agentbrowser.Snapshot{Origin: target, Text: liveFixture(t, "reply-live-filled.ax.txt")}
	cli := &fakeAgentCLI{openURL: target, snapshots: []agentbrowser.Snapshot{
		empty, filled, filled, {Origin: target, Text: liveFixture(t, "reply-toast.ax.txt")},
	}}
	got, err := testAgentBrowser(t, cli, nil).Reply(context.Background(), target, "Synthetic nested reply.")
	if err != nil || got != base+"/@synthetic_author/post/fixtureNestedReply1" {
		t.Fatalf("confirmed=%q err=%v", got, err)
	}
	if cli.clicks != 1 || len(cli.fills) != 1 {
		t.Fatalf("clicks=%d fills=%v", cli.clicks, cli.fills)
	}
}

// post-real.ax.txt is a sanitized snapshot of a real post page, where the
// composer textbox sits in its own wrapper node. post-real-filled.ax.txt is the
// same page with text typed; live typing was off limits, so its Reply button
// position (after the textbox, inside the post block) is inferred.
func TestAgentBrowserReplyOnRealPostPage(t *testing.T) {
	target := "https://www.threads.com/@synthetic_author/post/fixtureRealPost1"
	empty := agentbrowser.Snapshot{Origin: target, Text: liveFixture(t, "post-real.ax.txt")}
	filled := agentbrowser.Snapshot{Origin: target, Text: liveFixture(t, "post-real-filled.ax.txt")}
	cli := &fakeAgentCLI{openURL: target, snapshots: []agentbrowser.Snapshot{
		empty, filled, filled, {Origin: target, Text: liveFixture(t, "reply-toast.ax.txt")},
	}}
	if _, err := testAgentBrowser(t, cli, nil).Reply(context.Background(), target, "Synthetic reply."); err != nil && errors.Is(err, ErrMalformedPage) {
		t.Fatalf("composer not found on a real post page: %v", err)
	}
	if cli.clicks != 1 || len(cli.fills) != 1 {
		t.Fatalf("clicks=%d fills=%v", cli.clicks, cli.fills)
	}
}
