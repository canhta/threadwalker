package threads

import (
	"context"
	"errors"
	"testing"

	"github.com/canhta/threadz/internal/agentbrowser"
	"github.com/canhta/threadz/internal/config"
)

func TestMemoryServesConfiguredPagesAndRecordsCalls(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	m.Pages[OpSearch] = Page{Kind: "search", Posts: []Post{{URL: "https://t.test/a"}}}
	m.Own = Metrics{Views: 9}
	m.Actions["own"] = Metrics{Likes: 4}
	m.Results[OpPost] = "https://t.test/new"
	m.Results[OpReply] = "https://t.test/reply"

	if p, err := m.ReadFeed(ctx); err != nil || p.Kind != "feed" || len(p.Posts) != 1 {
		t.Fatalf("feed=%+v err=%v", p, err)
	}
	if p, err := m.Search(ctx, "go", false); err != nil || p.Posts[0].URL != "https://t.test/a" {
		t.Fatalf("search=%+v err=%v", p, err)
	}
	if p, err := m.ReadPost(ctx, "https://t.test/p"); err != nil || p.Kind != "post" {
		t.Fatalf("post=%+v err=%v", p, err)
	}
	if v, err := m.ReadOwnMetrics(ctx); err != nil || v.Views != 9 {
		t.Fatalf("own=%+v err=%v", v, err)
	}
	if v, err := m.ReadActionMetrics(ctx, "own"); err != nil || v.Likes != 4 {
		t.Fatalf("action=%+v err=%v", v, err)
	}
	if _, err := m.ReadActionMetrics(ctx, "other"); !errors.Is(err, ErrMetricsUnavailable) {
		t.Fatalf("unknown action url err=%v", err)
	}
	if u, err := m.Post(ctx, "hello"); err != nil || u != (Published{URL: "https://t.test/new", ID: "new"}) {
		t.Fatalf("post=%+v err=%v", u, err)
	}
	if u, err := m.Reply(ctx, "https://t.test/p", "re"); err != nil || u != (Published{URL: "https://t.test/reply", ID: "reply"}) {
		t.Fatalf("reply=%+v err=%v", u, err)
	}
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := m.Last(OpReply); got != (Call{Op: OpReply, Arg: "https://t.test/p", Text: "re"}) {
		t.Fatalf("last reply=%+v", got)
	}
	if m.Count(OpReadOwnMetrics) != 3 || m.Count(OpClose) != 1 || len(m.Calls()) != 9 {
		t.Fatalf("calls=%+v", m.Calls())
	}
	if m.Last("missing") != (Call{}) {
		t.Fatal("absent op must report the zero call")
	}
}

func TestMemoryErrorsAndHookFailOperations(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	m := NewMemory()
	for _, op := range []string{OpReadFeed, OpSearch, OpReadPost, OpReadOwnMetrics, OpPost, OpReply, OpClose} {
		m.Errors[op] = boom
	}
	if _, err := m.ReadFeed(ctx); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := m.Search(ctx, "", false); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := m.ReadPost(ctx, ""); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := m.ReadOwnMetrics(ctx); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := m.ReadActionMetrics(ctx, ""); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := m.Post(ctx, ""); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := m.Reply(ctx, "", ""); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if err := m.Close(ctx); !errors.Is(err, boom) {
		t.Fatal(err)
	}

	hooked := NewMemory()
	hooked.Hook = func(_ context.Context, c Call) error {
		if c.Op == OpSearch && c.Arg == "stop" {
			return boom
		}
		return nil
	}
	if _, err := hooked.Search(ctx, "stop", false); !errors.Is(err, boom) {
		t.Fatalf("hook err=%v", err)
	}
	if _, err := hooked.Search(ctx, "go", false); err != nil {
		t.Fatal(err)
	}
}

func TestStubBackendServesMemoryAndCannotPublish(t *testing.T) {
	factory, canPublish := NewBackend(config.Template{}, nil, nil)
	if canPublish {
		t.Fatal("stub must not publish")
	}
	b, err := factory(context.Background(), config.Template{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.(*Memory); !ok {
		t.Fatalf("stub backend is %T", b)
	}
	if u, err := b.Post(context.Background(), "x"); err != nil || u != (Published{}) {
		t.Fatalf("stub write=%+v err=%v", u, err)
	}
}

func TestLiveOpensBrowserOnlyForTheFeedAndClosesIt(t *testing.T) {
	opened := 0
	cli := &fakeAgentCLI{openURL: "https://www.threads.com/", snapshot: agentbrowser.Snapshot{Origin: "https://www.threads.com/", Text: liveFixture(t, "feed.ax.txt")}}
	l := &Live{openBrowser: func(context.Context) (*Browser, error) { opened++; return testAgentBrowser(t, cli, nil), nil }}
	if err := l.Close(context.Background()); err != nil || opened != 0 {
		t.Fatalf("close without feed read: err=%v opened=%d", err, opened)
	}
	for i := 0; i < 2; i++ {
		if page, err := l.ReadFeed(context.Background()); err != nil || len(page.Posts) != 2 {
			t.Fatalf("feed=%+v err=%v", page, err)
		}
	}
	if err := l.Close(context.Background()); err != nil || opened != 1 || cli.closed != 1 {
		t.Fatalf("err=%v opened=%d closed=%d", err, opened, cli.closed)
	}
	boom := errors.New("launch failed")
	failing := &Live{openBrowser: func(context.Context) (*Browser, error) { return nil, boom }}
	if _, err := failing.ReadFeed(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
}

func TestOpenLiveRequiresTokens(t *testing.T) {
	factory, canPublish := NewBackend(config.Template{Threads: config.ThreadsConfig{Backend: "api"}}, nil, nil)
	if !canPublish {
		t.Fatal("api backend must publish")
	}
	if _, err := factory(context.Background(), config.Template{}); err == nil {
		t.Fatal("api backend opened without a token manager")
	}
}
