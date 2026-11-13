package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/canhta/threadz/internal/store"
)

const (
	// minFitRows exceeds the 18 coefficients, so a fit is never exactly determined.
	minFitRows = 30
	// fitInterval is how often the coefficients are recomputed.
	fitInterval = 24 * time.Hour
	// rollingWindow bounds the rolling median and the same-kind fallback.
	rollingWindow = 14 * 24 * time.Hour
	// labelWindow is the history viral and flop are ranked against.
	labelWindow = 60 * 24 * time.Hour
	// minLabelRows is the fewest outcomes in labelWindow before a label other than ordinary.
	minLabelRows = 20
	// Pseudo is the pseudo-count of the smoothed rates; it keeps log of a zero count finite.
	Pseudo = 0.5

	viralShare      = 0.10
	flopShare       = 0.25
	ridge           = 1.0
	huberK          = 1.345
	hourBucketHours = 4
)

// coefficients are the fitted terms of the three modelled axes, on the log
// scale of views, conversation rate and amplification rate.
type coefficients struct {
	Views         []float64 `json:"views"`
	Conversation  []float64 `json:"conversation"`
	Amplification []float64 `json:"amplification"`
	Applause      []float64 `json:"applause"`
}

type axisValues struct {
	views, conversation, amplification, applause float64
	hasAmplification                             bool
}

// amplified is reposts + quotes + shares; ok is false when quotes or shares were not returned.
func amplified(m store.MeasuredAction) (n int64, ok bool) {
	if m.Quotes == nil || m.Shares == nil {
		return 0, false
	}
	return m.Reposts + *m.Quotes + *m.Shares, true
}

// axes returns the log-scale values the model fits and the lifts compare.
func axes(m store.MeasuredAction) axisValues {
	v := float64(m.Views)
	a := axisValues{
		views:        math.Log1p(v),
		conversation: math.Log((float64(m.Replies) + Pseudo) / (v + 1)),
		applause:     math.Log((float64(m.Likes) + Pseudo) / (v + 1)),
	}
	if n, ok := amplified(m); ok {
		a.hasAmplification = true
		a.amplification = math.Log((float64(n) + Pseudo) / (v + 1))
	}
	return a
}

// features is the context-only design row: intercept, 4-hour bucket and weekday
// in the template timezone, kind, the parent's replies and age for replies,
// topic heat, followers at posting time and the rolling 14-day median views.
func features(m store.MeasuredAction, tz *time.Location, rolling float64) []float64 {
	at := m.PostedAt.In(tz)
	x := make([]float64, 0, 18)
	x = append(x, 1)
	for b := 1; b < 24/hourBucketHours; b++ {
		x = append(x, b2f(at.Hour()/hourBucketHours == b))
	}
	for d := time.Monday; d <= time.Saturday; d++ {
		x = append(x, b2f(at.Weekday() == d))
	}
	reply := m.Kind == "reply"
	x = append(x, b2f(reply), 0, 0)
	if reply {
		x[len(x)-2] = math.Log1p(deref(m.ParentReplies))
		x[len(x)-1] = math.Log1p(deref(m.ParentAgeMins))
	}
	heat := 0.0
	if m.TopicHeat != nil {
		heat = *m.TopicHeat
	}
	return append(x, heat, math.Log1p(deref(m.FollowersAtPost)), math.Log1p(rolling))
}

func deref(p *int64) float64 {
	if p == nil {
		return 0
	}
	return float64(*p)
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// rollingViews is the median 24h views of the actions posted in the 14 days
// before m; 0 when there are none.
func rollingViews(all []store.MeasuredAction, m store.MeasuredAction) float64 {
	var v []float64
	for _, o := range all {
		if o.ActionID != m.ActionID && o.PostedAt.Before(m.PostedAt) && !o.PostedAt.Before(m.PostedAt.Add(-rollingWindow)) {
			v = append(v, float64(o.Views))
		}
	}
	if len(v) == 0 {
		return 0
	}
	return median(v)
}

// huberFit fits a ridge-regularised linear model by Huber-weighted iterative
// least squares; the intercept is not penalised. It returns nil when the normal
// equations are singular.
func huberFit(x [][]float64, y []float64) []float64 {
	p := len(x[0])
	w := make([]float64, len(y))
	for i := range w {
		w[i] = 1
	}
	var beta []float64
	for iter := 0; iter < 20; iter++ {
		a := make([][]float64, p)
		for i := range a {
			a[i] = make([]float64, p+1)
			if i > 0 {
				a[i][i] = ridge
			}
		}
		for r, row := range x {
			for i := 0; i < p; i++ {
				for j := 0; j < p; j++ {
					a[i][j] += w[r] * row[i] * row[j]
				}
				a[i][p] += w[r] * row[i] * y[r]
			}
		}
		if beta = solve(a); beta == nil {
			return nil
		}
		resid := make([]float64, len(y))
		for r, row := range x {
			resid[r] = math.Abs(y[r] - dot(beta, row))
		}
		scale := math.Max(1.4826*median(resid), 1e-6)
		for r := range w {
			w[r] = math.Min(1, huberK*scale/math.Max(resid[r], 1e-12))
		}
	}
	return beta
}

// solve reduces the augmented matrix by Gaussian elimination with partial pivoting.
func solve(a [][]float64) []float64 {
	n := len(a)
	for c := 0; c < n; c++ {
		piv := c
		for r := c + 1; r < n; r++ {
			if math.Abs(a[r][c]) > math.Abs(a[piv][c]) {
				piv = r
			}
		}
		if math.Abs(a[piv][c]) < 1e-12 {
			return nil
		}
		a[c], a[piv] = a[piv], a[c]
		for r := 0; r < n; r++ {
			if r == c {
				continue
			}
			f := a[r][c] / a[c][c]
			for k := c; k <= n; k++ {
				a[r][k] -= f * a[c][k]
			}
		}
	}
	beta := make([]float64, n)
	for i := range beta {
		beta[i] = a[i][n] / a[i][i]
	}
	return beta
}

func dot(a, b []float64) float64 {
	s := 0.0
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// fit fits the three axes on the given scored actions.
func fit(rows []store.MeasuredAction, all []store.MeasuredAction, tz *time.Location) (coefficients, bool) {
	var x, xa [][]float64
	var yv, yc, ya, yl []float64
	for _, m := range rows {
		f := features(m, tz, rollingViews(all, m))
		a := axes(m)
		x, yv, yc, yl = append(x, f), append(yv, a.views), append(yc, a.conversation), append(yl, a.applause)
		if a.hasAmplification {
			xa, ya = append(xa, f), append(ya, a.amplification)
		}
	}
	if len(x) < minFitRows {
		return coefficients{}, false
	}
	model := coefficients{Views: huberFit(x, yv), Conversation: huberFit(x, yc), Applause: huberFit(x, yl)}
	if len(xa) >= minFitRows {
		model.Amplification = huberFit(xa, ya)
	}
	return model, model.Views != nil && model.Conversation != nil && model.Applause != nil
}

// expected returns the expected log-scale value of each axis for m. Axes the
// model lacks, and every axis when model is nil, use the median of the actions
// of the same kind posted in the 14 days before m; ok is false when an axis has
// no expected value.
func expected(model *coefficients, all []store.MeasuredAction, m store.MeasuredAction, tz *time.Location) (e axisValues, ok bool) {
	var fb []axisValues
	for _, o := range all {
		if o.ActionID != m.ActionID && o.Kind == m.Kind && o.PostedAt.Before(m.PostedAt) && !o.PostedAt.Before(m.PostedAt.Add(-rollingWindow)) {
			fb = append(fb, axes(o))
		}
	}
	pick := func(coef []float64, f []float64, get func(axisValues) (float64, bool)) (float64, bool) {
		if coef != nil {
			return dot(coef, f), true
		}
		var v []float64
		for _, a := range fb {
			if x, has := get(a); has {
				v = append(v, x)
			}
		}
		if len(v) == 0 {
			return 0, false
		}
		return median(v), true
	}
	var mv, mc, ma, ml []float64
	if model != nil {
		mv, mc, ma, ml = model.Views, model.Conversation, model.Amplification, model.Applause
	}
	f := features(m, tz, rollingViews(all, m))
	var okViews, okConv, okApplause bool
	e.views, okViews = pick(mv, f, func(a axisValues) (float64, bool) { return a.views, true })
	e.conversation, okConv = pick(mc, f, func(a axisValues) (float64, bool) { return a.conversation, true })
	e.applause, okApplause = pick(ml, f, func(a axisValues) (float64, bool) { return a.applause, true })
	e.amplification, e.hasAmplification = pick(ma, f, func(a axisValues) (float64, bool) { return a.amplification, a.hasAmplification })
	return e, okViews && okConv && okApplause
}

// topCutoff is the smallest value of the highest ceil(frac*n) values.
func topCutoff(v []float64, frac float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)-int(math.Ceil(frac*float64(len(s))))]
}

// bottomCutoff is the largest value of the lowest ceil(frac*n) values.
func bottomCutoff(v []float64, frac float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[int(math.Ceil(frac*float64(len(s))))-1]
}

// label ranks one action's lifts against the lifts of the same agent's last 60
// days, itself included. Viral: reach lift among the highest 10% (ties
// included) and views among the highest 10% of views. Flop: reach lift and
// resonance lift both among the lowest 25%. The share of n values is rounded up.
// Fewer than minLabelRows outcomes label everything ordinary.
func label(reach, resonance float64, views int64, history []store.Outcome) string {
	reaches := []float64{reach}
	resonances := []float64{resonance}
	viewCounts := []float64{float64(views)}
	for _, o := range history {
		reaches = append(reaches, o.ReachLift)
		resonances = append(resonances, o.ResonanceLift)
		viewCounts = append(viewCounts, float64(o.Views))
	}
	if len(reaches) < minLabelRows {
		return store.LabelOrdinary
	}
	switch {
	case reach >= topCutoff(reaches, viralShare) && float64(views) >= topCutoff(viewCounts, viralShare):
		return store.LabelViral
	case reach <= bottomCutoff(reaches, flopShare) && resonance <= bottomCutoff(resonances, flopShare):
		return store.LabelFlop
	}
	return store.LabelOrdinary
}

// followersPer1k divides the follower change between posting and 24h among the
// actions posted in the 24h after m by view share; nil when either count is missing.
func followersPer1k(all []store.MeasuredAction, m store.MeasuredAction) *float64 {
	if m.FollowersAtPost == nil || m.Followers24h == nil {
		return nil
	}
	var total int64
	for _, o := range all {
		if !o.PostedAt.Before(m.PostedAt) && !o.PostedAt.After(m.PostedAt.Add(24*time.Hour)) {
			total += o.Views
		}
	}
	v := float64(*m.Followers24h-*m.FollowersAtPost) * 1000 / float64(total)
	return &v
}

// ComputeOutcomes stores the outcome of every measured action that has none:
// rates, lifts against the expected value, followers per 1k views and the
// label. The model is refit when the last fit is older than fitInterval, on the
// actions that already have outcomes. An action whose expected value is
// unavailable waits for a later call. Outcomes never change once stored.
func ComputeOutcomes(ctx context.Context, st *store.Store, agentID string, tz *time.Location, now time.Time) error {
	all, err := st.MeasuredActions(ctx, agentID)
	if err != nil {
		return err
	}
	stored, hasModel, err := st.LatestOutcomeModel(ctx, agentID)
	if err != nil {
		return err
	}
	var scored []store.MeasuredAction
	pending := 0
	for _, m := range all {
		if m.Scored {
			scored = append(scored, m)
		} else {
			pending++
		}
	}
	if pending == 0 {
		return nil
	}
	if !hasModel || now.Sub(stored.FittedAt) >= fitInterval {
		if fitted, ok := fit(scored, all, tz); ok {
			raw, e := json.Marshal(fitted)
			if e != nil {
				return fmt.Errorf("encoding outcome model: %w", e)
			}
			next := store.OutcomeModel{Version: stored.Version + 1, FittedAt: now, Rows: len(scored), CoefficientsJSON: string(raw)}
			if err = st.SaveOutcomeModel(ctx, agentID, next); err != nil {
				return err
			}
			stored, hasModel = next, true
		}
	}
	var model *coefficients
	version := int64(0)
	if hasModel {
		model = new(coefficients)
		if err = json.Unmarshal([]byte(stored.CoefficientsJSON), model); err != nil {
			return fmt.Errorf("decoding outcome model: %w", err)
		}
		version = stored.Version
	}
	history, err := st.OutcomesSince(ctx, agentID, now.Add(-labelWindow))
	if err != nil {
		return err
	}
	for _, m := range all {
		if m.Scored {
			continue
		}
		e, ok := expected(model, all, m, tz)
		if !ok {
			continue
		}
		a := axes(m)
		conv, reach := a.conversation-e.conversation, a.views-e.views
		resonance := conv
		applause := a.applause - e.applause
		o := store.Outcome{ActionID: m.ActionID, MeasuredAt: m.MeasuredAt, ModelVersion: version, Views: m.Views,
			ConversationRate: float64(m.Replies) / float64(m.Views), ApplauseRate: float64(m.Likes) / float64(m.Views),
			FollowersPer1k: followersPer1k(all, m), ReachLift: reach, ApplauseLift: applause}
		if a.hasAmplification && e.hasAmplification {
			n, _ := amplified(m)
			rate := float64(n) / float64(m.Views)
			o.AmplificationRate = &rate
			resonance = (conv + a.amplification - e.amplification) / 2
		}
		o.ResonanceLift = resonance
		o.Label = label(reach, resonance, m.Views, history)
		if err = st.InsertOutcome(ctx, agentID, o); err != nil {
			return err
		}
		history = append(history, o)
	}
	return nil
}
