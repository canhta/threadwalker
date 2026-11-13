package playbook

import (
	"math"
	"math/rand"
	"testing"
)

func lifts(n int, x float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = x
	}
	return out
}

func TestPosteriorUpdateAndPooling(t *testing.T) {
	alone := Posteriors([]Evidence{NewEvidence("post", lifts(10, 1))})[0]
	// k = priorActions + 10; mean = 10/18 of the sample mean.
	if want := 10.0 / (priorActions + 10); math.Abs(alone.Mean-want) > 1e-12 {
		t.Fatalf("mean %v want %v", alone.Mean, want)
	}
	more := Posteriors([]Evidence{NewEvidence("post", lifts(40, 1))})[0]
	if more.SD >= alone.SD || more.Mean <= alone.Mean {
		t.Fatalf("more evidence must tighten and move toward the data: %+v %+v", alone, more)
	}
	// Pooling: a hypothesis with one action is pulled toward the strong same-kind
	// hypothesis, and not toward one of another kind or without a kind.
	strong := NewEvidence("post", lifts(60, 1))
	one := func(kind string) Evidence { return NewEvidence(kind, lifts(1, 0)) }
	same := Posteriors([]Evidence{one("post"), strong})[0]
	other := Posteriors([]Evidence{one("reply"), strong})[0]
	none := Posteriors([]Evidence{one(""), strong})[0]
	if !(same.Mean > other.Mean) || other.Mean != none.Mean {
		t.Fatalf("pooling by kind: same %v other %v none %v", same.Mean, other.Mean, none.Mean)
	}
}

func TestDecideThresholds(t *testing.T) {
	const delta = 0.2
	tight := func(mean float64) Posterior { return Posterior{Mean: mean, SD: 0.05} }
	for _, c := range []struct {
		name   string
		p      Posterior
		n      int64
		status string
		reason string
	}{
		{"established", tight(0.5), MinActions, "established", reasonPositive},
		{"too few actions to establish", tight(0.5), MinActions - 1, "testing", ""},
		{"above zero but not above the minimum effect", tight(0.1), MinActions, "testing", ""},
		{"just under 95%", Posterior{Mean: 0.2 + 1.6*0.1, SD: 0.1}, MinActions, "testing", ""},
		{"just over 95%", Posterior{Mean: 0.2 + 1.7*0.1, SD: 0.1}, MinActions, "established", reasonPositive},
		{"negative", tight(-0.5), MinActions, "rejected", reasonNegative},
		{"too few actions to reject", tight(-0.5), MinActions - 1, "testing", ""},
		{"no effect needs 30 actions", tight(0.0), EquivalenceActions - 1, "testing", ""},
		{"no effect", tight(0.0), EquivalenceActions, "rejected", reasonNoEffect},
		{"wide posterior near zero stays testing", Posterior{Mean: 0, SD: 0.3}, EquivalenceActions, "testing", ""},
	} {
		if got := Decide(c.p, c.n, delta); got.Status != c.status || got.Reason != c.reason {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

func TestThompsonIsDeterministicUnderTheInjectedSource(t *testing.T) {
	post := []Posterior{{0, 1}, {0.3, 1}, {-0.2, 1}}
	pick := func(seed int64) int { return Thompson(post, rand.New(rand.NewSource(seed))) }
	if pick(7) != pick(7) {
		t.Fatal("same source, different suggestion")
	}
	seen := map[int]bool{}
	for seed := int64(0); seed < 200; seed++ {
		seen[pick(seed)] = true
	}
	if len(seen) != 3 {
		t.Fatalf("draws never explore every hypothesis: %v", seen)
	}
	sure := []Posterior{{5, 0.01}, {0, 0.01}}
	if Thompson(sure, rand.New(rand.NewSource(1))) != 0 {
		t.Fatal("a clearly better posterior must win")
	}
	if Thompson(nil, rand.New(rand.NewSource(1))) != -1 {
		t.Fatal("no posteriors must suggest nothing")
	}
}
