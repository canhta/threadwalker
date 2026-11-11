package guard_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/canhta/threadwalker/internal/guard"
)

func TestOwnAccountsBlockedThroughEveryDeclaredAddress(t *testing.T) {
	for _, args := range []string{
		`{"text":"A synthetic reply","target_username":"@FIXTURE_OTHER","target_url":"https://www.threads.net/@fixture_other/post/1"}`,
		`{"text":"A synthetic reply","target_username":"stranger","target_url":"https://THREADS.NET/@fixture_other/post/1?x=1"}`,
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
