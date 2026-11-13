package threads

import (
	"context"
	"github.com/canhta/threadz/internal/stdx"
	"os"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
)

type envToken struct{}

func (envToken) Token(context.Context) (config.Secret, error) {
	return config.NewSecret(os.Getenv("THREADS_ACCESS_TOKEN")), nil
}

// TestLiveSmokeReadOnly reads /me, account insights, followers_count and the
// publishing limit of the tester account. It never publishes. Opt in with
// THREADZ_LIVE_THREADS=1 plus THREADS_ACCESS_TOKEN and THREADS_USER_ID.
func TestLiveSmokeReadOnly(t *testing.T) {
	if os.Getenv("THREADZ_LIVE_THREADS") != "1" || os.Getenv("THREADS_ACCESS_TOKEN") == "" || os.Getenv("THREADS_USER_ID") == "" {
		t.Skip("set THREADZ_LIVE_THREADS=1, THREADS_ACCESS_TOKEN and THREADS_USER_ID for the live read-only smoke test")
	}
	api, err := NewAPI(APIOptions{UserID: os.Getenv("THREADS_USER_ID"), Tokens: envToken{}, Wait: stdx.Sleep, Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var me struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	if err = api.do(ctx, "GET", "/me", map[string][]string{"fields": {"id,username"}}, &me); err != nil || me.ID != os.Getenv("THREADS_USER_ID") || me.Username == "" {
		t.Fatalf("/me id match=%v username set=%v err=%v", me.ID == os.Getenv("THREADS_USER_ID"), me.Username != "", err)
	}
	var followers insights
	if err = api.do(ctx, "GET", "/"+api.o.UserID+"/threads_insights", map[string][]string{"metric": {"followers_count"}}, &followers); err != nil || len(followers.Data) != 1 || followers.Data[0].Name != "followers_count" {
		t.Fatalf("followers_count: %v", err)
	}
	m, err := api.ReadOwnMetrics(ctx)
	if err != nil || m.Views < 0 || m.Followers == nil || *m.Followers < 0 {
		t.Fatalf("account insights %+v: %v", m, err)
	}
	q, err := api.PublishingLimit(ctx)
	if err != nil || q.PostsTotal <= 0 || q.RepliesTotal <= 0 {
		t.Fatalf("publishing limit %+v: %v", q, err)
	}
	t.Logf("live read-only: /me ok, followers_count ok, account insights views=%d likes=%d followers=%v, posts quota %d/%d, replies quota %d/%d", m.Views, m.Likes, m.Followers != nil, q.PostsUsed, q.PostsTotal, q.RepliesUsed, q.RepliesTotal)
}
