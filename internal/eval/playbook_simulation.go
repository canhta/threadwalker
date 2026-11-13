package eval

import (
	_ "embed"
	"encoding/json"
	"errors"
	"github.com/canhta/threadz/internal/stdx"
	"math"
	"math/rand"
	"sort"

	"github.com/canhta/threadz/internal/playbook"
)

// PlaybookSimulationVersion identifies the synthetic generator and the learning
// rule it runs. The figures are synthetic estimates, never live qualification.
const PlaybookSimulationVersion = "playbook-bayes"

// PlaybookSimulationConfig predeclares the inputs. Seed builds a private
// deterministic source. Each run tests Hypotheses hypotheses of one kind, the
// first with a true mean lift of Lift and the rest with none. Every step the
// production Thompson rule picks the testing hypothesis that gets the next
// action, whose lift is its true lift plus a Student-t (3 degrees of freedom)
// noise of scale NoiseScale, and the production posterior and decision rules
// read the result. A hypothesis stops at MaxActions actions without a decision.
type PlaybookSimulationConfig struct {
	Name       string  `json:"name"`
	Seed       int64   `json:"seed"`
	Runs       int     `json:"runs"`
	Hypotheses int     `json:"hypotheses"`
	MaxActions int     `json:"max_actions"`
	Lift       float64 `json:"lift"`
	NoiseScale float64 `json:"noise_scale"`
	MinEffect  float64 `json:"min_effect"`
}

// PlaybookSimulationReport counts false establishes per run (at least one
// hypothesis without a true lift established) and per hypothesis, and reports
// the actions the lifted hypothesis needed to be established.
type PlaybookSimulationReport struct {
	Version string                   `json:"version"`
	Config  PlaybookSimulationConfig `json:"config"`
	// FalseEstablishedRuns counts runs with a false establish.
	FalseEstablishedRuns     int     `json:"false_established_runs"`
	FalseEstablishedRunRate  float64 `json:"false_established_run_rate"`
	FalseEstablishedPerHyp   float64 `json:"false_established_hypothesis_rate"`
	EstablishedLifted        int     `json:"established_lifted"`
	RejectedLifted           int     `json:"rejected_lifted"`
	MedianActionsToEstablish int     `json:"median_actions_to_establish"`
	P90ActionsToEstablish    int     `json:"p90_actions_to_establish"`
}

//go:embed testdata/simulation.json
var defaultSimulationFixture []byte

// DefaultPlaybookSimulationConfigs returns fresh values from the versioned
// fixture. The seeds and scenarios were fixed before measurement.
func DefaultPlaybookSimulationConfigs() []PlaybookSimulationConfig {
	var configs []PlaybookSimulationConfig
	if err := json.Unmarshal(defaultSimulationFixture, &configs); err != nil {
		return nil
	}
	return configs
}

// RunPlaybookSimulation runs the production posterior, decision and Thompson code.
func RunPlaybookSimulation(cfg PlaybookSimulationConfig) (PlaybookSimulationReport, error) {
	if cfg.Name == "" || cfg.Runs < 1 || cfg.Runs > 100000 || cfg.Hypotheses < 1 || cfg.Hypotheses > 10 || cfg.MaxActions < 1 || cfg.MaxActions > 10000 || math.IsNaN(cfg.Lift) || math.IsInf(cfg.Lift, 0) || !(cfg.NoiseScale > 0) || !(cfg.MinEffect > 0) || math.IsInf(cfg.NoiseScale, 0) || math.IsInf(cfg.MinEffect, 0) {
		return PlaybookSimulationReport{}, errors.New("running playbook simulation: invalid configuration")
	}
	report := PlaybookSimulationReport{Version: PlaybookSimulationVersion, Config: cfg}
	rng := rand.New(rand.NewSource(cfg.Seed))
	var falseHyps int
	var actionsToEstablish []int
	for run := 0; run < cfg.Runs; run++ {
		evidence := make([]playbook.Evidence, cfg.Hypotheses)
		for h := range evidence {
			evidence[h].Kind = "post"
		}
		decided := make([]bool, cfg.Hypotheses)
		falseRun := false
		for open := cfg.Hypotheses; open > 0; {
			post := playbook.Posteriors(evidence)
			var testing []int
			var drawn []playbook.Posterior
			for h := range post {
				if !decided[h] {
					testing, drawn = append(testing, h), append(drawn, post[h])
				}
			}
			h := testing[playbook.Thompson(drawn, rng)]
			trueLift := 0.0
			if h == 0 {
				trueLift = cfg.Lift
			}
			evidence[h].Add(trueLift + cfg.NoiseScale*studentT3(rng))
			n := evidence[h].N
			d := playbook.Decide(playbook.Posteriors(evidence)[h], n, cfg.MinEffect)
			switch {
			case d.Status == "established" && h == 0 && cfg.Lift > 0:
				report.EstablishedLifted++
				actionsToEstablish = append(actionsToEstablish, int(n))
			case d.Status == "established":
				falseHyps++
				falseRun = true
			case d.Status == "rejected" && h == 0 && cfg.Lift > 0:
				report.RejectedLifted++
			}
			if d.Status != "testing" || n >= int64(cfg.MaxActions) {
				decided[h] = true
				open--
			}
		}
		if falseRun {
			report.FalseEstablishedRuns++
		}
	}
	report.FalseEstablishedRunRate = float64(report.FalseEstablishedRuns) / float64(cfg.Runs)
	report.FalseEstablishedPerHyp = float64(falseHyps) / float64(cfg.Runs*cfg.Hypotheses)
	sort.Ints(actionsToEstablish)
	if n := len(actionsToEstablish); n > 0 {
		report.MedianActionsToEstablish = actionsToEstablish[(n-1)/2]
		report.P90ActionsToEstablish = actionsToEstablish[int(math.Ceil(float64(n)*.9))-1]
	}
	return report, nil
}

// studentT3 draws a Student-t variate with 3 degrees of freedom: heavy tails,
// finite variance.
func studentT3(rng *rand.Rand) float64 {
	a, b, c := rng.NormFloat64(), rng.NormFloat64(), rng.NormFloat64()
	return rng.NormFloat64() / math.Sqrt((a*a+b*b+c*c)/3)
}

// validSimulationMetadata rejects a persisted summary that claims impossible counts.
func validSimulationMetadata(r PlaybookSimulationReport) bool {
	c := r.Config
	rate := func(v float64) bool { return stdx.FiniteNonNegative(v) && v <= 1 }
	return r.Version == PlaybookSimulationVersion && reportText(c.Name) && c.Runs >= 1 && c.Runs <= 100000 && c.Hypotheses >= 1 && c.Hypotheses <= 10 &&
		r.FalseEstablishedRuns >= 0 && r.FalseEstablishedRuns <= c.Runs && rate(r.FalseEstablishedRunRate) && rate(r.FalseEstablishedPerHyp) &&
		r.EstablishedLifted >= 0 && r.RejectedLifted >= 0 && r.EstablishedLifted+r.RejectedLifted <= c.Runs &&
		r.MedianActionsToEstablish >= 0 && r.P90ActionsToEstablish >= r.MedianActionsToEstablish
}
