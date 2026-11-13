package eval

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func reportFixture() Report {
	return Report{SuiteDigest: strings.Repeat("a", 64), Mode: "offline", Revision: "test-revision", Seed: 7, Repeat: 1, ConfigFingerprint: strings.Repeat("b", 64), InputsFingerprint: strings.Repeat("d", 64), TimingKind: "wall_clock", Results: []CaseResult{
		{CaseID: "two_tools", ProfileID: "candidate", ProviderID: "synthetic", Model: "synthetic-model", Protocol: "openai", Repeat: 1, Passed: true, Reason: "matched", EndReason: "completed", Steps: 3, ToolCalls: []string{"lookup", "save"}, TokensIn: 100, TokensOut: 10, CostUSD: 0.00012, DurationMS: 1},
	}}
}

func TestReportRoundTripAndCanonicalOrdering(t *testing.T) {
	r := reportFixture()
	second := r.Results[0]
	second.CaseID = "aaa"
	r.Results = append(r.Results, second)
	original := append([]CaseResult(nil), r.Results...)
	path := filepath.Join(t.TempDir(), "report.json")
	if err := WriteReport(path, r); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%v", info.Mode())
	}
	got, err := ReadReport(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Results[0].CaseID != "aaa" || !reflect.DeepEqual(original, r.Results) {
		t.Fatal("report order not canonical or input mutated")
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteReport(path, got); err != nil {
		t.Fatal(err)
	}
	next, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(next) {
		t.Fatal("report encoding is unstable")
	}
}

func TestReportRejectsMalformedInputsWithSafeDiagnostics(t *testing.T) {
	for name, body := range map[string]string{"syntax": `{"private":"planted-secret @stranger"`, "unknown": `{"private":"planted-secret @stranger"}`, "trailing": `{} {}`, "empty": ""} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "report.json")
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := ReadReport(path)
			if err == nil {
				t.Fatal("malformed report accepted")
			}
			if strings.Contains(err.Error(), "planted-secret") || strings.Contains(err.Error(), "stranger") {
				t.Fatal("diagnostic leaked payload")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "planted-secret @stranger")
	if _, err := ReadReport(path); err == nil || strings.Contains(err.Error(), "stranger") {
		t.Fatal("unsafe missing report error")
	}
	if err := WriteReport(filepath.Join(path, "out.json"), reportFixture()); err == nil || strings.Contains(err.Error(), "stranger") {
		t.Fatal("unsafe write error")
	}
	if err := WriteReport(t.TempDir(), reportFixture()); err == nil {
		t.Fatal("wrote over directory")
	}
}

func TestReportValidatesBeforeReplacingExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := WriteReport(path, reportFixture()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	for _, change := range []func(*Report){
		func(r *Report) { r.Results[0].Reason = "raw payload @stranger" }, func(r *Report) { r.Results[0].CostUSD = math.NaN() }, func(r *Report) { r.Results[0].DurationMS = -1 }, func(r *Report) { r.Results[0].TokensIn = -1 }, func(r *Report) { r.Results = append(r.Results, r.Results[0]) }, func(r *Report) { r.Repeat = 0 }, func(r *Report) { r.Results = nil }, func(r *Report) { r.Results[0].ProfileID = "bad\nprofile" },
	} {
		r := reportFixture()
		change(&r)
		if err := WriteReport(path, r); err == nil {
			t.Fatal("invalid report accepted")
		}
		after, _ := os.ReadFile(path)
		if string(before) != string(after) {
			t.Fatal("invalid report replaced existing artifact")
		}
	}
}

func TestCompareSeparatesRegressionFromMeasuredChanges(t *testing.T) {
	baseline := reportFixture()
	current := reportFixture()
	current.Revision = "new-revision"
	current.Results[0].Passed = false
	current.Results[0].Reason = "content_mismatch"
	current.Results[0].Steps++
	current.Results[0].TokensIn += 20
	current.Results[0].TokensOut += 2
	current.Results[0].CostUSD += 0.00003
	current.Results[0].DurationMS += 2
	comparison, err := Compare(baseline, current)
	if err != nil {
		t.Fatal(err)
	}
	if !comparison.HasRegressions || len(comparison.Cases) != 1 || !comparison.Cases[0].Regressed {
		t.Fatal("seeded regression missed")
	}
	c := comparison.Cases[0]
	if c.StepsDelta != 1 || c.TokensInDelta != 20 || c.TokensOutDelta != 2 || math.Abs(c.CostUSDDelta-0.00003) > 1e-12 || c.DurationMSDelta != 2 {
		t.Fatalf("wrong metric deltas: %+v", c)
	}
	summary := ComparisonSummary(comparison)
	for _, want := range []string{"regression", "two_tools", "content_mismatch", "tokens", "estimated", "wall-clock"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %s", want)
		}
	}
	current = reportFixture()
	current.Results[0].DurationMS = 99999
	current.Results[0].CostUSD = 100
	comparison, err = Compare(baseline, current)
	if err != nil || comparison.HasRegressions {
		t.Fatal("timing/cost treated as expectation regression")
	}
}

func TestCompareProfileAlignmentAndIdenticalInputs(t *testing.T) {
	baseline := reportFixture()
	current := reportFixture()
	current.Results[0].ProfileID = "other"
	current.Results[0].Model = "other-model"
	if _, err := Compare(baseline, current); err != nil {
		t.Fatal("single-profile cross-model comparison rejected", err)
	}
	for _, change := range []func(*Report){func(r *Report) { r.SuiteDigest = strings.Repeat("c", 64) }, func(r *Report) { r.Seed++ }, func(r *Report) { r.InputsFingerprint = strings.Repeat("e", 64) }, func(r *Report) { r.Results[0].CaseID = "other_case" }, func(r *Report) { r.Mode = "live" }, func(r *Report) { r.Repeat = 2 }} {
		r := reportFixture()
		change(&r)
		if _, err := Compare(baseline, r); err == nil {
			t.Fatal("different fixture inputs accepted")
		}
	}
	add := baseline.Results[0]
	add.ProfileID = "second"
	baseline.Results = append(baseline.Results, add)
	if _, err := Compare(baseline, current); err == nil {
		t.Fatal("ambiguous comparison accepted")
	}
	current = baseline
	current.Results = append([]CaseResult(nil), baseline.Results...)
	current.Results[0].DurationMS = 999
	if _, err := Compare(baseline, current); err != nil {
		t.Fatal("same multi-profile mapping rejected", err)
	}
	current.Results[1].ProfileID = "different"
	if _, err := Compare(baseline, current); err == nil {
		t.Fatal("different multi-profile mappings accepted")
	}
}

func TestReportSizeLimitAndSafeInvalidSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxReportBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadReport(path); err == nil {
		t.Fatal("oversized report accepted")
	}
	r := reportFixture()
	r.Results[0].Reason = "private payload @stranger"
	if strings.Contains(Summary(r), "private") || strings.Contains(Summary(r), "stranger") {
		t.Fatal("invalid summary echoed payload")
	}
}

func TestSummaryHasSafeMetadataAndFailureReproductionIDs(t *testing.T) {
	r := reportFixture()
	r.Results[0].Passed = false
	r.Results[0].Reason = "content_mismatch"
	summary := Summary(r)
	for _, want := range []string{"offline", "test-revision", "seed=7", "two_tools", "candidate", "content_mismatch", "wall-clock", "estimated"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("missing summary metadata %s", want)
		}
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "content\"") {
		t.Fatal("report schema exports raw content")
	}
}
