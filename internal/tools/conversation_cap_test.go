package tools_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/tools"
)

func TestConversationCapRefusesTheThirdReplyAndHidesTheInboxItem(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	st, opts, _ := setup(t)
	mem := confirmingMemory("https://threads.com/@us/post/new")
	mem.Pages[threads.OpReadReplies] = threads.Page{Kind: "replies"}
	opts.Threads = mem
	opts.Template.DryRun = false
	opts.Template.Limits.RepliesPerConversation, opts.Template.Limits.ConversationHotLikes, opts.Template.Limits.ConversationHotReplies = 2, 5, 3
	opts.Scope = guard.Scope{Store: st, Template: opts.Template, WakeID: 1, AllowLive: true, Now: func() time.Time { return now }, OwnUsernames: []string{"us"}}
	d, err := tools.NewDispatcher(opts)
	if err != nil {
		t.Fatal(err)
	}
	rootA, rootB := "https://threads.com/@xx/post/RA", "https://threads.com/@xx/post/RB"
	var firstA int64
	for _, s := range []struct{ id, root string }{{"oA", rootA}, {"oB", rootB}} {
		res, err := st.DB().Exec(`INSERT INTO actions(agent_id,kind,text,target_url,target_username,dry_run,posted_at,thread_url,media_id,root_url) VALUES('agent','reply','first',?,'xx',0,?,?,?,?)`, s.root, now.Add(-time.Hour).Format(time.RFC3339Nano), "https://www.threads.com/@us/post/"+s.id, s.id, s.root)
		if err != nil {
			t.Fatal(err)
		}
		if s.id == "oA" {
			firstA, _ = res.LastInsertId()
		}
	}
	mem.Replies = map[string]threads.Page{
		"oA": {Kind: "replies", Posts: []threads.Post{inboxPost("p1", "yy", now.Add(-20*time.Minute), "one"), inboxPost("p2", "zz", now.Add(-19*time.Minute), "two")}},
		"oB": {Kind: "replies", Posts: []threads.Post{inboxPost("q1", "ww", now.Add(-18*time.Minute), "other conversation")}},
	}
	_, items := d.Inbox(ctx)
	byID := map[string]tools.InboxItem{}
	for _, it := range items {
		byID[it.TargetID] = it
	}
	if len(items) != 3 {
		t.Fatalf("%+v", items)
	}
	reply := func(id string) error {
		now = now.Add(time.Minute)
		it := byID[id]
		_, err := d.Dispatch(ctx, call("reply", `{"text":"ok `+id+`","target_url":"`+it.Ref+`","target_id":"`+id+`","target_username":"`+it.Author+`"}`))
		return err
	}
	if err := reply("p1"); err != nil {
		t.Fatalf("second reply in the conversation: %v", err)
	}
	var blocked *guard.BlockedError
	if err := reply("p2"); !errors.As(err, &blocked) || blocked.Reason != "replies_per_conversation" {
		t.Fatalf("third reply: %v", err)
	}
	if err := reply("q1"); err != nil {
		t.Fatalf("a different root is independent: %v", err)
	}
	mem.Replies["oA"] = threads.Page{Kind: "replies", Posts: []threads.Post{inboxPost("p3", "vv", now.Add(-time.Minute), "late")}}
	if _, items = d.Inbox(ctx); len(items) != 0 {
		t.Fatalf("capped conversation still listed: %+v", items)
	}

	// The first reply took off: its newest metric has 5 likes, so the cap is lifted.
	if _, err := st.DB().Exec(`INSERT INTO metrics(agent_id,action_id,measured_at,likes,replies) VALUES('agent',?,?,1,0),('agent',?,?,5,0)`, firstA, now.Add(-time.Hour).Format(time.RFC3339Nano), firstA, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	_, items = d.Inbox(ctx)
	if len(items) != 1 || items[0].TargetID != "p3" {
		t.Fatalf("hot conversation not listed: %+v", items)
	}
	if err := reply("p2"); err != nil {
		t.Fatalf("hot conversation: %v", err)
	}
}

func TestReplyStoresItsConversationRoot(t *testing.T) {
	st, opts, _ := setup(t)
	opts.Template.Limits.BrowserRepliesPerDay = 5
	opts.Scope = guard.Scope{Store: st, Template: opts.Template, WakeID: 1, Now: opts.Now}
	d, err := tools.NewDispatcher(opts)
	if err != nil {
		t.Fatal(err)
	}
	d.Guard().NoteRead("", "https://www.threads.com/@xx/post/feed1?x=1", "")
	args := `{"text":"Synthetic reply","target_username":"xx","target_url":"https://www.threads.com/@xx/post/feed1?x=1"}`
	if _, err := d.Dispatch(context.Background(), call("reply", args)); err != nil {
		t.Fatal(err)
	}
	var root string
	if err := st.DB().QueryRow(`SELECT root_url FROM actions WHERE kind='reply'`).Scan(&root); err != nil || root != "https://threads.com/@xx/post/feed1" {
		t.Fatalf("%q %v", root, err)
	}
}
