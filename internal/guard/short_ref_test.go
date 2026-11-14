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
	if len(got) != 2 || got[0] != "t1" || got[1] != "t2" {
		t.Fatalf("refs %v", got)
	}
	if again := g.NoteTrendText(googleNews, "story"); again != "t1" {
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
	args := `{"text":"model mới giá 3 đô\ndòng sau","hook":"trend_detail","rationale":"bám model mới","trend":"` + ref + `"}`
	if err := post(g, args); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := st.DB().QueryRow(`SELECT trend_ref FROM actions WHERE kind='post'`).Scan(&stored); err != nil || stored != googleNews {
		t.Fatalf("stored trend_ref %q, want the URL: %v", stored, err)
	}
	if got := blockedReason(t, post(g, strings.Replace(args, `"`+ref+`"`, `"t9"`, 1))); got != "no_trend_anchor" {
		t.Fatalf("unknown ref: %q", got)
	}
}

func TestReplyTargetByStoredRef(t *testing.T) {
	g, st := refGuard(t, false)
	const permalink = "https://threads.com/@fixture_author/post/1"
	ref := readPost(t, g, permalink+"?utm=x", "id-1", "a post")
	var target guard.ResolvedTarget
	reply := func(ref string) error {
		_, err := g.Execute(context.Background(), guard.Attempt{Tool: "reply", Target: &target, Arguments: `{"text":"ok","target":"` + ref + `"}`}, func() (string, error) { return "", nil })
		return err
	}
	if err := reply(ref); err != nil || target.URL != permalink || target.MediaID != "id-1" {
		t.Fatalf("ref reply: target %+v err %v", target, err)
	}
	var stored, author string
	if err := st.DB().QueryRow(`SELECT target_url,target_username FROM actions WHERE kind='reply'`).Scan(&stored, &author); err != nil || stored != permalink || author != "fixture_author" {
		t.Fatalf("stored target %q by %q: %v", stored, author, err)
	}
	if got := blockedReason(t, reply("p9")); got != "unknown_target" {
		t.Fatalf("unknown ref: %q", got)
	}
}

// A post read in an earlier wake keeps its ref: a fresh guard, which has read
// nothing, resolves it from the store, and reading it again returns the same ref.
func TestPostRefSurvivesTheGuard(t *testing.T) {
	opts := liveOpts(t)
	const permalink = "https://threads.com/@fixture_author/post/1"
	first, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ref := readPost(t, first, permalink, "id-1", "a post")
	later, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	var target guard.ResolvedTarget
	if _, err = later.Execute(context.Background(), guard.Attempt{Tool: "reply", Target: &target, Arguments: `{"text":"ok","target":"` + ref + `"}`}, func() (string, error) { return "", nil }); err != nil {
		t.Fatal(err)
	}
	if target.URL != permalink || target.MediaID != "id-1" {
		t.Fatalf("target %+v", target)
	}
	if again := readPost(t, later, permalink, "id-1", "a post"); again != ref {
		t.Fatalf("the same post got %q then %q", ref, again)
	}
}
