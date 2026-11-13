package metrics

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/canhta/threadz/internal/store"
)

const (
	// PairWindow is how far back matched pairs look.
	PairWindow = 14 * 24 * time.Hour
	heatBins   = 5
)

// Covariates are the context a pair is matched on.
type Covariates struct {
	Kind         string `json:"kind"`
	HourBucket   int    `json:"hour_bucket"`
	HeatQuintile int    `json:"heat_quintile"`
	ParentTier   int    `json:"parent_tier"`
}

// Contender is one viral or flop action with the figures the attribution
// reads: its actual combined log value, the value the outcome model expected
// and the combined lift it stored.
type Contender struct {
	ActionID int64      `json:"action_id"`
	Label    string     `json:"label"`
	Cov      Covariates `json:"covariates"`
	Actual   float64    `json:"actual"`
	Expected float64    `json:"expected"`
	Lift     float64    `json:"lift"`
}

// Pair is a viral and a flop action of one agent matched on context.
type Pair struct {
	Viral, Flop Contender
	// Distance is the summed bucket, heat quintile and parent tier difference;
	// Exact means same bucket and quintile and tier within one.
	Distance int
	Exact    bool
}

// Attribution splits the gap between a pair's actual combined values (log
// scale, mean of the views and resonance axes) into what the context predicted
// and what the outcome model left to the action itself.
type Attribution struct {
	Gap         float64 `json:"gap"`
	ContextGap  float64 `json:"context_gap"`
	ContentGap  float64 `json:"content_gap"`
	ExplainedBy string  `json:"explained_by"`
}

// Attribution reads the split from the stored lifts; nothing is recomputed.
func (p Pair) Attribution() Attribution {
	a := Attribution{Gap: p.Viral.Actual - p.Flop.Actual, ContextGap: p.Viral.Expected - p.Flop.Expected, ContentGap: p.Viral.Lift - p.Flop.Lift}
	a.ExplainedBy = "content"
	if math.Abs(a.ContextGap) > math.Abs(a.ContentGap) {
		a.ExplainedBy = "context"
	}
	return a
}

func covariates(m store.MeasuredAction, tz *time.Location) Covariates {
	c := Covariates{Kind: m.Kind, HourBucket: m.PostedAt.In(tz).Hour() / hourBucketHours}
	if m.TopicHeat != nil {
		c.HeatQuintile = min(heatBins-1, int(*m.TopicHeat*heatBins))
	}
	if m.Kind == "reply" {
		c.ParentTier = int(math.Log10(1 + deref(m.ParentReplies)))
	}
	return c
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// distance is the covariate difference of two actions of one kind; the hour
// bucket wraps around midnight.
func distance(a, b Covariates) (d int, exact bool) {
	hours := 24 / hourBucketHours
	db := absInt(a.HourBucket - b.HourBucket)
	db = min(db, hours-db)
	dh, dt := absInt(a.HeatQuintile-b.HeatQuintile), absInt(a.ParentTier-b.ParentTier)
	return db + dh + dt, db == 0 && dh <= 1 && dt <= 1
}

// MatchedPairs pairs the viral and flop actions of one agent posted within
// PairWindow before now, using the stored outcomes. Pairs are of one kind,
// exact matches first, then the nearest neighbours; an action is in at most one
// pair and ties go to the lower id. The viral actions are matched in posting order.
func MatchedPairs(ctx context.Context, st *store.Store, agentID string, tz *time.Location, now time.Time) ([]Pair, error) {
	all, err := st.MeasuredActions(ctx, agentID)
	if err != nil {
		return nil, err
	}
	outcomes, err := st.OutcomesSince(ctx, agentID, now.Add(-PairWindow-labelWindow))
	if err != nil {
		return nil, err
	}
	byID := map[int64]store.Outcome{}
	for _, o := range outcomes {
		byID[o.ActionID] = o
	}
	var viral, flop []Contender
	for _, m := range all {
		o, ok := byID[m.ActionID]
		if !ok || m.PostedAt.Before(now.Add(-PairWindow)) || m.PostedAt.After(now) {
			continue
		}
		c := contender(m, o, tz)
		switch o.Label {
		case store.LabelViral:
			viral = append(viral, c)
		case store.LabelFlop:
			flop = append(flop, c)
		}
	}
	var pairs []Pair
	used := map[int64]bool{}
	for _, v := range viral {
		best, bestD, bestExact := -1, 0, false
		for i, f := range flop {
			if used[f.ActionID] || f.Cov.Kind != v.Cov.Kind {
				continue
			}
			d, exact := distance(v.Cov, f.Cov)
			if best < 0 || (exact && !bestExact) || (exact == bestExact && d < bestD) {
				best, bestD, bestExact = i, d, exact
			}
		}
		if best >= 0 {
			used[flop[best].ActionID] = true
			pairs = append(pairs, Pair{Viral: v, Flop: flop[best], Distance: bestD, Exact: bestExact})
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		if pairs[i].Exact != pairs[j].Exact {
			return pairs[i].Exact
		}
		return pairs[i].Distance < pairs[j].Distance
	})
	return pairs, nil
}

// contender restates the stored lifts on the log scale: the model's expected
// value is the action's actual value minus the stored lift, per axis.
func contender(m store.MeasuredAction, o store.Outcome, tz *time.Location) Contender {
	a := axes(m)
	resonance := a.conversation
	if o.AmplificationRate != nil && a.hasAmplification {
		resonance = (a.conversation + a.amplification) / 2
	}
	lift := (o.ReachLift + o.ResonanceLift) / 2
	actual := (a.views + resonance) / 2
	return Contender{ActionID: m.ActionID, Label: o.Label, Cov: covariates(m, tz), Actual: actual, Expected: actual - lift, Lift: lift}
}
