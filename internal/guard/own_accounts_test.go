package guard_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/guard"
)

func TestOwnAccountsBlockedThroughEveryDeclaredAddress(t *testing.T) {
	for _, args := range []string{
		`{"text":"A synthetic reply","target_username":"@FIXTURE_OTHER","target_id": "tid", "target_url":"https://www.threads.net/@fixture_other/post/1"}`,
		`{"text":"A synthetic reply","target_username":"stranger","target_id": "tid", "target_url":"https://THREADS.NET/@fixture_other/post/1?x=1"}`,
		`{"text":"Hi @fixture_other"}`,
		`{"text":"Read https://www.threads.com/@fixture_other/post/1"}`,
		`{"text":"Read threads.net/@fixture_other"}`,
	} {
		t.Run(args, func(t *testing.T) {
			_, opts := setup(t)
			opts.OwnUsernames = []string{"fixture_other"}
			opts.Template.Threads.Username = "fixture_self"
			g, err := guard.New(opts)
			if err != nil {
				t.Fatal(err)
			}
			tool := "post"
			if strings.Contains(args, "target_url") {
				tool = "reply"
			}
			called := false
			_, err = g.Execute(context.Background(), guard.Attempt{Tool: tool, Arguments: args}, func() (string, error) { called = true; return "", nil })
			if !errors.Is(err, guard.ErrBlocked) || called || !strings.Contains(err.Error(), "own_account") {
				t.Fatalf("own account escaped: called=%v err=%v", called, err)
			}
		})
	}
}

func TestConfirmedOwnPublicationCannotBeDisguisedAsForeign(t *testing.T) {
	for _, args := range []string{
		`{"text":"Synthetic reply","target_username":"stranger","target_id": "tid", "target_url":"http://www.THREADS.NET/@stranger/post/opaque-publication/?tracking=1"}`,
		`{"text":"See https://threads.net/@stranger/post/opaque-publication"}`,
	} {
		t.Run(args, func(t *testing.T) {
			st, opts := setup(t)
			opts.Template.Limits.PostsPerDay = 5
			opts.Template.Limits.RepliesPerDay = 5
			opts.Template.Limits.RepliesPerUser7d = 2
			opts.Template.Limits.MinSecondsBetweenActions = 1
			if _, err := st.DB().Exec(`INSERT INTO actions(agent_id,kind,text,posted_at,thread_url) VALUES('other','post','synthetic confirmed publication','2026-11-10T00:00:00Z','https://threads.net/@stranger/post/opaque-publication')`); err != nil {
				t.Fatal(err)
			}
			g, err := guard.New(opts)
			if err != nil {
				t.Fatal(err)
			}
			tool := "post"
			if strings.Contains(args, "target_url") {
				tool = "reply"
			}
			g.NoteRead("tid", "https://threads.net/@stranger/post/opaque-publication", "")
			called := false
			_, err = g.Execute(context.Background(), guard.Attempt{Tool: tool, Arguments: args}, func() (string, error) { called = true; return "", nil })
			if called || !errors.Is(err, guard.ErrBlocked) || !strings.Contains(err.Error(), "own_account") {
				t.Fatalf("confirmed own URL escaped: %v", err)
			}
		})
	}
}
