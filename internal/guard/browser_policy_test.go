package guard_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/guard"
)

func TestBrowserCapabilityPreservesPolicyAndUnconfirmedIntent(t *testing.T) {
	st, opts := setup(t)
	opts.Template.DryRun = false
	opts.Template.Limits.PostsPerDay = 2
	opts.Template.Limits.RepliesPerDay = 2
	opts.Template.Limits.RepliesPerUser7d = 2
	opts.Template.Limits.MinSecondsBetweenActions = 1
	if _, err := guard.New(opts); err == nil {
		t.Fatal("non-dry-run requires trusted browser capability")
	}
	opts.AllowLive = true
	g, err := guard.New(opts)
	if err != nil {
		t.Fatalf("browser guard: %v", err)
	}
	var reserved int64
	calls := 0
	_, err = g.Execute(context.Background(), guard.Attempt{Tool: "reply", Arguments: `{"text":"Synthetic reply","target":"` + readPost(t, g, "https://threads.com/@fixture_author/post/1?tracking=x", "tid", "") + `"}`, ReservedActionID: &reserved}, func() (string, error) {
		calls++
		var dry bool
		var confirmed string
		if err := st.DB().QueryRow(`SELECT dry_run,thread_url FROM actions WHERE id=?`, reserved).Scan(&dry, &confirmed); err != nil || dry || confirmed != "" {
			t.Fatalf("intent before submit: dry=%v confirmed=%q err=%v", dry, confirmed, err)
		}
		return "", errors.New("synthetic uncertain submit")
	})
	if err == nil || calls != 1 || reserved == 0 {
		t.Fatal("uncertain outcome lost its intent")
	}
	opts.Now = func() time.Time { return time.Date(2026, 11, 11, 0, 1, 0, 0, time.UTC) }
	g, _ = guard.New(opts)
	_, err = g.Execute(context.Background(), guard.Attempt{Tool: "reply", Arguments: `{"text":"Another reply","target":"` + readPost(t, g, "https://threads.com/@fixture_author/post/1#fragment", "tid", "") + `"}`}, func() (string, error) { calls++; return "", nil })
	if !errors.Is(err, guard.ErrBlocked) || calls != 1 {
		t.Fatalf("unknown intent must count toward thread quota: %v calls=%d", err, calls)
	}
}
