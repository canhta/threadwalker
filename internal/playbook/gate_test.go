package playbook_test

import (
	"math"
	"testing"

	"github.com/canhta/threadwalker/internal/playbook"
)

func TestGateNeedsEvidence(t *testing.T) {
	for _, in := range []playbook.GateInput{
		{TestIndex: 1},
		{N: 49, Successes: 49, BaselineN: 200, BaselineSuccesses: 100, TestIndex: 1},
	} {
		got, err := playbook.EvaluateGate(in)
		if err != nil || got.Status != "testing" {
			t.Fatalf("EvaluateGate(%+v)=%+v,%v", in, got, err)
		}
	}
}

func TestGateMakesScheduledDecisions(t *testing.T) {
	cases := []struct {
		name              string
		n, k, base, index int64
		status, reason    string
	}{
		{"large improvement", 50, 50, 100, 1, "established", "positive_evidence"},
		{"large harm", 50, 0, 100, 1, "rejected", "negative_evidence"},
		{"null intermediate", 100, 50, 100, 1, "testing", "insufficient_evidence"},
		{"between looks", 51, 51, 100, 1, "testing", "awaiting_look"},
		{"null horizon", 1600, 800, 100, 1, "rejected", "horizon_inconclusive"},
		{"late ordinal is conservative", 50, 50, 100, 1 << 60, "testing", "insufficient_evidence"},
		{"all successes tie", 50, 50, 200, 1, "testing", "insufficient_evidence"},
		{"all failures tie", 50, 0, 0, 1, "testing", "insufficient_evidence"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := playbook.EvaluateGate(playbook.GateInput{N: tc.n, Successes: tc.k, BaselineN: 200, BaselineSuccesses: tc.base, TestIndex: tc.index})
			if err != nil || got.Status != tc.status || got.Reason != tc.reason {
				t.Fatalf("decision=%+v err=%v; want %s/%s", got, err, tc.status, tc.reason)
			}
		})
	}
}

func TestGateRejectsMalformedEvidence(t *testing.T) {
	valid := playbook.GateInput{N: 50, Successes: 25, BaselineN: 200, BaselineSuccesses: 100, TestIndex: 1}
	cases := []playbook.GateInput{{},
		{TestIndex: -1},
		{N: -1, TestIndex: 1},
		{N: 1601, TestIndex: 1},
		{N: 50, Successes: -1, BaselineN: 200, TestIndex: 1},
		{N: 50, Successes: 51, BaselineN: 200, TestIndex: 1},
		{BaselineN: -1, TestIndex: 1},
		{BaselineN: 200, BaselineSuccesses: -1, TestIndex: 1},
		{BaselineN: 200, BaselineSuccesses: 201, TestIndex: 1},
		{BaselineN: 199, TestIndex: 1},
		{N: 1, TestIndex: 1},
	}
	for _, in := range cases {
		got, err := playbook.EvaluateGate(in)
		if err == nil || got.Status != "" {
			t.Fatalf("invalid input %+v returned %+v,%v", in, got, err)
		}
	}
	if _, err := playbook.EvaluateGate(valid); err != nil {
		t.Fatal(err)
	}
}

func TestOutcomeAndFrozenBaseline(t *testing.T) {
	scores := make([]float64, 300)
	// Calibration median=.2. Reference successes are an independent40/200.
	for i := 0; i < 100; i++ {
		scores[i] = .2
	}
	for i := 100; i < 140; i++ {
		scores[i] = .3
	}
	got, err := playbook.BuildBaseline(scores)
	if err != nil || got.Threshold != .2 || got.N != 200 || got.Successes != 40 {
		t.Fatalf("baseline=%+v,%v", got, err)
	}
	for _, tc := range []struct {
		views, likes, replies, reposts int64
		score                          float64
		success                        bool
	}{
		{100, 10, 5, 5, .2, false},
		{100, 11, 5, 5, .21, true},
		{1, 1, 1, 1, 1, true},
		{100, 0, 0, 0, 0, false},
		{9223372036854775807, 9223372036854775807, 9223372036854775807, 9223372036854775807, 1, true},
	} {
		score, err := playbook.EngagementScore(tc.views, tc.likes, tc.replies, tc.reposts)
		if err != nil || score != tc.score {
			t.Fatalf("score=%v,%v want %v", score, err, tc.score)
		}
		success, err := playbook.OutcomeSuccess(score, got.Threshold)
		if err != nil || success != tc.success {
			t.Fatalf("success=%v,%v want %v", success, err, tc.success)
		}
	}
	// The latest300 are used without altering input; older scores are irrelevant.
	older := append([]float64{1, 1, 1}, scores...)
	latest, err := playbook.BuildBaseline(older)
	if err != nil || latest != got || older[0] != 1 {
		t.Fatalf("rolling baseline=%+v,%v", latest, err)
	}
	// For an even calibration cohort, the middle two values set the median.
	for i := 50; i < 100; i++ {
		scores[i] = .4
	}
	b, err := playbook.BuildBaseline(scores)
	if err != nil || b.Threshold < .299999 || b.Threshold > .300001 {
		t.Fatalf("even median=%+v,%v", b, err)
	}
}

func TestMalformedOutcomeAndBaselineFailSafely(t *testing.T) {
	for _, c := range [][4]int64{{0, 0, 0, 0}, {-1, 0, 0, 0}, {1, -1, 0, 0}, {1, 0, -1, 0}, {1, 0, 0, -1}} {
		if _, err := playbook.EngagementScore(c[0], c[1], c[2], c[3]); err == nil {
			t.Fatalf("accepted counters %v", c)
		}
	}
	for _, score := range []float64{-1, 2, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := playbook.OutcomeSuccess(score, .2); err == nil {
			t.Fatalf("accepted score %v", score)
		}
		if _, err := playbook.OutcomeSuccess(.2, score); err == nil {
			t.Fatalf("accepted threshold %v", score)
		}
		scores := make([]float64, 300)
		scores[120] = score
		if _, err := playbook.BuildBaseline(scores); err == nil {
			t.Fatalf("accepted baseline score %v", score)
		}
	}
	for _, scores := range [][]float64{nil, make([]float64, 299)} {
		if _, err := playbook.BuildBaseline(scores); err == nil {
			t.Fatal("accepted incomplete baseline")
		}
	}
}

func TestFisherExactBoundaryAndLifetimeOrdinal(t *testing.T) {
	// Independently calculated rational hypergeometric tails (integer binomial
	// coefficients): k36 p=.003782876228429782, k37 p=.0016383523048146336,
	// k38 p=.0006545692659498034. First alpha=.0033333; second=.0011111.
	for _, tc := range []struct {
		k, index int64
		status   string
	}{{36, 1, "testing"}, {37, 1, "established"}, {37, 2, "testing"}, {38, 2, "established"}, {14, 1, "testing"}, {13, 1, "rejected"}} {
		got, err := playbook.EvaluateGate(playbook.GateInput{N: 50, Successes: tc.k, BaselineN: 200, BaselineSuccesses: 100, TestIndex: tc.index})
		if err != nil || got.Status != tc.status {
			t.Fatalf("k%d index%d: %+v,%v want %s", tc.k, tc.index, got, err, tc.status)
		}
	}
}
