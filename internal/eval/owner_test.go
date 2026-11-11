package eval_test

import (
	"context"
	"github.com/canhta/threadwalker/internal/eval"
	"testing"
)

func TestOwnerSuiteExercisesRealServiceAndGuard(t *testing.T) {
	suite, err := eval.LoadSuite("testdata/owner-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	report, err := eval.Run(context.Background(), suite, eval.Options{Seed: 7, Revision: "synthetic-owner-test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 8 {
		t.Fatalf("cases %d", len(report.Results))
	}
	for _, r := range report.Results {
		if !r.Passed {
			t.Errorf("%s/%s: %s %s", r.CaseID, r.Protocol, r.Reason, r.EndReason)
		}
	}
}
