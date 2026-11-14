package threads

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeDriver struct {
	destinations []string
	read         Page
	pageErr      error
	submits      int
	submitErr    error
	ack          Page
	ackErr       error
}

func (f *fakeDriver) navigate(_ context.Context, dest string) error {
	f.destinations = append(f.destinations, dest)
	return nil
}
func (f *fakeDriver) page(context.Context, string) (Page, error) { return f.read, f.pageErr }
func (f *fakeDriver) submit(context.Context, string) (bool, error) {
	f.submits++
	return true, f.submitErr
}
func (f *fakeDriver) acknowledgement(context.Context) (Page, error) { return f.ack, f.ackErr }
func (f *fakeDriver) snapshot() (string, string)                    { return "", "" }
func (f *fakeDriver) close(context.Context) error                   { return nil }

func testBrowser(f *fakeDriver) *Browser {
	return &Browser{base: mustBase("https://fixture.test"), driver: f, timeout: time.Second, gate: make(chan struct{}, 1)}
}

var replyForm = &Form{Action: "https://fixture.test/p", Method: "POST", Composer: true, Submit: true}

func TestBrowserReplyConfirmedUsesConfiguredOrigin(t *testing.T) {
	f := &fakeDriver{read: Page{Kind: "post", Form: replyForm}, ack: Page{Kind: "submitted", ThreadURL: "https://fixture.test/@me/post/R1"}}
	got, err := testBrowser(f).Reply(context.Background(), "https://threads.net/@a/post/P1", "hi")
	if err != nil || got != "https://fixture.test/@me/post/R1" || f.submits != 1 {
		t.Fatalf("%q %v submits %d", got, err, f.submits)
	}
	if len(f.destinations) != 1 || f.destinations[0] != "https://fixture.test/@a/post/P1" {
		t.Fatalf("navigated to %v", f.destinations)
	}
}

func TestBrowserReplyUnknownOutcomeNeverRetries(t *testing.T) {
	for name, f := range map[string]*fakeDriver{
		"click_timeout": {submitErr: context.DeadlineExceeded},
		"ack_timeout":   {ackErr: context.DeadlineExceeded},
		"no_ack_url":    {ack: Page{Kind: "submitted"}},
	} {
		t.Run(name, func(t *testing.T) {
			f.read = Page{Kind: "post", Form: replyForm}
			_, err := testBrowser(f).Reply(context.Background(), "/@a/post/P1", "sensitive text")
			if !errors.Is(err, ErrUnknownOutcome) || f.submits != 1 || strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("outcome=%v submits=%d", err, f.submits)
			}
		})
	}
}

func TestBrowserReplyRefusalsNeverSubmit(t *testing.T) {
	for _, pageErr := range []error{ErrLoggedOut, ErrCaptcha, ErrUnknownPage} {
		f := &fakeDriver{pageErr: pageErr}
		if _, err := testBrowser(f).Reply(context.Background(), "/@a/post/P1", "x"); !errors.Is(err, pageErr) || f.submits != 0 {
			t.Fatalf("%v submits %d", err, f.submits)
		}
	}
	f := &fakeDriver{read: Page{Kind: "post"}}
	if _, err := testBrowser(f).Reply(context.Background(), "/@a/post/P1", "x"); !errors.Is(err, ErrMalformedPage) || f.submits != 0 {
		t.Fatalf("form-less page: %v submits %d", err, f.submits)
	}
	f = &fakeDriver{read: Page{Kind: "post", Form: replyForm}, submitErr: ErrWriteRefused}
	if _, err := testBrowser(f).Reply(context.Background(), "/@a/post/P1", "x"); !errors.Is(err, ErrWriteRefused) || errors.Is(err, ErrUnknownOutcome) {
		t.Fatalf("refusal before the click: %v", err)
	}
}

func liveWith(t *testing.T, f *fakeDriver) (*Live, *fakeThreads) {
	t.Helper()
	fake, api, _ := newFake(t)
	return &Live{API: api, browser: testBrowser(f)}, fake
}

func TestLiveReplyRoutesByTarget(t *testing.T) {
	f := &fakeDriver{read: Page{Kind: "post", Form: replyForm}, ack: Page{ThreadURL: "https://fixture.test/@me/post/R1"}}
	l, fake := liveWith(t, f)
	fake.route("POST", "/1000000000000001/threads", "container.json")
	fake.route("GET", "/3000000000000001", "status_finished.json")
	fake.route("POST", "/1000000000000001/threads_publish", "publish.json")
	fake.route("GET", "/2000000000000001", "post_permalink.json")
	fake.route("GET", "/me/replies", "replies.json")
	if _, err := l.Reply(context.Background(), "2000000000000001", "api reply"); err != nil || f.submits != 0 {
		t.Fatalf("numeric id left the API: %v submits %d", err, f.submits)
	}
	if _, err := l.Reply(context.Background(), "https://www.threads.com/@a/post/P1", "browser reply"); err != nil || f.submits != 1 {
		t.Fatalf("permalink did not use the browser: %v submits %d", err, f.submits)
	}
}

func TestLiveReplyOpensTheBrowserSession(t *testing.T) {
	boom := errors.New("launch failed")
	opened := 0
	l := &Live{openBrowser: func(context.Context) (*Browser, error) { opened++; return nil, boom }}
	if _, err := l.Reply(context.Background(), "https://www.threads.com/@a/post/P1", "x"); !errors.Is(err, boom) || opened != 1 {
		t.Fatalf("reply without a read opened %d sessions: %v", opened, err)
	}
}

func TestLiveReplyResolvesMediaIDFromOwnReplies(t *testing.T) {
	for name, tc := range map[string]struct{ text, id string }{
		"match":    {"synthetic reply text", "2000000000000002"},
		"no match": {"something else", ""},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeDriver{read: Page{Kind: "post", Form: replyForm}, ack: Page{ThreadURL: "https://fixture.test/@me/post/R1"}}
			l, fake := liveWith(t, f)
			fake.route("GET", "/me/replies", "replies.json")
			got, err := l.Reply(context.Background(), "https://www.threads.com/@a/post/P1", tc.text)
			if err != nil || got.ID != tc.id || got.URL != "https://fixture.test/@me/post/R1" {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	// A failed lookup never undoes a confirmed reply.
	f := &fakeDriver{read: Page{Kind: "post", Form: replyForm}, ack: Page{ThreadURL: "https://fixture.test/@me/post/R1"}}
	l, _ := liveWith(t, f)
	if got, err := l.Reply(context.Background(), "https://www.threads.com/@a/post/P1", "x"); err != nil || got.ID != "" || got.URL == "" {
		t.Fatalf("%+v %v", got, err)
	}
}
