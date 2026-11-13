// Package playbook implements the harness-owned, observational learning rule:
// a normal posterior on the mean lift of each hypothesis, the decisions read
// from it and the Thompson suggestion of what to test next.
package playbook

import (
	"math"
	"math/rand"
)

const (
	MinActions             = 8
	DecisionProbability    = 0.95
	EquivalenceProbability = 0.9
	EquivalenceActions     = 30

	priorActions         = 8.0  // pseudo-actions of the prior on a hypothesis's mean lift
	poolActions          = 20.0 // pseudo-actions of zero added to the pooled mean of the others
	priorVarianceActions = 4.0
	priorVariance        = 1.0
)

const (
	reasonPositive = "positive_evidence"
	reasonNegative = "negative_evidence"
	reasonNoEffect = "no_effect"
)

// Evidence is the lifts of one hypothesis revision's counted actions. Kind is
// the action kind of its first counted action, empty before there is one.
type Evidence struct {
	Kind string
	N    int64
	Mean float64
	// SS is the sum of squared deviations from Mean.
	SS float64
}

// Add counts one more lift.
func (e *Evidence) Add(x float64) {
	e.N++
	d := x - e.Mean
	e.Mean += d / float64(e.N)
	e.SS += d * (x - e.Mean)
}

// NewEvidence summarises lifts.
func NewEvidence(kind string, lifts []float64) Evidence {
	e := Evidence{Kind: kind}
	for _, x := range lifts {
		e.Add(x)
	}
	return e
}

// Posterior is the normal posterior of a hypothesis's mean lift.
type Posterior struct{ Mean, SD float64 }

// Above is P(mean lift > x).
func (p Posterior) Above(x float64) float64 { return 0.5 * math.Erfc((x-p.Mean)/(p.SD*math.Sqrt2)) }

// Posteriors returns one posterior per hypothesis. They share one variance,
// pooled over every hypothesis with a prior worth priorVarianceActions. Each
// mean has a normal prior worth priorActions, centred on the pooled mean of
// the other hypotheses of its kind shrunk toward zero (zero without a kind).
func Posteriors(all []Evidence) []Posterior {
	ss, df := priorVarianceActions*priorVariance, priorVarianceActions
	kindN, kindSum := map[string]float64{}, map[string]float64{}
	for _, e := range all {
		ss += e.SS
		df += math.Max(float64(e.N-1), 0)
		kindN[e.Kind] += float64(e.N)
		kindSum[e.Kind] += float64(e.N) * e.Mean
	}
	variance := ss / df
	out := make([]Posterior, len(all))
	for i, e := range all {
		centre := 0.0
		if e.Kind != "" {
			centre = (kindSum[e.Kind] - float64(e.N)*e.Mean) / (kindN[e.Kind] - float64(e.N) + poolActions)
		}
		k := priorActions + float64(e.N)
		out[i] = Posterior{Mean: (priorActions*centre + float64(e.N)*e.Mean) / k, SD: math.Sqrt(variance / k)}
	}
	return out
}

// Decision holds only a status and a fixed reason code, never editable text.
type Decision struct{ Status, Reason string }

// Decide moves a testing hypothesis with n counted actions to established or
// rejected, or leaves it testing. minEffect is the template's smallest lift
// worth acting on.
func Decide(p Posterior, n int64, minEffect float64) Decision {
	if n >= MinActions && p.Above(minEffect) >= DecisionProbability {
		return Decision{"established", reasonPositive}
	}
	if n >= MinActions && 1-p.Above(0) >= DecisionProbability {
		return Decision{"rejected", reasonNegative}
	}
	if n >= EquivalenceActions && p.Above(-minEffect)-p.Above(minEffect) >= EquivalenceProbability {
		return Decision{"rejected", reasonNoEffect}
	}
	return Decision{Status: "testing"}
}

// Thompson is Thompson sampling: it draws once from each posterior in order and
// returns the index of the largest draw, or -1 without posteriors.
func Thompson(posteriors []Posterior, rng *rand.Rand) int {
	best, top := -1, math.Inf(-1)
	for i, p := range posteriors {
		if draw := p.Mean + p.SD*rng.NormFloat64(); draw > top {
			best, top = i, draw
		}
	}
	return best
}
