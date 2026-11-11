package playbook

import (
	"container/heap"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
)

// SimulationVersion identifies both the synthetic generator and the frozen
// outcome/gate rules. These are synthetic estimates, never live qualification.
const SimulationVersion = "playbook-heavy-tail-v2"

// SimulationConfig predeclares inputs. Seed constructs a private deterministic
// source; no global randomness, wall clock, environment or network is read.
// HorizonDays starts at first tagged action, after the baseline warmup.
// EffectMultiplier and BaselineMultiplier scale engagement rates, not binary
// success probabilities. Their measured probabilities are reported separately.
type SimulationConfig struct {
	Name                  string  `json:"name"`
	Seed                  int64   `json:"seed"`
	Runs                  int     `json:"runs"`
	Hypotheses            int     `json:"hypotheses"`
	HorizonDays           int     `json:"horizon_days"`
	ActionsPerDay         int     `json:"actions_per_day"`
	ControlActionsPerDay  int     `json:"control_actions_per_day"`
	ControlMultiplier     float64 `json:"control_multiplier"`
	ShiftDay              int     `json:"shift_day"`
	ShiftMultiplier       float64 `json:"shift_multiplier"`
	SecondShiftDay        int     `json:"second_shift_day"`
	SecondShiftMultiplier float64 `json:"second_shift_multiplier"`
	EffectMultiplier      float64 `json:"effect_multiplier"`
	BaselineMultiplier    float64 `json:"baseline_multiplier"`
	CensorProbability     float64 `json:"censor_probability"`
	BaselineAvailable     bool    `json:"baseline_available"`
}

// SimulationReport counts a false positive at run level: at least one
// established hypothesis, across every look and hypothesis in that run.
// DetectionDays are calendar days since the first tagged action, including
// the 24h observation lag plus uniform 0..6h lateness; add BaselineWarmupDays
// for cold-start estimates. Zero detection quantiles mean no detection.
// Rejected includes negative evidence and inconclusive maximum-sample stops.
// Undetected includes both rejected and pending hypotheses, never just survivors.
// Paid estimates are zero: this generator makes zero provider/tool calls.
type SimulationReport struct {
	Version                    string           `json:"version"`
	Config                     SimulationConfig `json:"config"`
	EstablishedRuns            int              `json:"established_runs"`
	NullEstablishedRuns        int              `json:"null_established_runs"`
	EstablishedRunFraction     float64          `json:"established_run_fraction"`
	EstablishedHypotheses      int              `json:"established_hypotheses"`
	RejectedHypotheses         int              `json:"rejected_hypotheses"`
	PendingHypotheses          int              `json:"pending_hypotheses"`
	UndetectedHypotheses       int              `json:"undetected_hypotheses"`
	DetectionDays              []int            `json:"detection_days"`
	MedianDetectionDays        int              `json:"median_detection_days"`
	P90DetectionDays           int              `json:"p90_detection_days"`
	MeasuredControlOutcomes    int64            `json:"measured_control_outcomes"`
	UnavailableControlLooks    int64            `json:"unavailable_control_looks"`
	ControlSuccessRate         float64          `json:"control_success_rate"`
	MeasuredTestOutcomes       int64            `json:"measured_test_outcomes"`
	CensoredOutcomes           int64            `json:"censored_outcomes"`
	PendingOutcomes            int64            `json:"pending_outcomes"`
	IgnoredAfterDecision       int64            `json:"ignored_after_decision"`
	BaselineUnavailableActions int64            `json:"baseline_unavailable_actions"`
	SyntheticActions           int64            `json:"synthetic_actions"`
	EstimatedPaidCalls         int64            `json:"estimated_paid_calls"`
	EstimatedPaidUSD           float64          `json:"estimated_paid_usd"`
	BaselineSuccessRate        float64          `json:"baseline_success_rate"`
	TestSuccessRate            float64          `json:"test_success_rate"`
	BaselineWarmupDays         float64          `json:"baseline_warmup_days"`
	BaselineActionsPerDay      int              `json:"baseline_actions_per_day"`
}

//go:embed testdata/gate-v2.json
var defaultSimulationFixture []byte

// DefaultSimulationConfigs returns fresh independent values from the versioned
// fixture. Seeds/scenarios were frozen before measurement, not chosen for their
// observed false-positive rates. Runtime source/DB eligibility is tested by the
// integration suite; this pure generator does not claim to exercise that path.
func DefaultSimulationConfigs() []SimulationConfig {
	var configs []SimulationConfig
	// Embedded source is checked by TestPredeclaredSimulationSuite; malformed
	// source yields an empty suite and cannot silently become a passing null run.
	if err := json.Unmarshal(defaultSimulationFixture, &configs); err != nil {
		return nil
	}
	return configs
}

// RunSimulation uses the actual production score, frozen baseline and gate.
// Views have a Pareto tail (shape1.3, capped1e9) and engagement rates have a
// lognormal tail (median1%, log-SD1.25, capped1). Integer likes=views*rate;
// replies/reposts are zero in this explicit synthetic generator. A 300-action
// prehistory at 30 untagged controls/day creates each run's shared baseline;
// candidate pace may then slow (the sparse scenario). This history fits the
// production 30-day window. Independent conditional samples
// and fixed round-robin assignment are optimistic; labels are observational,
// and temporal correlation or baseline drift can defeat null calibration.
func RunSimulation(cfg SimulationConfig) (SimulationReport, error) {
	report := SimulationReport{Version: SimulationVersion, Config: cfg, DetectionDays: []int{}}
	if cfg.Name == "" || cfg.Runs < 1 || cfg.Runs > 10000 || cfg.Hypotheses < 1 || cfg.Hypotheses > 5 || cfg.HorizonDays < 1 || cfg.HorizonDays > 1095 || cfg.ActionsPerDay < 1 || cfg.ActionsPerDay > 1000 || cfg.ControlActionsPerDay < 0 || cfg.ControlActionsPerDay >= cfg.ActionsPerDay || int64(cfg.Runs)*int64(cfg.HorizonDays)*int64(cfg.ActionsPerDay) > 100000000 || !finiteNonnegative(cfg.EffectMultiplier) || !finiteNonnegative(cfg.BaselineMultiplier) || !finiteNonnegative(cfg.ControlMultiplier) || !validScore(cfg.CensorProbability) || cfg.ShiftDay < 0 || cfg.SecondShiftDay < 0 || !finiteNonnegative(cfg.ShiftMultiplier) || !finiteNonnegative(cfg.SecondShiftMultiplier) || (cfg.SecondShiftDay > 0 && (cfg.ShiftDay == 0 || cfg.SecondShiftDay <= cfg.ShiftDay)) {
		return SimulationReport{}, errors.New("running playbook simulation: invalid configuration")
	}
	rng := rand.New(rand.NewSource(cfg.Seed))
	if cfg.BaselineAvailable {
		report.BaselineActionsPerDay = 30
		report.BaselineWarmupDays = 11.25
	}
	var baselineN, baselineSuccesses, testSuccesses, selectedControlN, selectedControlSuccesses int64
	for run := 0; run < cfg.Runs; run++ {
		baseline := Baseline{}
		if cfg.BaselineAvailable {
			scores := make([]float64, BaselineWindowSize)
			for j := range scores {
				score, err := syntheticScore(rng, cfg.BaselineMultiplier)
				if err != nil {
					return SimulationReport{}, fmt.Errorf("generating simulation baseline: %w", err)
				}
				scores[j] = score
			}
			var err error
			baseline, err = BuildBaseline(scores)
			if err != nil {
				return SimulationReport{}, fmt.Errorf("freezing simulation baseline: %w", err)
			}
			report.SyntheticActions += BaselineWindowSize
			baselineN += baseline.N
			baselineSuccesses += baseline.Successes
		}
		states := make([]GateInput, cfg.Hypotheses)
		decisions := make([]string, cfg.Hypotheses)
		candidateDays := make([]map[int]int64, cfg.Hypotheses)
		for h := range states {
			states[h] = GateInput{BaselineN: baseline.N, BaselineSuccesses: baseline.Successes, TestIndex: int64(h + 1)}
			candidateDays[h] = make(map[int]int64)
		}
		controlDays := make(map[int][]simulationEvent)
		queue := &simulationEvents{}
		established := false
		consume := func(until float64) error {
			for queue.Len() > 0 && (*queue)[0].hour <= until {
				event := heap.Pop(queue).(simulationEvent)
				if event.hypothesis >= 0 && decisions[event.hypothesis] != "" {
					report.IgnoredAfterDecision++
					continue
				}
				if event.stale {
					report.CensoredOutcomes++
					continue
				}
				if event.hypothesis < 0 {
					report.MeasuredControlOutcomes++
					controlDays[event.day] = append(controlDays[event.day], event)
					continue
				}
				success, err := OutcomeSuccess(event.score, baseline.Threshold)
				if err != nil {
					return fmt.Errorf("classifying simulation outcome: %w", err)
				}
				state := &states[event.hypothesis]
				state.N++
				report.MeasuredTestOutcomes++
				candidateDays[event.hypothesis][event.day]++
				if success {
					state.Successes++
					testSuccesses++
				}
				state.ControlN = 0
				state.ControlSuccesses = 0
				if isGateLook(state.N) {
					n, k, err := matchedSimulationControls(candidateDays[event.hypothesis], controlDays, state.N, baseline.Threshold, event.hour)
					if err != nil {
						return err
					}
					state.ControlN = n
					state.ControlSuccesses = k
					selectedControlN += n
					selectedControlSuccesses += k
					if n == 0 {
						report.UnavailableControlLooks++
					}
				}
				decision, err := EvaluateGate(*state)
				if err != nil {
					return fmt.Errorf("evaluating simulation gate: %w", err)
				}
				if decision.Status != "testing" {
					decisions[event.hypothesis] = decision.Status
					if decision.Status == "established" {
						established = true
						report.EstablishedHypotheses++
						report.DetectionDays = append(report.DetectionDays, int(math.Ceil(event.hour/24)))
					} else {
						report.RejectedHypotheses++
					}
				}
			}
			return nil
		}
		slots := cfg.HorizonDays * cfg.ActionsPerDay
		candidateSlot := 0
		for slot := 0; slot < slots; slot++ {
			hour := float64(slot) * 24 / float64(cfg.ActionsPerDay)
			if err := consume(hour); err != nil {
				return SimulationReport{}, err
			}
			active := false
			for _, status := range decisions {
				if status == "" {
					active = true
					break
				}
			}
			if !active {
				break
			}
			dailySlot := slot % cfg.ActionsPerDay
			control := (dailySlot+1)*cfg.ControlActionsPerDay/cfg.ActionsPerDay > dailySlot*cfg.ControlActionsPerDay/cfg.ActionsPerDay
			hypothesis := -1
			multiplier := cfg.ControlMultiplier
			if !control {
				hypothesis = candidateSlot % cfg.Hypotheses
				candidateSlot++
				if decisions[hypothesis] != "" {
					continue
				}
				multiplier = cfg.EffectMultiplier
			}
			report.SyntheticActions++
			if !control && !cfg.BaselineAvailable {
				report.BaselineUnavailableActions++
				continue
			}
			day := slot / cfg.ActionsPerDay
			if cfg.ShiftDay > 0 && day >= cfg.ShiftDay {
				multiplier *= cfg.ShiftMultiplier
			}
			if cfg.SecondShiftDay > 0 && day >= cfg.SecondShiftDay {
				multiplier *= cfg.SecondShiftMultiplier
			}
			score, err := syntheticScore(rng, multiplier)
			if err != nil {
				return SimulationReport{}, fmt.Errorf("generating simulation outcome: %w", err)
			}
			delay := 24 + 6*rng.Float64()
			stale := rng.Float64() < cfg.CensorProbability
			if stale {
				delay = 32
			}
			heap.Push(queue, simulationEvent{hour: hour + delay, score: score, hypothesis: hypothesis, stale: stale, sequence: slot, day: day})
		}
		if err := consume(float64(cfg.HorizonDays) * 24); err != nil {
			return SimulationReport{}, err
		}
		report.PendingOutcomes += int64(queue.Len())
		for _, status := range decisions {
			if status == "" {
				report.PendingHypotheses++
			}
		}
		if established {
			report.EstablishedRuns++
			if cfg.EffectMultiplier == cfg.ControlMultiplier {
				report.NullEstablishedRuns++
			}
		}
	}
	report.EstablishedRunFraction = float64(report.EstablishedRuns) / float64(cfg.Runs)
	report.UndetectedHypotheses = cfg.Runs*cfg.Hypotheses - report.EstablishedHypotheses
	if baselineN > 0 {
		report.BaselineSuccessRate = float64(baselineSuccesses) / float64(baselineN)
	}
	if report.MeasuredTestOutcomes > 0 {
		report.TestSuccessRate = float64(testSuccesses) / float64(report.MeasuredTestOutcomes)
	}
	if selectedControlN > 0 {
		report.ControlSuccessRate = float64(selectedControlSuccesses) / float64(selectedControlN)
	}
	sort.Ints(report.DetectionDays)
	if len(report.DetectionDays) > 0 {
		report.MedianDetectionDays = report.DetectionDays[(len(report.DetectionDays)-1)/2]
		report.P90DetectionDays = report.DetectionDays[int(math.Ceil(float64(len(report.DetectionDays))*.9))-1]
	}
	return report, nil
}

// Matching is an availability rule using publication days and IDs only. Every
// counted candidate day must receive exactly its proportional share of200;
// fractional requirements, missing days or unavailable metrics consume the look
// without evaluation. No outcome-based selection, rounding or dropped days.
func matchedSimulationControls(days map[int]int64, controls map[int][]simulationEvent, n int64, threshold, now float64) (int64, int64, error) {
	var successes int64
	for day, count := range days {
		numerator := count * BaselineReferenceSize
		if numerator%n != 0 {
			return 0, 0, nil
		}
		required := numerator / n
		available := controls[day]
		// Appended in observation-time/sequence order by the event heap, matching
		// production measured_at then local ID selection within each posting day.
		selected := int64(0)
		for _, event := range available {
			if event.hour >= now {
				continue
			}
			success, err := OutcomeSuccess(event.score, threshold)
			if err != nil {
				return 0, 0, fmt.Errorf("classifying matched simulation control: %w", err)
			}
			if success {
				successes++
			}
			selected++
			if selected == required {
				break
			}
		}
		if selected < required {
			return 0, 0, nil
		}
	}
	return BaselineReferenceSize, successes, nil
}

func isGateLook(n int64) bool {
	return n == 50 || n == 100 || n == 200 || n == 400 || n == 800 || n == 1600
}

func finiteNonnegative(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1e6
}

func syntheticScore(rng *rand.Rand, multiplier float64) (float64, error) {
	views := int64(min(1e9, 1000/math.Pow(1-rng.Float64(), 1/1.3)))
	rate := min(1, .01*math.Exp(1.25*rng.NormFloat64())*multiplier)
	return EngagementScore(views, int64(float64(views)*rate), 0, 0)
}

type simulationEvent struct {
	hour, score               float64
	hypothesis, sequence, day int
	stale                     bool
}
type simulationEvents []simulationEvent

func (q simulationEvents) Len() int { return len(q) }
func (q simulationEvents) Less(i, j int) bool {
	if q[i].hour == q[j].hour {
		return q[i].sequence < q[j].sequence
	}
	return q[i].hour < q[j].hour
}
func (q simulationEvents) Swap(i, j int)   { q[i], q[j] = q[j], q[i] }
func (q *simulationEvents) Push(value any) { *q = append(*q, value.(simulationEvent)) }
func (q *simulationEvents) Pop() any {
	old := *q
	n := len(old)
	value := old[n-1]
	*q = old[:n-1]
	return value
}
