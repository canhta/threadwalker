package metrics_test

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/metrics"
	"github.com/canhta/threadz/internal/store"
)

type measured struct {
	posted        time.Time
	kind          string
	views, likes  int64
	replies       int64
	reposts       int64
	quotes        any
	shares        any
	followersPost any
	followers24   any
}

func outcomeStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "outcome.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err = st.EnsureAgent(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	return st
}

// addMeasured stores a live action with a completed 24h sample.
func addMeasured(t *testing.T, st *store.Store, m measured) int64 {
	t.Helper()
	if m.kind == "" {
		m.kind = "post"
	}
	posted := m.posted.UTC().Format(time.RFC3339Nano)
	at := m.posted.Add(24 * time.Hour).UTC().Format(time.RFC3339Nano)
	res, err := st.DB().Exec(`INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url,media_id,followers_at_post) VALUES('a',?,'x',0,?,'https://fixture.invalid/p','m',?)`, m.kind, posted, m.followersPost)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if _, err = st.DB().Exec(`INSERT INTO metrics(agent_id,action_id,measured_at,views,likes,replies,reposts,quotes,shares,followers) VALUES('a',?,?,?,?,?,?,?,?,?)`, id, at, m.views, m.likes, m.replies, m.reposts, m.quotes, m.shares, m.followers24); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec(`INSERT INTO metric_jobs(agent_id,action_id,window_hours,due_at,not_before,completed_at) VALUES('a',?,24,?,?,?)`, id, at, at, at); err != nil {
		t.Fatal(err)
	}
	return id
}

func outcomeOf(t *testing.T, st *store.Store, id int64) store.Outcome {
	t.Helper()
	all, err := st.OutcomesSince(context.Background(), "a", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range all {
		if o.ActionID == id {
			return o
		}
	}
	t.Fatalf("action %d has no outcome", id)
	return store.Outcome{}
}

func TestSameOutcomeAtAHotAndAColdHourGetsDifferentLifts(t *testing.T) {
	ctx := context.Background()
	st := outcomeStore(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for day := 0; day < 40; day++ {
		hot := start.AddDate(0, 0, day).Add(9 * time.Hour)
		cold := start.AddDate(0, 0, day).Add(3 * time.Hour)
		addMeasured(t, st, measured{posted: hot, views: int64(1000 + day), likes: 50, replies: 20})
		addMeasured(t, st, measured{posted: cold, views: int64(100 + day), likes: 5, replies: 2})
	}
	first := start.AddDate(0, 0, 41)
	if err := metrics.ComputeOutcomes(ctx, st, "a", time.UTC, first); err != nil {
		t.Fatal(err)
	}
	model, ok, err := st.LatestOutcomeModel(ctx, "a")
	if err != nil || ok {
		t.Fatalf("first pass has no scored rows to fit on: %+v ok=%v err=%v", model, ok, err)
	}

	hot := addMeasured(t, st, measured{posted: start.AddDate(0, 0, 42).Add(9 * time.Hour), views: 500, likes: 25, replies: 10})
	cold := addMeasured(t, st, measured{posted: start.AddDate(0, 0, 42).Add(3 * time.Hour), views: 500, likes: 25, replies: 10})
	if err = metrics.ComputeOutcomes(ctx, st, "a", time.UTC, start.AddDate(0, 0, 44)); err != nil {
		t.Fatal(err)
	}
	if model, ok, err = st.LatestOutcomeModel(ctx, "a"); err != nil || !ok || model.Version != 1 || model.Rows < 30 {
		t.Fatalf("model=%+v ok=%v err=%v", model, ok, err)
	}
	h, c := outcomeOf(t, st, hot), outcomeOf(t, st, cold)
	if h.ModelVersion != 1 || c.ModelVersion != 1 {
		t.Fatalf("versions %d %d", h.ModelVersion, c.ModelVersion)
	}
	if h.ReachLift >= 0 || c.ReachLift <= 0 || c.ReachLift-h.ReachLift < 1 {
		t.Fatalf("equal outcomes: hot-hour reach lift %.2f, cold-hour %.2f; the cold hour must beat its expectation by a wide margin", h.ReachLift, c.ReachLift)
	}
	if math.Abs(h.ConversationRate-c.ConversationRate) > 1e-12 {
		t.Fatal("rates are the raw per-view rates, identical for equal outcomes")
	}
	if h.AmplificationRate != nil {
		t.Fatal("no quotes counter: amplification is unavailable, not zero")
	}
}

func TestFewerRowsThanTheMinimumUseTheSameKindRollingMedian(t *testing.T) {
	ctx := context.Background()
	st := outcomeStore(t)
	start := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	ids := []int64{}
	for i, v := range []int64{100, 200, 400, 300} {
		ids = append(ids, addMeasured(t, st, measured{posted: start.AddDate(0, 0, i), views: v, likes: 5, replies: 1}))
	}
	replyID := addMeasured(t, st, measured{posted: start.AddDate(0, 0, 4), kind: "reply", views: 50, likes: 1})
	laterPost := addMeasured(t, st, measured{posted: start.AddDate(0, 0, 4), views: 900, likes: 5, replies: 1})
	if err := metrics.ComputeOutcomes(ctx, st, "a", time.UTC, start.AddDate(0, 0, 6)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.LatestOutcomeModel(ctx, "a"); ok {
		t.Fatal("fitted below the minimum rows")
	}
	all, err := st.OutcomesSince(ctx, "a", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	have := map[int64]store.Outcome{}
	for _, o := range all {
		have[o.ActionID] = o
		if o.ModelVersion != 0 {
			t.Fatalf("action %d scored with model %d, want the fallback", o.ActionID, o.ModelVersion)
		}
	}
	if _, ok := have[ids[0]]; ok {
		t.Fatal("the first action has nothing to compare with and must wait")
	}
	if _, ok := have[replyID]; ok {
		t.Fatal("a reply has no earlier reply to compare with and must wait")
	}
	// Median of the earlier posts' log(1+views) is the middle of 100,200,400,300.
	want := math.Log1p(900) - (math.Log1p(200)+math.Log1p(300))/2
	if got := have[laterPost].ReachLift; math.Abs(got-want) > 1e-9 {
		t.Fatalf("reach lift %.6f, want %.6f", got, want)
	}
}

func TestMissingCountersAreUnavailableNeverZero(t *testing.T) {
	ctx := context.Background()
	st := outcomeStore(t)
	start := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	addMeasured(t, st, measured{posted: start, views: 100, likes: 5, replies: 2, quotes: 1, shares: 0})
	withQuotes := addMeasured(t, st, measured{posted: start.AddDate(0, 0, 1), views: 100, likes: 5, replies: 2, reposts: 3, quotes: 1, shares: 2, followersPost: 10, followers24: 14})
	withoutQuotes := addMeasured(t, st, measured{posted: start.AddDate(0, 0, 2), views: 100, likes: 5, replies: 2, reposts: 3, quotes: 1})
	if err := metrics.ComputeOutcomes(ctx, st, "a", time.UTC, start.AddDate(0, 0, 4)); err != nil {
		t.Fatal(err)
	}
	a, b := outcomeOf(t, st, withQuotes), outcomeOf(t, st, withoutQuotes)
	if a.AmplificationRate == nil || math.Abs(*a.AmplificationRate-0.06) > 1e-12 {
		t.Fatalf("amplification rate=%v, want (reposts+quotes+shares)/views", a.AmplificationRate)
	}
	if b.AmplificationRate != nil || b.FollowersPer1k != nil {
		t.Fatalf("absent quotes or followers must stay nil: %+v", b)
	}
	recent, err := st.RecentOutcomes(ctx, "a", 5)
	if err != nil || len(recent) != 2 || recent[0].ApplauseLift == nil {
		t.Fatalf("recent outcomes %+v err=%v: applause lift is stored and shown", recent, err)
	}
	// 4 followers gained over the views of the actions posted in its 24h window (itself and the next).
	if a.FollowersPer1k == nil || math.Abs(*a.FollowersPer1k-4*1000.0/200) > 1e-9 {
		t.Fatalf("followers per 1k=%v", a.FollowersPer1k)
	}
}

func TestOutcomesAreStoredOnceAndNeverRecomputed(t *testing.T) {
	ctx := context.Background()
	st := outcomeStore(t)
	start := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	addMeasured(t, st, measured{posted: start, views: 100, likes: 5, replies: 2})
	id := addMeasured(t, st, measured{posted: start.AddDate(0, 0, 1), views: 300, likes: 5, replies: 2})
	now := start.AddDate(0, 0, 3)
	if err := metrics.ComputeOutcomes(ctx, st, "a", time.UTC, now); err != nil {
		t.Fatal(err)
	}
	before := outcomeOf(t, st, id)
	addMeasured(t, st, measured{posted: start.AddDate(0, 0, 2), views: 90000, likes: 5, replies: 2})
	if err := metrics.ComputeOutcomes(ctx, st, "a", time.UTC, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if after := outcomeOf(t, st, id); after != before {
		t.Fatalf("outcome changed: %+v -> %+v", before, after)
	}
}
