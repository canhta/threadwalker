package guard_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/quality"
)

func TestRepeatedClosingTokenIsFlaggedBeforeTheCritic(t *testing.T) {
	for _, tc := range []struct {
		name, tail, draft string
		dryRun            int // mode of the seeded rows; the template is dry-run
		want              bool
	}{
		{"emoticon run repeated in 5 replies", "hay quá :)))", "giá vé tăng gấp đôi thật luôn :)))", 1, true},
		{"emoji repeated", "thương quá 😭", "vé hết trong 5 phút 😭", 1, true},
		{"emoticon run of another length", "hay quá :))))", "giá vé tăng gấp đôi thật luôn :)))))", 1, true},
		{"laugh token", "hay quá kkkk", "giá vé tăng gấp đôi kkk", 1, true},
		{"a different ending passes", "hay quá :)))", "giá vé tăng gấp đôi thật luôn", 1, false},
		{"live rows are another mode", "hay quá :)))", "giá vé tăng gấp đôi thật luôn :)))", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, scope := setup(t)
			for i := 0; i < 5; i++ {
				if _, err := st.DB().Exec(`INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,target_username,target_url) VALUES('agent','reply',?,?,'2026-11-10T00:00:00Z','other_user','https://www.threads.com/@other_user/post/x')`, "câu số "+string(rune('a'+i))+" "+tc.tail, tc.dryRun); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			scope.Critic = func(context.Context, quality.CriticInput) (quality.CriticVerdict, error) {
				calls++
				return verdict(true), nil
			}
			args := `{"text":"` + tc.draft + `","target":"p1"}`
			_, err := criticReply(criticGuard(t, scope), args)
			var blocked *guard.BlockedError
			if !tc.want {
				if err != nil || calls != 1 {
					t.Fatalf("err %v critic calls %d, want a pass through the critic", err, calls)
				}
				return
			}
			if !errors.As(err, &blocked) || blocked.Reason != "repeat_ending" || blocked.Detail == "" || !strings.Contains(guard.Hint(blocked.Reason, blocked.Detail), blocked.Detail) || calls != 0 {
				t.Fatalf("err %v critic calls %d, want repeat_ending before the critic", err, calls)
			}
		})
	}
}
