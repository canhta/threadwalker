package eval_test

import (
	"context"
	"math"
	"testing"

	"github.com/canhta/threadz/internal/eval"
)

func TestCanonicalUsageTimeoutAndOutputFixtures(t *testing.T) {
	suite := loadFixture(t, "default.json")
	opts := fixtureOptions()
	opts.CaseIDs = []string{"missing_usage", "incomplete_usage", "negative_usage", "request_timeout", "native_cache_usage_normalization", "output_bound"}
	report, err := eval.Run(context.Background(), suite, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 12 {
		t.Fatal("missing canonical protocol cases")
	}
	for _, r := range report.Results {
		if !r.Passed {
			t.Fatalf("case=%s protocol=%s reason=%s", r.CaseID, r.Protocol, r.Reason)
		}
		switch r.CaseID {
		case "missing_usage", "incomplete_usage", "negative_usage", "request_timeout":
			if r.TokensIn != 0 || r.TokensOut != 0 || r.CostUSD != 0 || r.Steps != 0 {
				t.Fatal("invalid or unknown usage was invented")
			}
		case "native_cache_usage_normalization", "output_bound":
			if r.TokensIn != 12 || r.TokensOut != 2 || math.Abs(r.CostUSD-0.000016) > 1e-15 || r.Steps != 1 {
				t.Fatal("protocol accounting did not normalize")
			}
		}
	}
}
