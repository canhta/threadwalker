package dashboard_test

import (
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/dashboard"
	"github.com/canhta/threadz/internal/store"
)

func i64(n int64) *int64 { return &n }

func scored() store.DashboardSnapshot {
	snap := started()
	p1 := pub(2, "post", ago(2*day), ownLink("HIT1"))
	p1.FollowersAtPost = i64(100)
	p1.Text = "post\nbody"
	p2 := pub(1, "post", ago(4*day), ownLink("MISS1"))
	p2.FollowersAtPost = i64(98)
	snap.Publications = []store.DashboardPublication{p1, p2}
	m := func(a int64, at int, views, likes, quotes, followers int64) store.DashboardMetric {
		return store.DashboardMetric{ActionID: a, MeasuredAt: ago(time3(at)), Views: views, Likes: likes, Quotes: quotes, Followers: i64(followers)}
	}
	snap.Metrics = []store.DashboardMetric{m(2, 1, 2000, 100, 4, 110), m(1, 3, 500, 5, 0, 99)}
	snap.Phases = []store.DashboardPhaseSpend{
		{Day: "2026-11-12", Phase: "agent", CostUSD: 0.4, TokensIn: 9000, TokensOut: 1200},
		{Day: "2026-11-11", Phase: "dream", CostUSD: 0.2, TokensIn: 4000, TokensOut: 800},
	}
	snap.Ledger = []store.DashboardLedgerDay{{Day: "2026-11-11", CostUSD: 0.2}, {Day: "2026-11-12", CostUSD: 0.4}}
	snap.SpendTotal = 1.5
	w := finishedWake(1, "agent", "completed", 3)
	w.CostUSD = 0.3
	snap.Wakes = []store.DashboardWake{w}
	snap.ToolCalls = []store.DashboardToolCall{call(1, 1, "post", `{"outcome":"error"}`, "engagement_bait"), call(2, 1, "find_posts", `{"outcome":"success"}`, "")}
	return snap
}

func TestPostedGrowthAndCostShowInPublicMode(t *testing.T) {
	page := mustPage(t, newHandler(newSource(scored())))
	if len(page.Growth.Posts) != 2 {
		t.Fatalf("posts: %+v", page.Growth.Posts)
	}
	byLink := map[string]dashboard.PostRow{}
	for _, p := range page.Growth.Posts {
		byLink[p.Link] = p
	}
	if p := byLink["post body"]; p.Label != "" {
		t.Fatalf("public post row: %+v", p)
	}
	if !page.Followers.HasData || page.Followers.Now != "110" || page.Followers.PerPost != "+5.5" || page.Followers.Per1000 != "+4.40" {
		t.Fatalf("followers: %+v", page.Followers)
	}
	if page.Blocked.Total != 1 || page.Blocked.Rows[0].Raw != "engagement_bait" {
		t.Fatalf("blocked: %+v", page.Blocked)
	}
	r := page.Return
	if r.SpentTotal != "$1.50" || r.CostPerPost == "—" || r.CostPerFollow == "—" {
		t.Fatalf("cost: %+v", r)
	}
	body := get(newHandler(newSource(scored())), "/").Body.String()
	if !strings.Contains(body, "post body") {
		t.Fatal("public page missing the post text")
	}
}

func time3(days int) time.Duration { return time.Duration(days) * day }
