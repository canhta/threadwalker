package eval

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxReportBytes = 32 << 20

// WriteReport atomically publishes metadata-only JSON with private permissions.
// Callers retain fixtures separately: reports never contain prompts or outputs.
func WriteReport(path string, report Report) error {
	report, err := canonicalReport(report)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return errors.New("encoding eval report failed")
	}
	data = append(data, '\n')
	if len(data) > maxReportBytes {
		return errors.New("eval report exceeds size limit")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".threadwalker-eval-*.tmp")
	if err != nil {
		return errors.New("creating eval report failed")
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return errors.New("writing eval report failed")
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return errors.New("syncing eval report failed")
	}
	if err = f.Close(); err != nil {
		return errors.New("closing eval report failed")
	}
	if err = os.Rename(name, path); err != nil {
		return errors.New("publishing eval report failed")
	}
	return nil
}

// ReadReport accepts only the versioned metadata schema. Diagnostics deliberately
// omit paths, parser excerpts, and untrusted JSON values.
func ReadReport(path string) (Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return Report{}, errors.New("opening eval report failed")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxReportBytes+1))
	if err != nil || len(data) > maxReportBytes {
		return Report{}, errors.New("reading eval report failed or size limit exceeded")
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var r Report
	if err := dec.Decode(&r); err != nil {
		return Report{}, errors.New("decoding eval report failed")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return Report{}, errors.New("eval report must contain one JSON object")
	}
	return canonicalReport(r)
}

func reportText(s string) bool {
	if s == "" || len(s) > 512 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func reportCode(s string) bool {
	if s == "" || len(s) > 96 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

type resultKey struct {
	caseID, profile string
	repeat          int
}

func canonicalReport(r Report) (Report, error) {
	invalid := errors.New("invalid eval report metadata")
	if r.Version != "v1" || !reportText(r.SuiteVersion) || !reportText(r.SuiteDigest) || !reportText(r.ConfigFingerprint) || !reportText(r.InputsFingerprint) || !reportText(r.Revision) || !reportText(r.TimingKind) || (r.Mode != "offline" && r.Mode != "live") || r.Repeat < 1 || len(r.Results) == 0 {
		return Report{}, invalid
	}
	r.Results = append([]CaseResult(nil), r.Results...)
	seen := make(map[resultKey]bool, len(r.Results))
	var tokensIn, tokensOut int64
	var cost, duration float64
	for i, c := range r.Results {
		if !reportText(c.CaseID) || !reportText(c.ProfileID) || !reportText(c.ProviderID) || !reportText(c.Model) || (c.Protocol != "openai" && c.Protocol != "anthropic") || !reportCode(c.Reason) || !reportCode(c.EndReason) || c.Repeat < 1 || c.Repeat > r.Repeat || c.Steps < 0 || c.TokensIn < 0 || c.TokensOut < 0 || c.LogicalHours < 0 || c.MetricSamples < 0 || c.MetricTargets < 0 || !finiteNonnegative(c.CostUSD) || !finiteNonnegative(c.DurationMS) {
			return Report{}, invalid
		}
		if c.Simulation != nil && !validSimulationMetadata(*c.Simulation) {
			return Report{}, invalid
		}
		key := resultKey{c.CaseID, c.ProfileID, c.Repeat}
		if seen[key] {
			return Report{}, errors.New("duplicate eval report case")
		}
		seen[key] = true
		for _, tool := range c.ToolCalls {
			if !reportText(tool) {
				return Report{}, invalid
			}
		}
		if c.TokensIn > math.MaxInt64-tokensIn || c.TokensOut > math.MaxInt64-tokensOut {
			return Report{}, invalid
		}
		tokensIn += c.TokensIn
		tokensOut += c.TokensOut
		cost += c.CostUSD
		duration += c.DurationMS
		if !finiteNonnegative(cost) || !finiteNonnegative(duration) {
			return Report{}, invalid
		}
		r.Results[i].ToolCalls = append([]string{}, c.ToolCalls...)
	}
	sort.Slice(r.Results, func(i, j int) bool {
		a, b := r.Results[i], r.Results[j]
		if a.CaseID != b.CaseID {
			return a.CaseID < b.CaseID
		}
		if a.ProfileID != b.ProfileID {
			return a.ProfileID < b.ProfileID
		}
		return a.Repeat < b.Repeat
	})
	return r, nil
}

// CaseComparison keeps expectation changes independent of resource measurements.
// Positive deltas mean the current run used more than its baseline.
type CaseComparison struct {
	CaseID            string  `json:"case_id"`
	BaselineProfileID string  `json:"baseline_profile_id"`
	CurrentProfileID  string  `json:"current_profile_id"`
	Repeat            int     `json:"repeat"`
	Regressed         bool    `json:"regressed"`
	Improved          bool    `json:"improved"`
	Reason            string  `json:"reason"`
	StepsDelta        int     `json:"steps_delta"`
	TokensInDelta     int64   `json:"tokens_in_delta"`
	TokensOutDelta    int64   `json:"tokens_out_delta"`
	CostUSDDelta      float64 `json:"estimated_cost_usd_delta"`
	DurationMSDelta   float64 `json:"wall_clock_duration_ms_delta"`
}
type Comparison struct {
	HasRegressions bool             `json:"has_regressions"`
	Cases          []CaseComparison `json:"cases"`
}

// Compare aligns identical fixture/case/repeat inputs. A single selected profile
// on each side permits cross-model comparisons; multiple profiles require exact
// profile IDs to avoid an arbitrary pairing. Wall-clock timing never gates a run.
func Compare(baseline, current Report) (Comparison, error) {
	a, err := canonicalReport(baseline)
	if err != nil {
		return Comparison{}, err
	}
	b, err := canonicalReport(current)
	if err != nil {
		return Comparison{}, err
	}
	if a.SuiteVersion != b.SuiteVersion || a.SuiteDigest != b.SuiteDigest || a.InputsFingerprint != b.InputsFingerprint || a.Seed != b.Seed || a.Repeat != b.Repeat || a.Mode != b.Mode {
		return Comparison{}, errors.New("comparison requires identical suite version, digest, inputs fingerprint, mode, seed and repeat inputs")
	}
	profiles := func(r Report) map[string]bool {
		out := make(map[string]bool)
		for _, c := range r.Results {
			out[c.ProfileID] = true
		}
		return out
	}
	ap, bp := profiles(a), profiles(b)
	single := len(ap) == 1 && len(bp) == 1
	if !single {
		if len(ap) != len(bp) {
			return Comparison{}, errors.New("ambiguous profile comparison: select one profile per run or identical profile IDs")
		}
		for p := range ap {
			if !bp[p] {
				return Comparison{}, errors.New("ambiguous profile comparison: select one profile per run or identical profile IDs")
			}
		}
	}
	index := make(map[resultKey]CaseResult, len(a.Results))
	for _, c := range a.Results {
		p := c.ProfileID
		if single {
			p = ""
		}
		index[resultKey{c.CaseID, p, c.Repeat}] = c
	}
	if len(a.Results) != len(b.Results) {
		return Comparison{}, errors.New("comparison requires identical case and repeat inputs")
	}
	result := Comparison{Cases: make([]CaseComparison, 0, len(b.Results))}
	for _, c := range b.Results {
		p := c.ProfileID
		if single {
			p = ""
		}
		base, ok := index[resultKey{c.CaseID, p, c.Repeat}]
		if !ok {
			return Comparison{}, errors.New("comparison requires identical case and repeat inputs")
		}
		delta := CaseComparison{CaseID: c.CaseID, BaselineProfileID: base.ProfileID, CurrentProfileID: c.ProfileID, Repeat: c.Repeat, Regressed: base.Passed && !c.Passed, Improved: !base.Passed && c.Passed, Reason: c.Reason, StepsDelta: c.Steps - base.Steps, TokensInDelta: c.TokensIn - base.TokensIn, TokensOutDelta: c.TokensOut - base.TokensOut, CostUSDDelta: c.CostUSD - base.CostUSD, DurationMSDelta: c.DurationMS - base.DurationMS}
		result.HasRegressions = result.HasRegressions || delta.Regressed
		result.Cases = append(result.Cases, delta)
	}
	return result, nil
}

// Summary contains safe metadata and reproduction IDs, never model output.
func Summary(report Report) string {
	r, err := canonicalReport(report)
	if err != nil {
		return "Invalid eval report.\n"
	}
	var out strings.Builder
	passed := 0
	var in, outTokens int64
	var cost, duration float64
	profiles := make(map[string]CaseResult)
	for _, c := range r.Results {
		if c.Passed {
			passed++
		}
		in += c.TokensIn
		outTokens += c.TokensOut
		cost += c.CostUSD
		duration += c.DurationMS
		profiles[c.ProfileID] = c
	}
	fmt.Fprintf(&out, "Eval suite=%s report=%s mode=%s revision=%s seed=%d repeat=%d: %d/%d passed\n", r.SuiteVersion, r.Version, r.Mode, r.Revision, r.Seed, r.Repeat, passed, len(r.Results))
	fmt.Fprintf(&out, "Suite digest=%s inputs=%s configuration=%s\n", r.SuiteDigest, r.InputsFingerprint, r.ConfigFingerprint)
	ids := make([]string, 0, len(profiles))
	for id := range profiles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		c := profiles[id]
		fmt.Fprintf(&out, "Profile %q provider=%q model=%q protocol=%s\n", id, c.ProviderID, c.Model, c.Protocol)
	}
	fmt.Fprintf(&out, "Measured tokens in=%d out=%d; estimated cost USD=%.9f; wall-clock duration_ms=%.3f (%s; excluded from deterministic comparisons)\n", in, outTokens, cost, duration, r.TimingKind)
	for _, c := range r.Results {
		if !c.Passed {
			fmt.Fprintf(&out, "FAIL case=%q profile=%q repeat=%d reason=%s end_reason=%s\n", c.CaseID, c.ProfileID, c.Repeat, c.Reason, c.EndReason)
		}
	}
	return out.String()
}

func ComparisonSummary(c Comparison) string {
	var out strings.Builder
	regressions := 0
	for _, v := range c.Cases {
		if v.Regressed {
			regressions++
		}
	}
	fmt.Fprintf(&out, "Correctness/safety/deterministic-rubric regressions: %d\n", regressions)
	for _, v := range c.Cases {
		if v.Regressed {
			fmt.Fprintf(&out, "REGRESSION case=%q profile=%q repeat=%d reason=%s\n", v.CaseID, v.CurrentProfileID, v.Repeat, v.Reason)
		}
		fmt.Fprintf(&out, "Metrics case=%q profile=%q repeat=%d: steps_delta=%+d tokens_in_delta=%+d tokens_out_delta=%+d estimated_cost_usd_delta=%+.9f wall-clock_duration_ms_delta=%+.3f\n", v.CaseID, v.CurrentProfileID, v.Repeat, v.StepsDelta, v.TokensInDelta, v.TokensOutDelta, v.CostUSDDelta, v.DurationMSDelta)
	}
	return out.String()
}
