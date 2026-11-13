package eval_test

import (
	"context"
	"testing"

	"github.com/canhta/threadz/internal/eval"
)

func TestBrowserSuiteUsesParserGuardAndRuntimeOffline(t *testing.T) {
	suite, err := eval.LoadSuite("testdata/browser.json")
	if err != nil {
		t.Fatal(err)
	}
	report, err := eval.Run(context.Background(), suite, eval.Options{Seed: 8, Revision: "synthetic-browser-test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 14 {
		t.Fatalf("results=%d", len(report.Results))
	}
	for _, result := range report.Results {
		if !result.Passed {
			t.Errorf("%s/%s: %s (%s)", result.CaseID, result.Protocol, result.Reason, result.EndReason)
		}
	}
}
