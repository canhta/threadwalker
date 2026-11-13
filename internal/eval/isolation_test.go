package eval

import (
	"context"
	"testing"
)

func TestIsolationVersionedCasesUseRealComponents(t *testing.T) {
	s, err := LoadSuite("testdata/isolation.json")
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), s, Options{Seed: 12, Revision: "synthetic-isolation"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 10 {
		t.Fatalf("missing component cases: %d", len(report.Results))
	}
	for _, r := range report.Results {
		if !r.Passed {
			t.Errorf("%s/%s: %s", r.CaseID, r.Protocol, r.Reason)
		}
		if len(r.Agents) != 2 {
			t.Errorf("agent scope missing: %s", r.CaseID)
		}
	}
}
