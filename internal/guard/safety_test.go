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
		{"thả tim cho mình nha", "thả tim cho mình nha", "engagement_bait"},
		{"thả tym cho mình nha", "thả tym cho mình nha", "engagement_bait"},
		{"tha tim cho minh nha", "tha tim cho minh nha", "engagement_bait"},
		{"xin 1 tym", "xin 1 tym", "engagement_bait"},
		{"follow mk nha", "follow mk nha", "engagement_bait"},
		{"follow t nha", "follow t nha", "engagement_bait"},
		{"nhớ follow nha mn", "nhớ follow nha mn", "engagement_bait"},
		{"follow để xem phần 2", "follow để xem phần 2", "engagement_bait"},
		{"để lại 1 tim nếu thấy đúng", "để lại 1 tim nếu thấy đúng", "engagement_bait"},
		{"chia sẻ giúp mình", "chia sẻ giúp mình", "engagement_bait"},
		{"share giúp mình", "share giúp mình", "engagement_bait"},
		{"repost giúp mình", "repost giúp mình", "engagement_bait"},
		{"❤️ đi mn", "❤️ đi mn", "engagement_bait"},
		{"Thả tim cho mình đi", "Thả tim cho mình đi", "engagement_bait"},
		{"Follow mình để xem phần 2", "Follow mình để xem phần 2", "engagement_bait"},
		{"Theo dõi mình nhé", "Theo dõi mình nhé", "engagement_bait"},
		{"Sơn Tùng vừa chia sẻ ngay sau show", "Sơn Tùng vừa chia sẻ ngay sau show", ""},
		{"idol chia sẻ cho fan một tin vui", "idol chia sẻ cho fan một tin vui", ""},
		{"ai share cho mình link full với", "ai share cho mình link full với", ""},
		{"xem xong đau tim nha mn", "xem xong đau tim nha mn", ""},
		{"clip này rụng tim nha", "clip này rụng tim nha", ""},
		{"trái tim đi lạc rồi", "trái tim đi lạc rồi", ""},
		{"giữ tim nhé mn", "giữ tim nhé mn", ""},
		{"mình đã thả tim cho idol rồi", "mình đã thả tim cho idol rồi", ""},
		{"anh ấy theo dõi em suốt cả tuần", "anh ấy theo dõi em suốt cả tuần", ""},
		{"follow page chính thức của BTC", "follow page chính thức của BTC", ""},
		{"mình đã thả tim bài này, tim mình tan chảy", "mình đã thả tim bài này, tim mình tan chảy", ""},
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
