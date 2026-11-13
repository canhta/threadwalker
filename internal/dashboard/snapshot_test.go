package dashboard_test

import (
	"context"
	"fmt"
	"time"

	"github.com/canhta/threadz/internal/dashboard"
	"github.com/canhta/threadz/internal/store"
)

func fixedNow() time.Time { return time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC) }

// fakeSource serves prepared snapshots by agent id and records the queries.
type fakeSource struct {
	snaps   map[string]store.DashboardSnapshot
	err     error
	queries []store.DashboardQuery
}

func newSource(alpha store.DashboardSnapshot) *fakeSource {
	return &fakeSource{snaps: map[string]store.DashboardSnapshot{"alpha": alpha}}
}

func (f *fakeSource) DashboardSnapshot(ctx context.Context, q store.DashboardQuery) (store.DashboardSnapshot, error) {
	f.queries = append(f.queries, q)
	if err := ctx.Err(); err != nil {
		return store.DashboardSnapshot{}, err
	}
	if f.err != nil {
		return store.DashboardSnapshot{}, f.err
	}
	return f.snaps[q.AgentID], nil
}

func newHandler(src dashboard.Source, opts ...func(*dashboard.Options)) *dashboard.Handler {
	o := dashboard.Options{Source: src, Now: fixedNow, Agents: []dashboard.Agent{
		{ID: "alpha", Username: "own_alpha", DailyBudgetUSD: 2, Timezone: "Asia/Ho_Chi_Minh"},
		{ID: "beta", Username: "own_beta", DryRun: true, DailyBudgetUSD: 1},
		{ID: "missing", Username: "own_missing", DailyBudgetUSD: 3},
	}}
	for _, fn := range opts {
		fn(&o)
	}
	h, err := dashboard.New(o)
	if err != nil {
		panic(err)
	}
	return h
}

func ago(d time.Duration) time.Time { return fixedNow().Add(-d) }

const day = 24 * time.Hour

func ownLink(code string) string { return "https://threads.net/@own_alpha/post/" + code }

func pub(id int64, kind string, at time.Time, link string) store.DashboardPublication {
	return store.DashboardPublication{ID: id, Kind: kind, PostedAt: at, URL: link}
}

func sample(action int64, at time.Time, views, likes, replies, reposts int64) store.DashboardMetric {
	return store.DashboardMetric{ActionID: action, MeasuredAt: at, Views: views, Likes: likes, Replies: replies, Reposts: reposts}
}

func finishedWake(id int64, kind, reason string, steps int) store.DashboardWake {
	return store.DashboardWake{ID: id, StartedAt: ago(time.Hour), EndedAt: fixedNow(), Ended: true, Kind: kind, Steps: steps, EndReason: reason}
}

func call(id, wake int64, tool, result, blocked string) store.DashboardToolCall {
	return store.DashboardToolCall{ID: id, WakeID: wake, Tool: tool, Result: result, BlockedBy: blocked}
}

// started is the snapshot of an agent that exists and has nothing else.
func started() store.DashboardSnapshot {
	return store.DashboardSnapshot{Started: true, Status: "running"}
}

// measured is one own post with two samples, ten days old.
func measured() store.DashboardSnapshot {
	s := started()
	s.Publications = []store.DashboardPublication{pub(1, "post", ago(10*day), "https://www.threads.net/@own_alpha/post/ABC123")}
	s.Metrics = []store.DashboardMetric{sample(1, ago(7*day), 100, 10, 2, 1), sample(1, ago(2*day), 400, 40, 8, 3)}
	return s
}

func idCode(prefix string, i int) string { return fmt.Sprintf("%s%d", prefix, i) }
