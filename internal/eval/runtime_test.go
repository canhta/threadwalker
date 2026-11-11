package eval_test

import (
	"context"
	"github.com/canhta/threadwalker/internal/eval"
	"path/filepath"
	"testing"
)

func TestRuntimeSuiteUsesGuardedToolsAndPersistentWakes(t *testing.T) {
	suite, err := eval.LoadSuite("testdata/runtime-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	report, err := eval.Run(context.Background(), suite, eval.Options{Seed: 1, Revision: "synthetic-test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 16 {
		t.Fatalf("cases=%d", len(report.Results))
	}
	for _, c := range report.Results {
		if !c.Passed {
			t.Errorf("%s/%s: %s (%s)", c.CaseID, c.Protocol, c.Reason, c.EndReason)
		}
	}
	p := filepath.Join(t.TempDir(), "report.json")
	if err = eval.WriteReport(p, report); err != nil {
		t.Fatal(err)
	}
	if _, err = eval.ReadReport(p); err != nil {
		t.Fatal(err)
	}
	_, err = eval.Run(context.Background(), suite, eval.Options{Mode: "live"})
	if err == nil {
		t.Fatal("runtime suite allowed live calls")
	}
}
