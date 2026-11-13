package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/canhta/threadz/internal/eval"
)

func TestMetricsCLIProducesOfflineLogicalEvidenceWithoutEnvironment(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := filepath.Join(t.TempDir(), "evidence")
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"eval", "metrics", "-soak-hours", "72", "-evidence", dir}, noSecretEnv(t, "read environment"), &out, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "simulation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var evidence eval.MetricSimulationEvidence
	if err = json.Unmarshal(data, &evidence); err != nil || evidence.Samples != 19 || !evidence.Synthetic || evidence.TimingKind != "logical_time" {
		t.Fatalf("%+v err=%v", evidence, err)
	}
	for _, name := range []string{"events.jsonl", "eval.json"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil || info.Size() == 0 {
			t.Fatalf("missing %s %v", name, err)
		}
	}
}
