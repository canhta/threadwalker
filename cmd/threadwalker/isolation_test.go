package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/canhta/threadwalker/internal/agent"
	"github.com/canhta/threadwalker/internal/eval"
)

func TestTwoAgentOfflineSoakCLIExportsScopedEvidence(t *testing.T) {
	// Suite paths are intentionally resolved from the documented repository root.
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	evidence := filepath.Join(t.TempDir(), "evidence")
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"run", "-offline", "-soak-hours", "48", "-soak-agents", "2", "-evidence", evidence}, func(string) string { t.Fatal("offline read environment"); return "" }, &out, &stderr)
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
	for _, args := range [][]string{{"run", "-offline", "-soak-agents", "2"}, {"run", "-offline", "-soak-hours", "48", "-soak-agents", "3"}, {"run", "-soak-hours", "48", "-soak-agents", "2"}, {"run", "-offline", "-once", "-soak-hours", "48", "-soak-agents", "2"}} {
		var out, stderr bytes.Buffer
		if code := run(context.Background(), args, func(string) string { t.Fatal("invalid mode read environment"); return "" }, &out, &stderr); code == 0 {
			t.Fatalf("invalid mode accepted %v", args)
		}
	}
}
