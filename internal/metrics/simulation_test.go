package metrics_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/metrics"
)

func TestMultiDaySimulationUsesActualCodeAndDatabaseRestart(t *testing.T) {
	var logs bytes.Buffer
	logger, _ := logging.New(&logs, "debug", "json")
	got, err := metrics.RunOfflineSimulation(context.Background(), filepath.Join(t.TempDir(), "simulation.db"), 72, logger)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Synthetic || got.TimingKind != "logical_time" || got.Restarts != 1 || got.ConfirmedActions != 7 || got.Samples != 19 || got.CompletedTargets != 21 || got.SourceReads != 20 || got.ExpectedSamples != 19 {
		t.Fatalf("%+v", got)
	}
	if measured, targets := strings.Count(logs.String(), `"event":"metrics.measured"`), strings.Count(logs.String(), `"event":"metrics.target.completed"`); measured != 19 || targets != 21 {
		t.Fatalf("measurement events=%d target events=%d", measured, targets)
	}
	for _, s := range []string{`"event":"metrics.measured"`, `"reason":"sample_committed"`, `"lateness_ms":90000000`, `"event":"guard.checked"`} {
		if !strings.Contains(logs.String(), s) {
			t.Errorf("missing evidence %s", s)
		}
	}
	for _, private := range []string{"https://fixture.invalid", "SYNTHETIC PRIVATE BODY"} {
		if strings.Contains(logs.String(), private) {
			t.Fatalf("log leak %s", private)
		}
	}
}
