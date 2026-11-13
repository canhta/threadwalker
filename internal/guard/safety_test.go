package guard_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/canhta/threadz/internal/guard"
)

func postReason(t *testing.T, opts guard.Scope, args string) (blocked string, err error) {
	t.Helper()
	opts.Template.Limits.PostsPerDay, opts.Template.Limits.RepliesPerDay, opts.Template.Limits.RepliesPerUser7d, opts.Template.Limits.MinSecondsBetweenActions = 100, 100, 100, 1
	g, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	_, err = g.Execute(context.Background(), guard.Attempt{Tool: "post", Arguments: args}, func() (string, error) { return "", nil })
	if err != nil && !errors.Is(err, guard.ErrBlocked) {
		t.Fatal(err)
	}
	_ = opts.Store.DB().QueryRow(`SELECT blocked_by FROM tool_calls ORDER BY id DESC LIMIT 1`).Scan(&blocked)
	return blocked, err
}

func TestHardContentRulesBlockAndStyleAvoidNever(t *testing.T) {
	for _, tc := range []struct{ name, text, want string }{
		{"style avoid", "Tuy nhiên, hôm nay tôi ship xong", ""},
		{"plain", "ship xong rồi các con vợ", ""},
		{"blocked phrase", "KÈO CHẮC THẮNG nè", "blocked_phrase"},
		{"appearance", "mày xấu quá", "identity_targeting"},
		{"region", "đồ bắc kỳ", "identity_targeting"},
		{"private life", "vợ của mày ngoại tình", "identity_targeting"},
		{"english", "you are so ugly", "identity_targeting"},
		{"political", "mọi người nên bầu cho ông A", "political_recommendation"},
		{"vote", "vote for him", "political_recommendation"},
		{"comment bait", "comment ngay để nhận quà", "engagement_bait"},
		{"tag bait", "tag a friend who needs this", "engagement_bait"},
		{"share bait", "like và share nhé", "engagement_bait"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, opts := setup(t)
			opts.Template.Guard.BlockedPhrases = []string{"kèo chắc thắng"}
			opts.Template.Persona.StyleAvoid = []string{"Tuy nhiên,"}
			got, err := postReason(t, opts, fmt.Sprintf(`{"text":%q}`, tc.text))
			if got != tc.want || (tc.want == "") != (err == nil) {
				t.Fatalf("blocked_by=%q err=%v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestExploreBudgetOverRollingWindow(t *testing.T) {
	for _, tc := range []struct {
		name            string
		percent         int
		oldExplore      int
		recent, explore int
		want            string
	}{
		{"within budget", 30, 0, 10, 2, ""},
		{"at budget edge", 30, 0, 19, 5, ""},
		{"over budget", 30, 0, 19, 6, "explore_budget"},
		{"disabled", 0, 0, 10, 0, "explore_budget"},
		{"old explores roll out of the window", 30, 10, 20, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, opts := setup(t)
			opts.Template.Limits.ExplorePercent = tc.percent
			insert := func(n, explore int) {
				for i := 0; i < n; i++ {
					flag := 0
					if i < explore {
						flag = 1
					}
					if _, err := st.DB().Exec(`INSERT INTO actions(agent_id,kind,text,posted_at,explore) VALUES('agent','post','x','2026-11-01T00:00:00Z',?)`, flag); err != nil {
						t.Fatal(err)
					}
				}
			}
			insert(tc.oldExplore, tc.oldExplore)
			insert(tc.recent, tc.explore)
			got, _ := postReason(t, opts, `{"text":"thử cái mới","explore":true}`)
			if got != tc.want {
				t.Fatalf("blocked_by=%q, want %q", got, tc.want)
			}
			if tc.want == "" {
				var flagged int
				if err := st.DB().QueryRow(`SELECT explore FROM actions ORDER BY id DESC LIMIT 1`).Scan(&flagged); err != nil || flagged != 1 {
					t.Fatalf("explore flag not stored: %d %v", flagged, err)
				}
			}
		})
	}
}
