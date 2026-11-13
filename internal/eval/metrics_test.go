package eval_test

import (
	"context"
	"github.com/canhta/threadz/internal/eval"
	"testing"
)

func TestMetricSuiteExecutesProductionPollingOffline(t *testing.T) {
	suite, err := eval.LoadSuite("testdata/metrics.json")
	if err != nil {
		t.Fatal(err)
	}
	r, err := eval.Run(context.Background(), suite, eval.Options{Seed: 19, Revision: "synthetic-metrics-test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Results) != 14 {
		t.Fatalf("results=%d", len(r.Results))
	}
	for _, result := range r.Results {
		if !result.Passed {
			t.Errorf("%s/%s %s %s", result.CaseID, result.Protocol, result.Reason, result.EndReason)
		}
	}
}
