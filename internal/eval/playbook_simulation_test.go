package eval

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

// The go test run scales every predeclared configuration down; the full size
// runs as the learning eval suite.
func TestPredeclaredSimulationSuite(t *testing.T) {
	configs := DefaultPlaybookSimulationConfigs()
	if len(configs) != 5 {
		t.Fatalf("fixture has %d configurations", len(configs))
	}
	for _, cfg := range configs {
		cfg.Runs = 100
		got, err := RunPlaybookSimulation(cfg)
		if err != nil {
			t.Fatal(err)
		}
		again, _ := RunPlaybookSimulation(cfg)
		if !reflect.DeepEqual(got, again) {
			t.Fatalf("%s: not reproducible", cfg.Name)
		}
		if strings.HasPrefix(cfg.Name, "null-") && got.FalseEstablishedRunRate > .05 {
			t.Fatalf("%s: false-establish rate %.3f", cfg.Name, got.FalseEstablishedRunRate)
		}
		if cfg.Lift == .5 && (got.EstablishedLifted < 90 || got.MedianActionsToEstablish < 8) {
			t.Fatalf("%s: %+v", cfg.Name, got)
		}
		t.Logf("%s: %+v", cfg.Name, got)
	}
}

func TestSimulationRejectsInvalidConfiguration(t *testing.T) {
	valid := PlaybookSimulationConfig{Name: "v", Seed: 1, Runs: 1, Hypotheses: 1, MaxActions: 1, NoiseScale: 1, MinEffect: .2}
	if _, err := RunPlaybookSimulation(valid); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*PlaybookSimulationConfig){
		func(c *PlaybookSimulationConfig) { c.Name = "" },
		func(c *PlaybookSimulationConfig) { c.Runs = 0 },
		func(c *PlaybookSimulationConfig) { c.Hypotheses = 0 },
		func(c *PlaybookSimulationConfig) { c.MaxActions = 0 },
		func(c *PlaybookSimulationConfig) { c.NoiseScale = 0 },
		func(c *PlaybookSimulationConfig) { c.MinEffect = 0 },
	} {
		c := valid
		change(&c)
		if _, err := RunPlaybookSimulation(c); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
}

func TestSimulationReportRejectsImpossibleMetadata(t *testing.T) {
	valid := PlaybookSimulationReport{Version: PlaybookSimulationVersion, Config: PlaybookSimulationConfig{Name: "synthetic", Runs: 1, Hypotheses: 1}}
	if !validSimulationMetadata(valid) {
		t.Fatal("valid zero-evidence report refused")
	}
	for _, change := range []func(*PlaybookSimulationReport){
		func(r *PlaybookSimulationReport) { r.Version = "unknown" },
		func(r *PlaybookSimulationReport) { r.FalseEstablishedRuns = 2 },
		func(r *PlaybookSimulationReport) { r.FalseEstablishedRunRate = math.NaN() },
		func(r *PlaybookSimulationReport) { r.FalseEstablishedPerHyp = 2 },
		func(r *PlaybookSimulationReport) { r.EstablishedLifted = 2 },
		func(r *PlaybookSimulationReport) { r.MedianActionsToEstablish = -1 },
		func(r *PlaybookSimulationReport) { r.MedianActionsToEstablish, r.P90ActionsToEstablish = 5, 4 },
	} {
		r := valid
		change(&r)
		if validSimulationMetadata(r) {
			t.Fatal("impossible metadata accepted")
		}
	}
}
