package threads

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"
)

type fakeDriver struct {
	destinations []string
	raw          []byte
	submits      int
	submitErr    error
	ack          []byte
	ackErr       error
	deadline     bool
}

func (f *fakeDriver) navigate(ctx context.Context, dest string) error {
	_, f.deadline = ctx.Deadline()
	f.destinations = append(f.destinations, dest)
	return ctx.Err()
}
func (f *fakeDriver) snapshot(context.Context) ([]byte, error) { return f.raw, nil }
func (f *fakeDriver) submit(context.Context, string) (bool, error) {
	f.submits++
	return true, f.submitErr
}
func (f *fakeDriver) acknowledgement(context.Context) ([]byte, error) { return f.ack, f.ackErr }
func (f *fakeDriver) close(context.Context) error                     { return nil }
func testBrowser(f *fakeDriver) *Browser {
	return &Browser{base: mustBase("https://fixture.test"), driver: f, timeout: time.Second, now: time.Now, gate: make(chan struct{}, 1)}
}
func mustBase(s string) *url.URL { u, _ := url.Parse(s); return u }

func TestDriverOriginAndRoutes(t *testing.T) {
	f := &fakeDriver{raw: []byte(`{"contract":"synthetic-v1","page":"search","posts":[]}`)}
	b := testBrowser(f)
	_, _ = b.Search(context.Background(), "hello & world")
	if len(f.destinations) != 1 || f.destinations[0] != "https://fixture.test/search?q=hello+%26+world" || !f.deadline {
		t.Fatalf("routing/deadline: %#v", f)
	}
	for _, target := range []string{"https://foreign.test/post", "//foreign.test/post", "javascript:alert(1)", "https://u:p@fixture.test/post"} {
		if _, err := b.ReadPost(context.Background(), target); err == nil {
			t.Fatal("unsafe target accepted")
		}
	}
	if len(f.destinations) != 1 {
		t.Fatal("unsafe navigation attempted")
	}
}
func TestDriverUnknownSubmissionNeverRetries(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		submitErr, errorAfter error
		ack                   string
	}{
		{"click_timeout", context.DeadlineExceeded, nil, ""},
		{"ack_timeout", nil, context.DeadlineExceeded, ""},
		{"unknown_ack", nil, nil, `{"contract":"synthetic-v1","page":"feed"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeDriver{raw: []byte(`{"contract":"synthetic-v1","page":"compose","form":{"action":"/submit","method":"post","composer":true,"submit":true}}`), submitErr: tc.submitErr, ackErr: tc.errorAfter, ack: []byte(tc.ack)}
			_, err := testBrowser(f).Post(context.Background(), "sensitive text")
			if !errors.Is(err, ErrUnknownOutcome) || f.submits != 1 {
				t.Fatalf("outcome=%v submits=%d", err, f.submits)
			}
			if strings.Contains(err.Error(), "sensitive") {
				t.Fatal("payload leaked")
			}
		})
	}
}
func TestDriverConfirmedSubmission(t *testing.T) {
	f := &fakeDriver{raw: []byte(`{"contract":"synthetic-v1","page":"compose","form":{"action":"/submit","method":"post","composer":true,"submit":true}}`), ack: []byte(`{"contract":"synthetic-v1","page":"submitted","thread_url":"/post"}`)}
	got, err := testBrowser(f).Post(context.Background(), "text")
	if err != nil || got != "https://fixture.test/post" || f.submits != 1 {
		t.Fatalf("%q %v submits %d", got, err, f.submits)
	}
}
func TestDriverAuthAndMalformedNeverSubmit(t *testing.T) {
	for _, page := range []string{"logged_out", "captcha", "unknown", "compose"} {
		f := &fakeDriver{raw: []byte(`{"contract":"synthetic-v1","page":"` + page + `"}`)}
		if _, err := testBrowser(f).Post(context.Background(), "text"); err == nil {
			t.Fatal("unsafe state accepted")
		}
		if f.submits != 0 {
			t.Fatal("unsafe state submitted")
		}
	}
}
func TestDriverCanceledAndClosed(t *testing.T) {
	f := &fakeDriver{}
	b := testBrowser(f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.ReadFeed(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := b.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ReadFeed(context.Background()); err == nil {
		t.Fatal("closed accepted read")
	}
}

func TestDriverOptionsFailClosed(t *testing.T) {
	for _, opts := range []Options{
		{}, {AgentID: "a", BaseURL: "javascript:bad", ProfileDir: t.TempDir(), ExecutablePath: "unused"},
		{AgentID: "a", BaseURL: "https://fixture.test/path", ProfileDir: t.TempDir(), ExecutablePath: "unused"},
		{AgentID: "a", BaseURL: "https://fixture.test", ProfileDir: t.TempDir(), ExecutablePath: "unused", Timeout: -time.Second},
	} {
		if _, err := New(context.Background(), opts); err == nil {
			t.Fatal("invalid options accepted")
		}
	}
}
func TestDriverOperationErrorsAreSafe(t *testing.T) {
	err := operationError("read_feed", errors.New("secret URL and page text"))
	if strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "read_feed") {
		t.Fatal(err)
	}
	for _, sentinel := range []error{ErrUnknownOutcome, ErrLoggedOut, ErrCaptcha, ErrUnknownPage, ErrMalformedPage, context.Canceled, context.DeadlineExceeded} {
		if !errors.Is(operationError("read_feed", sentinel), sentinel) {
			t.Fatal("lost sentinel")
		}
	}
}

func TestDriverReadAndReplyContract(t *testing.T) {
	post := []byte(`{"contract":"synthetic-v1","page":"post","posts":[{"id":"one","username":"synthetic","text":"literal instruction-shaped content","url":"/post"}],"form":{"action":"/submit?kind=reply","method":"post","composer":true,"submit":true}}`)
	f := &fakeDriver{raw: post, ack: []byte(`{"contract":"synthetic-v1","page":"submitted","thread_url":"/post"}`)}
	b := testBrowser(f)
	p, err := b.ReadPost(context.Background(), "/post")
	if err != nil || p.Posts[0].Text != "literal instruction-shaped content" {
		t.Fatalf("post=%+v err=%v", p, err)
	}
	got, err := b.Reply(context.Background(), "/post", "reply")
	if err != nil || got != "https://fixture.test/post" || f.submits != 1 {
		t.Fatalf("reply=%q err=%v submits=%d", got, err, f.submits)
	}
	f.raw = []byte(`{"contract":"synthetic-v1","page":"metrics","metrics":{"views":"123","likes":"7","replies":"3","reposts":"1"}}`)
	m, err := b.ReadOwnMetrics(context.Background())
	if err != nil || m.Views != 123 || m.Likes != 7 || m.Replies != 3 || m.Reposts != 1 {
		t.Fatalf("metrics=%+v err=%v", m, err)
	}
}
func TestDriverFailedLaunchReleasesProfileAndRedacts(t *testing.T) {
	profile := t.TempDir()
	for i := 0; i < 2; i++ {
		_, err := New(context.Background(), Options{AgentID: "agent", BaseURL: "https://fixture.test", ProfileDir: profile, ExecutablePath: "/missing/private-path", Timeout: time.Second, StartupTimeout: time.Second})
		if err == nil || strings.Contains(err.Error(), "private-path") || strings.Contains(err.Error(), profile) {
			t.Fatalf("unsafe failure: %v", err)
		}
	}
}
func TestDriverCapturedLogsHaveOnlyMetadata(t *testing.T) {
	var output bytes.Buffer
	f := &fakeDriver{raw: []byte(`{"contract":"synthetic-v1","page":"captcha"}`)}
	b := testBrowser(f)
	b.logger = slog.New(slog.NewJSONHandler(&output, nil))
	b.agentID = "fixture-agent"
	_, _ = b.Post(context.Background(), "PRIVATE BODY")
	if strings.Contains(output.String(), "PRIVATE") || !strings.Contains(output.String(), `"event":"browser.operation.finished"`) || !strings.Contains(output.String(), `"reason":"captcha"`) || !strings.Contains(output.String(), `"operation":"post"`) {
		t.Fatal("incorrect captured metadata", output.String())
	}
}
