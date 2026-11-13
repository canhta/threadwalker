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
	p1.Label, p1.ReachLift, p1.ResonanceLift, p1.FollowersAtPost = store.LabelViral, 0.7, 0.2, i64(100)
	p1.Text, p1.Why = "post\nbody", []string{"private plan"}
	p2 := pub(1, "post", ago(4*day), ownLink("MISS1"))
	p2.Label, p2.FollowersAtPost = store.LabelFlop, i64(98)
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
	snap.ToolCalls = []store.DashboardToolCall{call(1, 1, "post", `{"outcome":"error"}`, "engagement_bait"), call(2, 1, "search", `{"outcome":"success"}`, "")}
	snap.Hypotheses = []store.DashboardHypothesis{{ID: 7, Status: "testing", N: 2, Text: "an idea"}}
	snap.Lifts = []store.PlaybookLift{{Hypothesis: 7, Status: "testing", Counted: true, ReachLift: 0.5, ResonanceLift: 0.3}}
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
	if p := byLink["post body"]; p.Label != "viral" || len(p.Why) != 0 {
		t.Fatalf("public post row: %+v", p)
	}
	if !page.Followers.HasData || page.Followers.Now != "110" || page.Followers.PerPost != "+5.5" || page.Followers.Per1000 != "+4.40" {
		t.Fatalf("followers: %+v", page.Followers)
	}
	if !page.Outcomes.HasData || page.Outcomes.Viral != 1 || page.Outcomes.Flop != 1 {
		t.Fatalf("outcomes: %+v", page.Outcomes)
	}
	if page.Blocked.Total != 1 || page.Blocked.Rows[0].Raw != "engagement_bait" {
		t.Fatalf("blocked: %+v", page.Blocked)
	}
	r := page.Return
	if r.SpentTotal != "$1.50" || r.CostPerPost == "—" || r.CostPerFollow == "—" {
		t.Fatalf("cost: %+v", r)
	}
	if len(page.Learned.Posteriors) != 1 {
		t.Fatalf("standings: %+v", page.Learned)
	}
	body := get(newHandler(newSource(scored())), "/").Body.String()
	for _, want := range []string{"post body", "an idea"} {
		if !strings.Contains(body, want) {
			t.Fatalf("public page missing %q", want)
		}
	}
	for _, secret := range []string{"private plan"} {
		if strings.Contains(body, secret) {
			t.Fatalf("public page leaks %q", secret)
		}
	}
}

func TestContentModeAddsPostTextAndPlan(t *testing.T) {
	const token = "synthetic-token-123456"
	h := newHandler(newSource(scored()), func(o *dashboard.Options) { o.Content, o.ContentToken = true, token })
	body := getWithAuth(h, "/", token).Body.String()
	for _, want := range []string{"post body", "private plan", "an idea"} {
		if !strings.Contains(body, want) {
			t.Fatalf("content page missing %q", want)
		}
	}
}

func time3(days int) time.Duration { return time.Duration(days) * day }
