package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/eval"
)

func TestTwoAgentOfflineSoakCLIExportsScopedEvidence(t *testing.T) {
	t.Chdir(t.TempDir())
	evidence := filepath.Join(t.TempDir(), "evidence")
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"eval", "soak", "-soak-hours", "48", "-soak-agents", "2", "-evidence", evidence}, noSecretEnv(t, "offline read environment"), &out, &stderr)
	if code != 0 {
		t.Fatalf("code %d: %s", code, &stderr)
	}
	raw, err := os.ReadFile(filepath.Join(evidence, "isolation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result agent.IsolationEvidence
	if err = json.Unmarshal(raw, &result); err != nil || !result.Serial || result.LogicalHours != 48 || len(result.Agents) != 2 || result.MaintenanceWakes == 0 {
		t.Fatalf("bad evidence %+v %v", result, err)
	}
	report, err := eval.ReadReport(filepath.Join(evidence, "eval.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range report.Results {
		if !r.Passed {
			t.Fatalf("eval %s: %s", r.CaseID, r.Reason)
		}
	}
}

func TestIsolationSoakCLIRejectsInvalidAgentCountAndModes(t *testing.T) {
	for _, args := range [][]string{{"eval", "soak", "-soak-hours", "48", "-soak-agents", "3"}, {"eval", "soak", "-soak-hours", "47"}, {"eval", "metrics", "-soak-agents", "2"}, {"run", "-soak-hours", "48"}} {
		var out, stderr bytes.Buffer
		if code := run(context.Background(), args, noSecretEnv(t, "invalid mode read environment"), &out, &stderr); code == 0 {
			t.Fatalf("invalid mode accepted %v", args)
		}
	}
}
