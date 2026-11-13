package eval

import (
	"context"
	"testing"
)

func TestPlaybookSuiteRunsActualCode(t *testing.T) {
	suite, err := LoadSuite("testdata/playbook.json")
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), suite, Options{Seed: 10, Revision: "synthetic-playbook-test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) < 3 {
		t.Fatalf("results=%d", len(report.Results))
	}
	for _, r := range report.Results {
		if !r.Passed {
			t.Errorf("%s %s: %s", r.CaseID, r.Protocol, r.Reason)
		}
	}
	path := t.TempDir() + "/report.json"
	if err = WriteReport(path, report); err != nil {
		t.Fatal(err)
	}
	got, err := ReadReport(path)
	if err != nil || len(got.Results) != len(report.Results) {
		t.Fatalf("roundtrip: %v", err)
	}
}
