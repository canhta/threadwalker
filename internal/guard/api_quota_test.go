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

func TestReplyTargetResolvesToTheStoredPost(t *testing.T) {
	for name, tc := range map[string]struct {
		id, target string
		wantID     string
		want       string
	}{
		"post with an API id":     {id: "id-1", wantID: "id-1"},
		"feed post, browser path": {id: "", wantID: ""},
		"unknown ref":             {id: "id-1", target: "p999", want: "unknown_target"},
		"not a ref":               {id: "id-1", target: pairURL, want: "unknown_target"},
	} {
		t.Run(name, func(t *testing.T) {
			g, err := guard.New(liveOpts(t))
			if err != nil {
				t.Fatal(err)
			}
			ref := readPost(t, g, pairURL, tc.id, "")
			if tc.target != "" {
				ref = tc.target
			}
			var resolved guard.ResolvedTarget
			_, err = g.Execute(context.Background(), guard.Attempt{Tool: "reply", Target: &resolved, Arguments: `{"text":"ok","target":"` + ref + `"}`}, func() (string, error) { return "", nil })
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if resolved.URL != pairURL || resolved.MediaID != tc.wantID {
					t.Fatalf("resolved %+v, want %s and id %q", resolved, pairURL, tc.wantID)
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
	reply := guard.Attempt{Tool: "reply", Arguments: replyTo(t, g, pairURL, "id-1", "ok")}
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
		_, err := g.Execute(context.Background(), guard.Attempt{Tool: "reply", Arguments: replyTo(t, g, "https://threads.com/@"+user+"/post/"+post, id, "ok")}, func() (string, error) { return "", nil })
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
