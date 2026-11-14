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
		{"heart ask", "thả tim cho mình nha", "engagement_bait"},
		{"heart ask, short", "tim đi mn", "engagement_bait"},
		{"heart ask, command", "thả tim đi", "engagement_bait"},
		{"heart ask, begging", "cho mình xin 1 tim", "engagement_bait"},
		{"follow ask", "nhớ follow mình nha", "engagement_bait"},
		{"follow ask, Vietnamese", "theo dõi mình để xem tiếp", "engagement_bait"},
		{"share ask", "share ngay cho bạn bè", "engagement_bait"},
		{"a heart that is not asked for", "mình đã thả tim bài này, tim mình tan chảy", ""},
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
