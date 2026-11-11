package playbook_test

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/canhta/threadwalker/internal/playbook"
)

func TestSimulationIsInjectedSeedDeterministic(t *testing.T) {
	cfg := playbook.SimulationConfig{Name: "strong-effect", Seed: 17, Runs: 2, Hypotheses: 5, HorizonDays: 90, ActionsPerDay: 30, ControlActionsPerDay: 10, ControlMultiplier: 1, EffectMultiplier: 4, BaselineMultiplier: 1, BaselineAvailable: true}
	a, err := playbook.RunSimulation(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := playbook.RunSimulation(cfg)
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatalf("nonreproducible: %v", err)
	}
	if a.EstablishedHypotheses != 10 || len(a.DetectionDays) != 10 || a.UndetectedHypotheses != 0 || a.MedianDetectionDays < 1 {
		t.Fatalf("strong effect report: %+v", a)
	}
	if a.EstimatedPaidCalls != 0 || a.EstimatedPaidUSD != 0 || a.SyntheticActions <= 600 || a.BaselineWarmupDays != 11.25 {
		t.Fatalf("cost/assumptions: %+v", a)
	}
}

func TestPredeclaredSimulationSuite(t *testing.T) {
	configs := playbook.DefaultSimulationConfigs()
	if len(configs) != 26 {
		t.Fatalf("versioned suite has %d configurations, want26", len(configs))
	}
	nullRuns := 0
	nullEstablished := 0
	for _, cfg := range configs {
		t.Run(cfg.Name, func(t *testing.T) {
			got, err := playbook.RunSimulation(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if got.Version != playbook.SimulationVersion {
				t.Fatalf("version=%q", got.Version)
			}
			if got.EstablishedHypotheses+got.RejectedHypotheses+got.PendingHypotheses != cfg.Runs*cfg.Hypotheses {
				t.Fatalf("hypotheses not conserved: %+v", got)
			}
			baselineActions := int64(0)
			if cfg.BaselineAvailable {
				baselineActions = int64(cfg.Runs * 300)
			}
			accounted := baselineActions + got.MeasuredControlOutcomes + got.MeasuredTestOutcomes + got.CensoredOutcomes + got.PendingOutcomes + got.IgnoredAfterDecision + got.BaselineUnavailableActions
			if accounted != got.SyntheticActions {
				t.Fatalf("actions not conserved: got%d accounted%d", got.SyntheticActions, accounted)
			}
			if got.UndetectedHypotheses != got.RejectedHypotheses+got.PendingHypotheses {
				t.Fatal("missed detections omit rejected or pending hypotheses")
			}
			if got.EstimatedPaidCalls != 0 || got.EstimatedPaidUSD != 0 {
				t.Fatal("offline synthetic suite has paid cost")
			}
			if strings.HasPrefix(cfg.Name, "null-") {
				nullRuns += cfg.Runs
				nullEstablished += got.NullEstablishedRuns
				if got.EstablishedRunFraction >= .05 {
					t.Fatalf("null false promotion %.4f >=5%%", got.EstablishedRunFraction)
				}
			}
			if cfg.EffectMultiplier == cfg.ControlMultiplier && got.EstablishedRunFraction >= .05 {
				t.Fatalf("common-shift/null rate %.4f >=5%%", got.EstablishedRunFraction)
			}
			if cfg.Name == "missing-baseline" || cfg.Name == "no-evidence" || cfg.Name == "sparse-2x" {
				if got.EstablishedHypotheses != 0 || got.PendingHypotheses != cfg.Runs*cfg.Hypotheses {
					t.Fatalf("sparse/missing evidence established or rejected: %+v", got)
				}
			}
			if len(got.DetectionDays) > 0 && (got.MedianDetectionDays < 1 || got.P90DetectionDays < got.MedianDetectionDays || got.P90DetectionDays > cfg.HorizonDays) {
				t.Fatalf("bad detection delays: %+v", got)
			}
			t.Logf("seed=%d runs=%d horizon_days=%d rate=%.6f established_runs=%d established=%d rejected=%d pending=%d undetected=%d median_days=%d p90_days=%d baseline_p=%.6f test_p=%.6f actions=%d measured=%d censored=%d awaiting=%d warmup_days=%.2f paid_calls=%d paid_usd=%.2f", cfg.Seed, cfg.Runs, cfg.HorizonDays, got.EstablishedRunFraction, got.EstablishedRuns, got.EstablishedHypotheses, got.RejectedHypotheses, got.PendingHypotheses, got.UndetectedHypotheses, got.MedianDetectionDays, got.P90DetectionDays, got.BaselineSuccessRate, got.TestSuccessRate, got.SyntheticActions, got.MeasuredTestOutcomes, got.CensoredOutcomes, got.PendingOutcomes, got.BaselineWarmupDays, got.EstimatedPaidCalls, got.EstimatedPaidUSD)
			t.Logf("matched_control_p=%.6f measured_control_outcomes=%d unavailable_control_looks=%d", got.ControlSuccessRate, got.MeasuredControlOutcomes, got.UnavailableControlLooks)
			var unavailable int64
			for i, look := range got.LookCounts {
				if look.N != []int64{50, 100, 200, 400, 800, 1600}[i] || look.Unavailable > look.Attempted {
					t.Fatalf("invalid per-look report %+v", look)
				}
				unavailable += look.Unavailable
				t.Logf("look_n=%d attempted=%d unavailable=%d", look.N, look.Attempted, look.Unavailable)
			}
			if len(got.LookCounts) != 6 || unavailable != got.UnavailableControlLooks {
				t.Fatal("per-look counts do not match aggregate")
			}

		})
	}
	if nullRuns != 1500 {
		t.Fatalf("null predeclared runs=%d want1500", nullRuns)
	}
	t.Logf("combined_null_established_runs=%d/%d (%.6f)", nullEstablished, nullRuns, float64(nullEstablished)/float64(nullRuns))
}

func TestSimulationRejectsInvalidConfiguration(t *testing.T) {
	valid := playbook.SimulationConfig{Name: "validation", Seed: 17, Runs: 1, Hypotheses: 1, HorizonDays: 1, ActionsPerDay: 1, EffectMultiplier: 1, BaselineMultiplier: 1, BaselineAvailable: true}
	cases := []playbook.SimulationConfig{{}}
	mutate := func(change func(*playbook.SimulationConfig)) { c := valid; change(&c); cases = append(cases, c) }
	mutate(func(c *playbook.SimulationConfig) { c.Name = "" })
	mutate(func(c *playbook.SimulationConfig) { c.Runs = 0 })
	mutate(func(c *playbook.SimulationConfig) { c.Runs = 10001 })
	mutate(func(c *playbook.SimulationConfig) { c.Hypotheses = 0 })
	mutate(func(c *playbook.SimulationConfig) { c.Hypotheses = 6 })
	mutate(func(c *playbook.SimulationConfig) { c.HorizonDays = 0 })
	mutate(func(c *playbook.SimulationConfig) { c.HorizonDays = 1096 })
	mutate(func(c *playbook.SimulationConfig) { c.ActionsPerDay = 0 })
	mutate(func(c *playbook.SimulationConfig) { c.ActionsPerDay = 1001 })
	mutate(func(c *playbook.SimulationConfig) { c.ControlActionsPerDay = -1 })
	mutate(func(c *playbook.SimulationConfig) { c.ControlActionsPerDay = c.ActionsPerDay })
	mutate(func(c *playbook.SimulationConfig) { c.ShiftDay = -1 })
	mutate(func(c *playbook.SimulationConfig) { c.SecondShiftDay = -1 })
	mutate(func(c *playbook.SimulationConfig) { c.SecondShiftDay = 10 })
	mutate(func(c *playbook.SimulationConfig) { c.ShiftDay = 10; c.SecondShiftDay = 5 })
	mutate(func(c *playbook.SimulationConfig) { c.Runs = 10000; c.HorizonDays = 1095; c.ActionsPerDay = 1000 })
	for _, value := range []float64{-1, 1e7, math.NaN(), math.Inf(1)} {
		mutate(func(c *playbook.SimulationConfig) { c.EffectMultiplier = value })
		mutate(func(c *playbook.SimulationConfig) { c.BaselineMultiplier = value })
		mutate(func(c *playbook.SimulationConfig) { c.ControlMultiplier = value })
		mutate(func(c *playbook.SimulationConfig) { c.ShiftMultiplier = value })
		mutate(func(c *playbook.SimulationConfig) { c.SecondShiftMultiplier = value })
	}
	for _, value := range []float64{-1, 2, math.NaN(), math.Inf(1)} {
		mutate(func(c *playbook.SimulationConfig) { c.CensorProbability = value })
	}
	for _, cfg := range cases {
		if got, err := playbook.RunSimulation(cfg); err == nil || got.Version != "" {
			t.Fatalf("invalid config %+v accepted", cfg)
		}
	}
	got, err := playbook.RunSimulation(valid)
	if err != nil || got.PendingHypotheses != 1 || got.PendingOutcomes != 1 {
		t.Fatalf("short horizon=%+v,%v", got, err)
	}
	zero := valid
	zero.EffectMultiplier = 0
	zero.HorizonDays = 90
	zero.ActionsPerDay = 30
	got, err = playbook.RunSimulation(zero)
	if err != nil || got.RejectedHypotheses != 1 || got.EstablishedHypotheses != 0 {
		t.Fatalf("zero effect=%+v,%v", got, err)
	}
}

func TestSparseSimulationUsesEligibleHistoricalBaseline(t *testing.T) {
	cfg := playbook.SimulationConfig{Name: "slow-after-baseline", Seed: 17, Runs: 1, Hypotheses: 5, HorizonDays: 28, ActionsPerDay: 5, ControlActionsPerDay: 1, ControlMultiplier: 1, EffectMultiplier: 2, BaselineMultiplier: 1, BaselineAvailable: true}
	got, err := playbook.RunSimulation(cfg)
	// Baseline controls arrive at30/day before the candidate pace slows to5/day;
	// 300 controls at5/day would violate production's30-day baseline window.
	if err != nil || got.BaselineWarmupDays != 11.25 {
		t.Fatalf("ineligible baseline history assumed: %.2f days,%v", got.BaselineWarmupDays, err)
	}
}

func TestSimulationReportsEveryLookIncludingUnavailableAndZero(t *testing.T) {
	cfg := playbook.SimulationConfig{Name: "missing-current", Seed: 101, Runs: 1, Hypotheses: 5, HorizonDays: 430, ActionsPerDay: 30, ControlMultiplier: 1, EffectMultiplier: 2, BaselineMultiplier: 1, BaselineAvailable: true}
	got, err := playbook.RunSimulation(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.LookCounts) != 6 {
		t.Fatalf("look count=%d", len(got.LookCounts))
	}
	for i, n := range []int64{50, 100, 200, 400, 800, 1600} {
		look := got.LookCounts[i]
		if look.N != n || look.Attempted != 5 || look.Unavailable != 5 {
			t.Fatalf("look %d=%+v", n, look)
		}
	}
	cfg.CensorProbability = 1
	got, err = playbook.RunSimulation(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, look := range got.LookCounts {
		if look.Attempted != 0 || look.Unavailable != 0 {
			t.Fatalf("missing outcomes show attempted look: %+v", look)
		}
	}
}
