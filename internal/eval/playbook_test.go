package eval

import (
	"context"
	"github.com/canhta/threadwalker/internal/playbook"
	"math"
	"testing"
)

func TestPlaybookSuiteRunsActualCodeAndSeededGateSimulation(t *testing.T) {
	suite, err := LoadSuite("testdata/playbook-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), suite, Options{Seed: 10, Revision: "synthetic-playbook-test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) < 6 {
		t.Fatalf("results=%d", len(report.Results))
	}
	for _, r := range report.Results {
		if !r.Passed {
			t.Errorf("%s %s: %s", r.CaseID, r.Protocol, r.Reason)
		}
	}
	path := t.TempDir() + "/report.json"
	if err = WriteReport(path, report); err != nil {
		t.Fatal(err)
	}
	got, err := ReadReport(path)
	if err != nil || len(got.Results) != len(report.Results) {
		t.Fatalf("roundtrip: %v", err)
	}
}

func TestSimulationReportRejectsImpossibleMetadata(t *testing.T) {
	valid := playbook.SimulationReport{Version: playbook.SimulationVersion, Config: playbook.SimulationConfig{Name: "synthetic", Runs: 1, Hypotheses: 1, HorizonDays: 30}, PendingHypotheses: 1, UndetectedHypotheses: 1}
	for _, n := range []int64{50, 100, 200, 400, 800, 1600} {
		valid.LookCounts = append(valid.LookCounts, playbook.SimulationLook{N: n})
	}
	if !validSimulationMetadata(valid) {
		t.Fatal("valid zero-evidence report refused")
	}
	for _, change := range []func(*playbook.SimulationReport){
		func(r *playbook.SimulationReport) { r.Version = "unknown" },
		func(r *playbook.SimulationReport) { r.EstablishedHypotheses = 1 },
		func(r *playbook.SimulationReport) { r.EstimatedPaidCalls = 1 },
		func(r *playbook.SimulationReport) { r.EstablishedRunFraction = math.NaN() },
		func(r *playbook.SimulationReport) { r.ControlSuccessRate = 2 },
		func(r *playbook.SimulationReport) { r.MeasuredControlOutcomes = -1 },
		func(r *playbook.SimulationReport) { r.LookCounts = nil },
		func(r *playbook.SimulationReport) {
			r.LookCounts = []playbook.SimulationLook{{N: 0}, {N: 100}, {N: 200}, {N: 400}, {N: 800}, {N: 1600}}
		},
		func(r *playbook.SimulationReport) { r.UnavailableControlLooks = 1 },
		func(r *playbook.SimulationReport) {
			r.DetectionDays = []int{31}
			r.EstablishedHypotheses = 1
			r.PendingHypotheses = 0
			r.UndetectedHypotheses = 0
		},
	} {
		r := valid
		change(&r)
		if validSimulationMetadata(r) {
			t.Fatal("impossible metadata accepted")
		}
	}
}
