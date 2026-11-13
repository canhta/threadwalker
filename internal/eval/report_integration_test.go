package eval_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/canhta/threadz/internal/eval"
)

func TestDefaultRunProducesWritableComparableReport(t *testing.T) {
	suite := loadFixture(t, "default.json")
	report, err := eval.Run(context.Background(), suite, eval.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Revision == "" {
		t.Fatal("default report omits code revision status")
	}
	path := filepath.Join(t.TempDir(), "report.json")
	if err := eval.WriteReport(path, report); err != nil {
		t.Fatal(err)
	}
	loaded, err := eval.ReadReport(path)
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := eval.Compare(report, loaded)
	if err != nil || comparison.HasRegressions {
		t.Fatal("default run report cannot be compared after persistence")
	}
}

func TestRunRejectsInvalidReportMetadataBeforeExecution(t *testing.T) {
	suite := loadFixture(t, "default.json")
	for _, change := range []func(*eval.Options){
		func(o *eval.Options) { o.Revision = "invalid\nrevision" },
		func(o *eval.Options) { o.Profiles[0].ID = "invalid\nprofile" },
		func(o *eval.Options) { o.Profiles[0].ProviderID = "invalid\nprovider" },
		func(o *eval.Options) { o.Profiles[0].Config.Model = "invalid\nmodel" },
	} {
		opts := fixtureOptions()
		change(&opts)
		report, err := eval.Run(context.Background(), suite, opts)
		if err == nil || len(report.Results) != 0 {
			t.Fatal("invalid report metadata reached execution")
		}
	}
}
