// Package playbook implements the harness-owned, observational learning rule.
package playbook

import (
	"errors"
	"math"
	"sort"
)

const (
	BaselineCalibrationSize = 100
	BaselineReferenceSize   = 200
	BaselineWindowSize      = BaselineCalibrationSize + BaselineReferenceSize
	MinGateSamples          = 50
	MaxGateSamples          = 1600
)

// GateInput contains code-owned counts for one immutable hypothesis revision.
// TestIndex is its persistent, one-based ordinal across the agent's lifetime,
// including amendments. It must never be recycled when an entry is decided.
type GateInput struct {
	N, Successes, BaselineN, BaselineSuccesses, TestIndex int64
}

// GateDecision contains only static status and reason codes, never editable text.
type GateDecision struct{ Status, Reason string }

// EvaluateGate is pure: persistence, eligibility and revision binding belong to
// the caller. A terminal decision must be persisted and never evaluated again.
func EvaluateGate(in GateInput) (GateDecision, error) {
	if in.TestIndex < 1 || in.N < 0 || in.N > MaxGateSamples || in.Successes < 0 || in.Successes > in.N || in.BaselineN < 0 || in.BaselineSuccesses < 0 || in.BaselineSuccesses > in.BaselineN || (in.BaselineN != 0 && in.BaselineN != BaselineReferenceSize) || (in.BaselineN == 0 && in.N != 0) {
		return GateDecision{}, errors.New("evaluating playbook gate: invalid evidence")
	}
	if in.BaselineN == 0 {
		return GateDecision{Status: "testing", Reason: "baseline_unavailable"}, nil
	}
	if in.N < MinGateSamples {
		return GateDecision{Status: "testing", Reason: "minimum_sample"}, nil
	}
	look := false
	for _, n := range [...]int64{50, 100, 200, 400, 800, 1600} {
		if in.N == n {
			look = true
			break
		}
	}
	if !look {
		return GateDecision{Status: "testing", Reason: "awaiting_look"}, nil
	}
	// Sum_i 1/(i(i+1))=1; six looks spend at most 4% across all
	// lifetime hypotheses/revisions, regardless of overlap between hypotheses.
	// Conditional on the frozen threshold, Fisher requires independent,
	// stationary Bernoulli reference and test samples. Adaptive labels, drift
	// and repeated same-day correlation can violate that assumption.
	i := float64(in.TestIndex)
	alpha := math.Exp(math.Log(0.04) - math.Log(i) - math.Log(i+1) - math.Log(6))
	positive, negative := fisherTails(in)
	// Leave a small numerical margin on the conservative side.
	if positive <= alpha*(1-1e-10) {
		return GateDecision{Status: "established", Reason: "positive_evidence"}, nil
	}
	if negative <= alpha*(1-1e-10) {
		return GateDecision{Status: "rejected", Reason: "negative_evidence"}, nil
	}
	if in.N == MaxGateSamples {
		return GateDecision{Status: "rejected", Reason: "horizon_inconclusive"}, nil
	}
	return GateDecision{Status: "testing", Reason: "insufficient_evidence"}, nil
}

// Conditioning on total successes makes the number in the reference cohort
// hypergeometric. A small reference count is evidence for the candidate.
func fisherTails(in GateInput) (positive, negative float64) {
	total := in.Successes + in.BaselineSuccesses
	population := in.N + in.BaselineN
	lo := max(int64(0), in.BaselineN-(population-total))
	hi := min(in.BaselineN, total)
	denominator := logChoose(population, in.BaselineN)
	for x := lo; x <= hi; x++ {
		p := math.Exp(logChoose(total, x) + logChoose(population-total, in.BaselineN-x) - denominator)
		if x <= in.BaselineSuccesses {
			positive += p
		}
		if x >= in.BaselineSuccesses {
			negative += p
		}
	}
	return min(1, positive), min(1, negative)
}

// A bounded table keeps many seeded simulations fast and avoids dependencies.
var logFactorials = func() [MaxGateSamples + BaselineReferenceSize + 1]float64 {
	var result [MaxGateSamples + BaselineReferenceSize + 1]float64
	for i := 1; i < len(result); i++ {
		result[i] = result[i-1] + math.Log(float64(i))
	}
	return result
}()

func logChoose(n, k int64) float64 { return logFactorials[n] - logFactorials[k] - logFactorials[n-k] }

// EngagementScore normalizes by views and caps at one before binarization.
// Zero views is unavailable evidence, never a fabricated zero-score failure.
// Converting before addition avoids signed counter overflow.
func EngagementScore(views, likes, replies, reposts int64) (float64, error) {
	if views <= 0 || likes < 0 || replies < 0 || reposts < 0 {
		return 0, errors.New("scoring playbook outcome: invalid counters")
	}
	return min(1, (float64(likes)+float64(replies)+float64(reposts))/float64(views)), nil
}

// OutcomeSuccess uses a strict comparison; equality is a measured failure.
func OutcomeSuccess(score, threshold float64) (bool, error) {
	if !validScore(score) || !validScore(threshold) {
		return false, errors.New("classifying playbook outcome: invalid score")
	}
	return score > threshold, nil
}

// Baseline is frozen with a hypothesis revision before its first eligible
// tagged action. N and Successes come from the disjoint reference cohort.
type Baseline struct {
	Threshold    float64
	N, Successes int64
}

// BuildBaseline takes chronological eligible same-kind untagged controls. The
// caller enforces ownership, 30-day history, actual observation age 24..30h,
// and measurement strictly before action reservation. The latest 300 are
// split into 100 calibration scores and 200 independent reference scores.
// The revision's candidate outcomes must never enter either cohort.
func BuildBaseline(scores []float64) (Baseline, error) {
	if len(scores) < BaselineWindowSize {
		return Baseline{}, errors.New("building playbook baseline: insufficient history")
	}
	scores = scores[len(scores)-BaselineWindowSize:]
	for _, score := range scores {
		if !validScore(score) {
			return Baseline{}, errors.New("building playbook baseline: invalid score")
		}
	}
	calibration := append([]float64(nil), scores[:BaselineCalibrationSize]...)
	sort.Float64s(calibration)
	b := Baseline{Threshold: (calibration[49] + calibration[50]) / 2, N: BaselineReferenceSize}
	for _, score := range scores[BaselineCalibrationSize:] {
		if score > b.Threshold {
			b.Successes++
		}
	}
	return b, nil
}

func validScore(score float64) bool {
	return !math.IsNaN(score) && !math.IsInf(score, 0) && score >= 0 && score <= 1
}
