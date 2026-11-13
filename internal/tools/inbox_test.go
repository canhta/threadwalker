package tools_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/tools"
)

func inboxPost(id, user string, at time.Time, text string) threads.Post {
	return threads.Post{ID: id, Username: user, Text: text, URL: "https://www.threads.com/@" + user + "/post/" + id, Timestamp: at, AddressedToAgent: true}
}

func TestInboxListsNewRepliesAndAnswerUsesTheAPIPath(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	st, opts, _ := setup(t)
	mem := threads.NewMemory()
	mem.Pages[threads.OpReadReplies] = threads.Page{Kind: "replies"}
	opts.Threads = mem
	opts.Template.Limits.RepliesPerUser7d = 1
	opts.Scope = guard.Scope{Store: st, Template: opts.Template, WakeID: 1, Now: func() time.Time { return now }, OwnUsernames: []string{"us"}}
	d, err := tools.NewDispatcher(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []store.SeededAction{
		{AgentID: "agent", Kind: "post", Text: "Our post\nsecond line", ThreadURL: "https://www.threads.com/@us/post/m1", MediaID: "m1", PostedAt: now.Add(-2 * time.Hour)},
		{AgentID: "agent", Kind: "post", Text: "Too old to look at", ThreadURL: "https://www.threads.com/@us/post/m0", MediaID: "m0", PostedAt: now.Add(-100 * time.Hour)},
	} {
		if _, err := st.SeedAction(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	answered := inboxPost("c1", "carol", now.Add(-40*time.Minute), "answered already")
	if _, err := st.DB().Exec(`INSERT INTO actions(agent_id,kind,text,target_url,target_username,dry_run,posted_at) VALUES('agent','reply','thanks',?,'carol',0,?)`, answered.URL, now.Add(-30*time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkDoNotContact(ctx, "agent", "erin", "owner_request"); err != nil {
		t.Fatal(err)
	}
	mem.Replies = map[string]threads.Page{
		"m1": {Kind: "replies", Posts: []threads.Post{
			inboxPost("a1", "alice", now.Add(-30*time.Minute), "alice says hi"),
			inboxPost("b1", "us", now.Add(-35*time.Minute), "our own reply"),
			answered,
			inboxPost("d1", "dave", now.Add(-80*time.Hour), "too old"),
			inboxPost("e1", "erin", now.Add(-20*time.Minute), "do not contact"),
			inboxPost("f1", "frank", now.Add(-25*time.Minute), "we answered under it"),
		}},
		"": {Kind: "mentions", Posts: []threads.Post{
			inboxPost("h1", "hank", now.Add(-10*time.Minute), "hank mentions us"),
			inboxPost("a1", "alice", now.Add(-30*time.Minute), "alice says hi"),
		}},
		"f1": {Kind: "replies", Posts: []threads.Post{inboxPost("f2", "us", now.Add(-5*time.Minute), "reply")}},
	}

	_, items := d.Inbox(ctx)
	if len(items) != 2 || items[0].Author != "hank" || items[1].Author != "alice" {
		t.Fatalf("%+v", items)
	}
	if items[1].ReplyingTo != "Our post" || items[0].ReplyingTo != "(a mention of the account)" || items[1].AgeMinutes != 30 || !strings.HasPrefix(items[1].Ref, "r") {
		t.Fatalf("%+v", items)
	}

	// Answering by ref uses the API path (target_id), is no browser reply and
	// is not capped by replies_per_user_7d, so a back-and-forth continues.
	args := func(item tools.InboxItem, text string) string {
		return `{"text":"` + text + `","target_url":"` + item.Ref + `","target_id":"` + item.TargetID + `","target_username":"` + item.Author + `"}`
	}
	if _, err := d.Dispatch(ctx, call("reply", args(items[1], "chao alice"))); err != nil {
		t.Fatal(err)
	}
	var target, user string
	var browser bool
	if err := st.DB().QueryRow(`SELECT target_url,target_username,browser FROM actions WHERE text='chao alice'`).Scan(&target, &user, &browser); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(target, "/@alice/post/a1") || user != "alice" || browser {
		t.Fatalf("%q %q %v", target, user, browser)
	}
	now = now.Add(time.Minute)
	mem.Replies["m1"] = threads.Page{Kind: "replies", Posts: append(mem.Replies["m1"].Posts, inboxPost("a2", "alice", now.Add(-time.Second), "and another"))}
	_, again := d.Inbox(ctx)
	var second tools.InboxItem
	for _, it := range again {
		if it.Author == "alice" {
			second = it
		}
	}
	if len(again) != 2 || second.TargetID != "a2" {
		t.Fatalf("answered reply listed again or new one missing: %+v", again)
	}
	now = now.Add(time.Minute)
	if _, err := d.Dispatch(ctx, call("reply", args(second, "va tiep"))); err != nil {
		t.Fatalf("second answer to the same person: %v", err)
	}
}

func TestReadInboxCountsRepliesWithoutATimestamp(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	st, _, _ := setup(t)
	mem := threads.NewMemory()
	mem.Replies = map[string]threads.Page{
		"": {Kind: "mentions", Skipped: 2, Posts: []threads.Post{
			inboxPost("h1", "hank", now.Add(-10*time.Minute), "ok"),
			inboxPost("h2", "ivy", time.Time{}, "no timestamp"),
		}},
	}
	got, skipped, err := tools.ReadInbox(ctx, tools.InboxRead{Store: st, Threads: mem, AgentID: "agent", Now: now, IsOwn: func(string) bool { return false }})
	if err != nil || len(got) != 1 || got[0].Post.ID != "h1" || skipped != 3 {
		t.Fatalf("got=%+v skipped=%d err=%v", got, skipped, err)
	}
}
