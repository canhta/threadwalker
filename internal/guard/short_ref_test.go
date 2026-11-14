package guard_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
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
	if err := st.DB().QueryRow(`SELECT trend FROM actions WHERE kind='post'`).Scan(&stored); err != nil || stored != googleNews {
		t.Fatalf("stored trend %q, want the URL: %v", stored, err)
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

// A reply target resolves only to a ref this wake issued: through its reads,
// the inbox or the candidates. A ref from an earlier wake, a mistyped id and
// another agent's id are refused, however valid the id is in the store; a post
// keeps its ref across wakes.
func TestReplyTargetMustBeIssuedThisWake(t *testing.T) {
	opts := liveOpts(t)
	clock := opts.Now()
	opts.Now = func() time.Time { clock = clock.Add(time.Minute); return clock }
	opts.APIQuota = func(context.Context) (guard.APIQuota, error) { return guard.APIQuota{Posts: -1, Replies: 5}, nil }
	ctx := context.Background()
	const permalink = "https://threads.com/@fixture_author/post/1"
	first, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ref := readPost(t, first, permalink, "id-1", "a post")
	if _, err = opts.Store.DB().Exec(`INSERT INTO agents(id,status) VALUES ('other','running')`); err != nil {
		t.Fatal(err)
	}
	otherID, err := opts.Store.NoteSeenPost(ctx, "other", store.SeenPost{URL: "https://threads.com/@fixture_author/post/2", Author: "fixture_author", Text: "theirs", ReadAt: opts.Now()})
	if err != nil {
		t.Fatal(err)
	}
	reply := func(g *guard.Guard, ref string) (guard.ResolvedTarget, error) {
		var target guard.ResolvedTarget
		_, err := g.Execute(ctx, guard.Attempt{Tool: "reply", Target: &target, Arguments: `{"text":"ok","target":"` + ref + `"}`}, func() (string, error) { return "", nil })
		return target, err
	}
	fresh := func() *guard.Guard {
		g, err := guard.New(opts)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	later := fresh()
	if _, err = reply(later, ref); blockedReason(t, err) != "unknown_target" {
		t.Fatalf("a ref of an earlier wake: %v", err)
	}
	if again := readPost(t, later, permalink, "id-1", "a post"); again != ref {
		t.Fatalf("the same post got %q then %q", ref, again)
	}
	if target, err := reply(later, ref); err != nil || target.URL != permalink || target.MediaID != "id-1" {
		t.Fatalf("a ref read again: target %+v err %v", target, err)
	}
	candidateRef := readPost(t, first, "https://threads.com/@fixture_author/post/4", "id-4", "an open post")
	candidate := fresh()
	if err = candidate.IssueCandidates(ctx, []guard.Candidate{{Ref: candidateRef}}); err != nil {
		t.Fatal(err)
	}
	if target, err := reply(candidate, candidateRef); err != nil || target.MediaID != "id-4" {
		t.Fatalf("a candidate ref: target %+v err %v", target, err)
	}
	inbox := fresh()
	inboxRef := inbox.NoteInbox(ctx, threads.Post{ID: "id-3", URL: "https://threads.com/@fixture_author/post/3", Text: "a reply to us"}, "")
	if target, err := reply(inbox, inboxRef); err != nil || target.MediaID != "id-3" {
		t.Fatalf("an inbox ref: target %+v err %v", target, err)
	}
	for name, bad := range map[string]string{"typo": "p9999", "other agent": "p" + strconv.FormatInt(otherID, 10)} {
		g := fresh()
		if err = g.IssueCandidates(ctx, []guard.Candidate{{Ref: bad}}); err != nil {
			t.Fatal(err)
		}
		if _, err = reply(g, bad); blockedReason(t, err) != "unknown_target" {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// A post anchor resolves only to a ref this wake issued too.
func TestPostAnchorMustBeIssuedThisWake(t *testing.T) {
	opts := liveOpts(t)
	opts.Template.Guard.TrendAnchor = true
	first, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ref := readPost(t, first, "https://threads.com/@fixture_author/post/1", "id-1", "a post")
	later, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	args := `{"text":"model mới giá 3 đô\ndòng sau","hook":"trend_detail","rationale":"bám model mới","trend":"` + ref + `"}`
	if got := blockedReason(t, post(later, args)); got != "no_trend_anchor" {
		t.Fatalf("a ref of an earlier wake: %q", got)
	}
	readPost(t, later, "https://threads.com/@fixture_author/post/1", "id-1", "a post")
	if err = post(later, args); err != nil {
		t.Fatalf("a ref read this wake: %v", err)
	}
}
