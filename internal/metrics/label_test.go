package metrics

import (
	"testing"

	"github.com/canhta/threadz/internal/store"
)

func ramp(n int, views float64) []store.Outcome {
	out := make([]store.Outcome, n)
	for i := range out {
		out[i] = store.Outcome{ReachLift: float64(i + 1), ResonanceLift: float64(i + 1), Views: int64(views)}
	}
	return out
}

func TestLabelBoundaries(t *testing.T) {
	// 19 others with lifts 1..19 and the action itself make n=20: viral needs the
	// highest 2, flop the lowest 5.
	history := ramp(19, 100)
	for _, tc := range []struct {
		name             string
		reach, resonance float64
		views            int64
		history          []store.Outcome
		want             string
	}{
		{"viral at the cutoff, ties included", 18, 10, 100, history, store.LabelViral},
		{"just under the reach cutoff", 17.999, 10, 100, history, store.LabelOrdinary},
		{"viral needs views at its own 90th percentile", 100, 10, 99, history, store.LabelOrdinary},
		{"views at the cutoff", 100, 10, 100, history, store.LabelViral},
		{"flop at the cutoff, ties included", 5, 5, 100, history, store.LabelFlop},
		{"just over the reach cutoff", 5.001, 5, 100, history, store.LabelOrdinary},
		{"just over the resonance cutoff", 5, 5.001, 100, history, store.LabelOrdinary},
		{"low reach with high resonance is not a flop", 1, 100, 100, history, store.LabelOrdinary},
		{"too little history", 100, 100, 100, ramp(18, 100), store.LabelOrdinary},
		{"the minimum history labels", 100, 100, 100, ramp(19, 100), store.LabelViral},
	} {
		if got := label(tc.reach, tc.resonance, tc.views, tc.history); got != tc.want {
			t.Errorf("%s: label=%s want %s", tc.name, got, tc.want)
		}
	}
	// n=100: exactly the highest 10 values, 90..99 and the action, are viral.
	if got := label(90, 0, 1000, ramp(99, 1000)); got != store.LabelViral {
		t.Errorf("n=100 at 90: %s", got)
	}
	if got := label(89.5, 0, 1000, ramp(99, 1000)); got != store.LabelOrdinary {
		t.Errorf("n=100 at 89.5: %s", got)
	}
	// n=21: 10% of 21 rounds up to 3 values, so the third highest is viral.
	if got := label(18, 10, 100, ramp(20, 100)); got != store.LabelViral {
		t.Errorf("n=21 third highest: %s", got)
	}
	if got := label(17.9, 10, 100, ramp(20, 100)); got != store.LabelOrdinary {
		t.Errorf("n=21 fourth highest: %s", got)
	}
}
