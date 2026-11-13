package metrics_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/metrics"
	"github.com/canhta/threadz/internal/store"
)

var pairNow = time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

// labelled stores a measured action with an outcome of the given label and lifts.
func labelled(t *testing.T, st *store.Store, m measured, label string, reach, resonance float64) int64 {
	t.Helper()
	id := addMeasured(t, st, m)
	err := st.InsertOutcome(context.Background(), "a", store.Outcome{ActionID: id, MeasuredAt: m.posted.Add(24 * time.Hour), Views: m.views, ReachLift: reach, ResonanceLift: resonance, Label: label})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func atHour(day, hour int) time.Time { return time.Date(2026, 3, day, hour, 0, 0, 0, time.UTC) }

func TestMatchedPairsPreferSameContextAndRecordFallbackDistance(t *testing.T) {
	st := outcomeStore(t)
	m := func(day, hour int, kind string) measured {
		return measured{posted: atHour(day, hour), kind: kind, views: 500, likes: 5, replies: 5}
	}
	v1 := labelled(t, st, m(2, 9, "post"), store.LabelViral, 1, 1)
	far := labelled(t, st, m(3, 21, "post"), store.LabelFlop, -1, -1)
	same := labelled(t, st, m(4, 10, "post"), store.LabelFlop, -1, -1)
	v2 := labelled(t, st, m(5, 13, "post"), store.LabelViral, 1, 1)
	pairs, err := metrics.MatchedPairs(context.Background(), st, "a", time.UTC, pairNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 {
		t.Fatalf("pairs = %+v", pairs)
	}
	first, second := pairs[0], pairs[1]
	if first.Viral.ActionID != v1 || first.Flop.ActionID != same || !first.Exact || first.Distance != 0 {
		t.Errorf("same-context pair = %+v", first)
	}
	if second.Viral.ActionID != v2 || second.Flop.ActionID != far || second.Exact || second.Distance != 2 {
		t.Errorf("fallback pair = %+v, want bucket distance 2", second)
	}
}

func TestMatchedPairsNeverSpanKindOrAgent(t *testing.T) {
	st := outcomeStore(t)
	if err := st.EnsureAgent(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	labelled(t, st, measured{posted: atHour(2, 9), views: 500, likes: 5, replies: 5}, store.LabelViral, 1, 1)
	labelled(t, st, measured{posted: atHour(3, 9), kind: "reply", views: 500, likes: 5, replies: 5}, store.LabelFlop, -1, -1)
	other := addMeasured(t, st, measured{posted: atHour(4, 9), views: 500, likes: 5, replies: 5})
	for _, q := range []string{
		`UPDATE actions SET agent_id='b' WHERE id=?`, `UPDATE metrics SET agent_id='b' WHERE action_id=?`, `UPDATE metric_jobs SET agent_id='b' WHERE action_id=?`,
	} {
		if _, err := st.DB().Exec(q, other); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.InsertOutcome(context.Background(), "b", store.Outcome{ActionID: other, MeasuredAt: atHour(5, 9), Views: 500, ReachLift: -1, ResonanceLift: -1, Label: store.LabelFlop}); err != nil {
		t.Fatal(err)
	}
	pairs, err := metrics.MatchedPairs(context.Background(), st, "a", time.UTC, pairNow)
	if err != nil || len(pairs) != 0 {
		t.Fatalf("pairs = %+v, err = %v", pairs, err)
	}
}

func TestMatchedPairsIgnoreActionsOlderThanTheWindow(t *testing.T) {
	st := outcomeStore(t)
	old := time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC)
	labelled(t, st, measured{posted: old, views: 500, likes: 5, replies: 5}, store.LabelViral, 1, 1)
	labelled(t, st, measured{posted: atHour(3, 9), views: 500, likes: 5, replies: 5}, store.LabelFlop, -1, -1)
	pairs, err := metrics.MatchedPairs(context.Background(), st, "a", time.UTC, pairNow)
	if err != nil || len(pairs) != 0 {
		t.Fatalf("pairs = %+v, err = %v", pairs, err)
	}
}

// liftsFor are the stored lifts of an action against the expected views and conversation values.
func liftsFor(m measured, expViews, expConv float64) (reach, resonance float64) {
	v := float64(m.views)
	return math.Log1p(v) - expViews, math.Log((float64(m.replies)+metrics.Pseudo)/(v+1)) - expConv
}

func TestAttributionNamesContextWhenTheExpectedValuesDiffer(t *testing.T) {
	st := outcomeStore(t)
	hot := measured{posted: atHour(2, 9), views: 10000, likes: 100, replies: 100}
	cold := measured{posted: atHour(3, 9), views: 100, likes: 1, replies: 1}
	// Both beat their own context by the same margin: the gap is the context's.
	labelled(t, st, hot, store.LabelViral, 0.5, 0.5)
	labelled(t, st, cold, store.LabelFlop, 0.5, 0.5)
	pair := onePair(t, st)
	if a := pair.Attribution(); a.ExplainedBy != "context" || a.Gap <= 0 || math.Abs(a.ContentGap) > 1e-9 || math.Abs(a.ContextGap-a.Gap) > 1e-9 {
		t.Errorf("attribution = %+v", a)
	}
}

func TestAttributionNamesContentWhenTheContextWasTheSame(t *testing.T) {
	st := outcomeStore(t)
	hot := measured{posted: atHour(2, 9), views: 10000, likes: 100, replies: 100}
	cold := measured{posted: atHour(3, 9), views: 100, likes: 1, replies: 1}
	// Both were expected to do the same, so the whole gap is lift.
	rv, cv := liftsFor(hot, 5, -4)
	rf, cf := liftsFor(cold, 5, -4)
	labelled(t, st, hot, store.LabelViral, rv, cv)
	labelled(t, st, cold, store.LabelFlop, rf, cf)
	pair := onePair(t, st)
	if a := pair.Attribution(); a.ExplainedBy != "content" || math.Abs(a.ContextGap) > 1e-9 || math.Abs(a.ContentGap-a.Gap) > 1e-9 {
		t.Errorf("attribution = %+v", a)
	}
}

func onePair(t *testing.T, st *store.Store) metrics.Pair {
	t.Helper()
	pairs, err := metrics.MatchedPairs(context.Background(), st, "a", time.UTC, pairNow)
	if err != nil || len(pairs) != 1 {
		t.Fatalf("pairs = %+v, err = %v", pairs, err)
	}
	return pairs[0]
}

func TestHeatAndParentTierDecideWhetherAMatchIsExact(t *testing.T) {
	cases := []struct {
		name         string
		heat         float64
		parent       int64
		wantExact    bool
		wantDistance int
	}{
		{"one quintile apart is exact", 0.3, 5, true, 1},
		{"two quintiles apart is not", 0.5, 5, false, 2},
		{"parent two tiers apart is not", 0.1, 500, false, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := outcomeStore(t)
			v := labelled(t, st, measured{posted: atHour(2, 9), kind: "reply", views: 500, likes: 5, replies: 5}, store.LabelViral, 1, 1)
			f := labelled(t, st, measured{posted: atHour(3, 9), kind: "reply", views: 500, likes: 5, replies: 5}, store.LabelFlop, -1, -1)
			for id, heat := range map[int64]float64{v: 0.1, f: c.heat} {
				if _, err := st.DB().Exec(`UPDATE actions SET topic_heat=? WHERE id=?`, heat, id); err != nil {
					t.Fatal(err)
				}
			}
			for id, parent := range map[int64]int64{v: 5, f: c.parent} {
				if _, err := st.DB().Exec(`UPDATE actions SET parent_replies=? WHERE id=?`, parent, id); err != nil {
					t.Fatal(err)
				}
			}
			p := onePair(t, st)
			if p.Exact != c.wantExact {
				t.Errorf("exact = %v, want %v", p.Exact, c.wantExact)
			}
			if c.wantExact && p.Distance != c.wantDistance {
				t.Errorf("distance = %d, want %d", p.Distance, c.wantDistance)
			}
		})
	}
}
