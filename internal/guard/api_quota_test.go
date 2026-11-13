package guard_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/guard"
)

const pairURL = "https://threads.com/@fixture_author/post/1"

func liveOpts(t *testing.T) guard.Scope {
	t.Helper()
	_, opts := setup(t)
	opts.Template.DryRun = false
	opts.AllowLive = true
	opts.Template.Limits.PostsPerDay = 5
	opts.Template.Limits.RepliesPerDay = 5
	opts.Template.Limits.RepliesPerUser7d = 5
	opts.Template.Limits.BrowserRepliesPerDay = 5
	opts.Template.Limits.MinSecondsBetweenActions = 1
	return opts
}

func blockedReason(t *testing.T, err error) string {
	t.Helper()
	var b *guard.BlockedError
	if !errors.As(err, &b) {
		t.Fatalf("not a block: %v", err)
	}
	return b.Reason
}

func TestReplyNeedsTheIDAndPermalinkSeenTogether(t *testing.T) {
	for name, tc := range map[string]struct {
		noted [2]string
		args  string
		want  string
	}{
		"matching pair":       {[2]string{"id-1", pairURL}, `"target_id":"id-1"`, ""},
		"other id":            {[2]string{"id-1", pairURL}, `"target_id":"id-2"`, "reply_target_unread"},
		"id seen without url": {[2]string{"id-1", "https://threads.com/@fixture_author/post/9"}, `"target_id":"id-1"`, "reply_target_unread"},
		"missing id":          {[2]string{"id-1", pairURL}, `"target_id":""`, "reply_target_unread"},
		"feed post":           {[2]string{"", pairURL}, `"target_id":""`, ""},
		"feed post, no id":    {[2]string{"", pairURL}, `"target_id":"id-1"`, "reply_target_unread"},
	} {
		t.Run(name, func(t *testing.T) {
			g, err := guard.New(liveOpts(t))
			if err != nil {
				t.Fatal(err)
			}
			g.NoteRead(tc.noted[0], tc.noted[1], "")
			_, err = g.Execute(context.Background(), guard.Attempt{Tool: "reply", Arguments: `{"text":"ok","target_username":"fixture_author",` + tc.args + `,"target_url":"` + pairURL + `"}`}, func() (string, error) { return "", nil })
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if got := blockedReason(t, err); got != tc.want {
				t.Fatalf("reason=%q want %q", got, tc.want)
			}
		})
	}
}

func TestAPIQuotaIsAnExtraLimit(t *testing.T) {
	quota := guard.APIQuota{Posts: 1, Replies: 0}
	var qerr error
	opts := liveOpts(t)
	clock := opts.Now()
	opts.Now = func() time.Time { clock = clock.Add(time.Minute); return clock }
	opts.APIQuota = func(context.Context) (guard.APIQuota, error) { return quota, qerr }
	g, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	g.NoteRead("id-1", pairURL, "")
	reply := guard.Attempt{Tool: "reply", Arguments: `{"text":"ok","target_username":"fixture_author","target_id":"id-1","target_url":"` + pairURL + `"}`}
	post := guard.Attempt{Tool: "post", Arguments: `{"text":"hello"}`}
	run := func(a guard.Attempt) error {
		_, err := g.Execute(context.Background(), a, func() (string, error) { return "", nil })
		return err
	}
	if got := blockedReason(t, run(reply)); got != "api_quota_exhausted" {
		t.Fatalf("reply with no API quota left: %q", got)
	}
	if err := run(post); err != nil {
		t.Fatalf("post with quota left: %v", err)
	}
	quota = guard.APIQuota{Posts: -1, Replies: -1}
	if err := run(reply); err != nil {
		t.Fatalf("unlimited API quota: %v", err)
	}
	quota = guard.APIQuota{Posts: 0, Replies: 5}
	if got := blockedReason(t, run(post)); got != "api_quota_exhausted" {
		t.Fatalf("exhausted post quota: %q", got)
	}
	qerr = errors.New("api down")
	if got := blockedReason(t, run(post)); got != "api_quota_unreadable" {
		t.Fatalf("unreadable quota must fail closed: %q", got)
	}
}

func TestLiveQuotaIgnoresDryRunActions(t *testing.T) {
	opts := liveOpts(t)
	opts.Template.Limits.PostsPerDay = 3
	for i := 0; i < 3; i++ {
		if _, err := opts.Store.DB().Exec(`INSERT INTO actions(agent_id,kind,text,posted_at,dry_run) VALUES(?,'post',?,?,1)`, opts.Template.ID, "rehearsal "+string(rune('a'+i)), opts.Now().Add(-time.Hour).Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	g, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.Execute(context.Background(), guard.Attempt{Tool: "post", Arguments: `{"text":"first live post"}`}, func() (string, error) { return "", nil }); err != nil {
		t.Fatalf("live post blocked by dry-run rows: %v", err)
	}
}

func TestBrowserRepliesHaveTheirOwnDailyCap(t *testing.T) {
	opts := liveOpts(t)
	opts.Template.Limits.BrowserRepliesPerDay = 1
	now := opts.Now()
	opts.Now = func() time.Time { return now }
	g, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	reply := func(user, id, post string) error {
		now = now.Add(time.Minute)
		g.NoteRead(id, "https://threads.com/@"+user+"/post/"+post, "")
		_, err := g.Execute(context.Background(), guard.Attempt{Tool: "reply", Arguments: `{"text":"ok","target_username":"` + user + `","target_id":"` + id + `","target_url":"https://threads.com/@` + user + `/post/` + post + `"}`}, func() (string, error) { return "", nil })
		return err
	}
	if err := reply("feed_one", "", "1"); err != nil {
		t.Fatal(err)
	}
	if got := blockedReason(t, reply("feed_two", "", "2")); got != "browser_replies_per_day" {
		t.Fatalf("second feed reply: %q", got)
	}
	if err := reply("api_user", "id-3", "3"); err != nil {
		t.Fatalf("API reply counted against the browser cap: %v", err)
	}
}
