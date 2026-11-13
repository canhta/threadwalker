package guard_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/store"
)

const googleNews = "https://news.google.com/rss/articles/CBMikgFBVV95cUxOzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz?oc=5"

func refGuard(t *testing.T, anchored bool) (*guard.Guard, *store.Store) {
	t.Helper()
	opts := liveOpts(t)
	opts.Template.Guard.TrendAnchor = anchored
	clock := opts.Now()
	opts.Now = func() time.Time { clock = clock.Add(time.Minute); return clock }
	opts.APIQuota = func(context.Context) (guard.APIQuota, error) { return guard.APIQuota{Posts: -1, Replies: 2}, nil }
	g, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return g, opts.Store
}

func TestNoteTrendIssuesStableShortRefs(t *testing.T) {
	g, _ := refGuard(t, true)
	got := g.NoteTrend(googleNews, " ", anchor)
	if len(got) != 2 || got[0] != "r1" || got[1] != "r2" {
		t.Fatalf("refs %v", got)
	}
	if again := g.NoteTrendText(googleNews, "story"); again != "r1" {
		t.Fatalf("same URL got %q", again)
	}
	if empty := g.NoteTrendText("", "x"); empty != "" {
		t.Fatalf("empty item got %q", empty)
	}
}

func TestPostAnchorsByShortRefAndStoresTheURL(t *testing.T) {
	g, st := refGuard(t, true)
	g.NoteTrend(anchor)
	ref := g.NoteTrendText(googleNews, "story")
	args := `{"text":"model mới giá 3 đô\ndòng sau","hook":"trend_detail","rationale":"bám model mới","trend_ref":"` + ref + `"}`
	if err := post(g, args); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := st.DB().QueryRow(`SELECT trend_ref FROM actions WHERE kind='post'`).Scan(&stored); err != nil || stored != googleNews {
		t.Fatalf("stored trend_ref %q, want the URL: %v", stored, err)
	}
	if got := blockedReason(t, post(g, strings.Replace(args, `"`+ref+`"`, `"r9"`, 1))); got != "no_trend_anchor" {
		t.Fatalf("unknown ref: %q", got)
	}
}

func TestReplyTargetByShortRef(t *testing.T) {
	g, st := refGuard(t, false)
	const permalink = "https://threads.com/@fixture_author/post/1"
	ref := g.NoteTrendText(permalink, "a post")
	g.NoteRead("", permalink, "a post")
	var target string
	reply := func(url string) error {
		_, err := g.Execute(context.Background(), guard.Attempt{Tool: "reply", Target: &target, Arguments: `{"text":"ok","target_username":"fixture_author","target_url":"` + url + `"}`}, func() (string, error) { return "", nil })
		return err
	}
	if err := reply(ref); err != nil || target != permalink {
		t.Fatalf("ref reply: target %q err %v", target, err)
	}
	var stored string
	if err := st.DB().QueryRow(`SELECT target_url FROM actions WHERE kind='reply'`).Scan(&stored); err != nil || stored != permalink {
		t.Fatalf("stored target_url %q: %v", stored, err)
	}
	if got := blockedReason(t, reply("r9")); got != "reply_target_unread" {
		t.Fatalf("unknown ref: %q", got)
	}
}
