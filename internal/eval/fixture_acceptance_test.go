package eval_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/eval"
)

const (
	fixtureSecret   = "fixture-secret-PLANTED-9931"
	fixtureUsername = "fixture_stranger_9931"
	fixtureAPIKey   = "fixture-key-should-not-leak-9931"
)

// fixtureProfiles are deliberately synthetic. Offline eval runs must replace
// their transport; the fake base URL is never contacted.
func fixtureProfiles() []eval.Profile {
	return []eval.Profile{
		{
			ID:         "fixture-openai",
			ProviderID: "synthetic-provider-openai",
			Config: config.LLMConfig{
				Protocol:           "openai",
				Model:              "synthetic-openai-v1",
				BaseURL:            "https://fixture.invalid/v1",
				APIKey:             config.NewSecret(fixtureAPIKey),
				PricePerMTokInUSD:  1,
				PricePerMTokOutUSD: 2,
			},
		},
		{
			ID:         "fixture-anthropic",
			ProviderID: "synthetic-provider-anthropic",
			Config: config.LLMConfig{
				Protocol:           "anthropic",
				Model:              "synthetic-anthropic-v1",
				BaseURL:            "https://fixture.invalid/v1",
				APIKey:             config.NewSecret(fixtureAPIKey),
				PricePerMTokInUSD:  1,
				PricePerMTokOutUSD: 2,
			},
		},
	}
}

func fixtureOptions() eval.Options {
	return eval.Options{
		Seed:     42,
		Repeat:   1,
		Profiles: fixtureProfiles(),
		Mode:     "offline",
		Revision: "fixture-test",
	}
}

func loadFixture(t *testing.T, name string) *eval.Suite {
	t.Helper()
	suite, err := eval.LoadSuite(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("loading %s: %v", name, err)
	}
	return suite
}

func runFixture(t *testing.T, suite *eval.Suite, opts eval.Options) eval.Report {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := eval.Run(ctx, suite, opts)
	if err != nil {
		t.Fatalf("running %s: %v", suite.Version, err)
	}
	return report
}

func withoutWallTiming(report eval.Report) eval.Report {
	for i := range report.Results {
		report.Results[i].DurationMS = 0
	}
	return report
}

func TestDefaultSuitePassesBothProfilesIsRepeatableAndRedacted(t *testing.T) {
	suite := loadFixture(t, "default.json")
	if suite.Version != "v1" || len(suite.Digest) != 64 {
		t.Fatalf("unexpected suite metadata: version=%q digest=%q", suite.Version, suite.Digest)
	}

	required := []string{
		"valid_two_tool_loop",
		"retry_then_success",
		"retry_exhaustion",
		"malformed_response",
		"unknown_and_invalid_arguments_recover",
		"budget_stop",
		"step_stop",
		"cancelled_request",
		"deterministic_quality_rubric",
	}
	cases := make(map[string]bool, len(suite.Cases))
	for _, tc := range suite.Cases {
		cases[tc.ID] = true
	}
	for _, id := range required {
		if !cases[id] {
			t.Errorf("default suite is missing case %q", id)
		}
	}

	first := runFixture(t, suite, fixtureOptions())
	second := runFixture(t, suite, fixtureOptions())
	if first.TimingKind != "wall_clock" {
		t.Errorf("timing kind = %q, want wall_clock", first.TimingKind)
	}
	if first.Mode != "offline" || first.Seed != 42 || first.Repeat != 1 || first.Revision != "fixture-test" {
		t.Errorf("unexpected report header: %+v", first)
	}
	if first.SuiteVersion != "v1" || first.SuiteDigest != suite.Digest {
		t.Errorf("report suite identity = %q/%q", first.SuiteVersion, first.SuiteDigest)
	}

	wantResults := len(suite.Cases) * len(fixtureProfiles())
	if len(first.Results) != wantResults {
		t.Fatalf("results = %d, want %d", len(first.Results), wantResults)
	}
	seen := make(map[string]bool, wantResults)
	for _, result := range first.Results {
		key := fmt.Sprintf("%s/%s/%d", result.CaseID, result.ProfileID, result.Repeat)
		if seen[key] {
			t.Errorf("duplicate result %s", key)
		}
		seen[key] = true
		if result.ProfileID != "fixture-openai" && result.ProfileID != "fixture-anthropic" {
			t.Errorf("unexpected profile %q", result.ProfileID)
		}
		if !result.Passed {
			t.Errorf("case %s failed on %s: reason=%q end_reason=%q", result.CaseID, result.ProfileID, result.Reason, result.EndReason)
		}
		if result.Reason == "" || result.EndReason == "" {
			t.Errorf("case %s has incomplete outcome: %+v", key, result)
		}
		if result.Repeat != 1 {
			t.Errorf("case %s repeat = %d, want one-based 1", key, result.Repeat)
		}
	}
	for _, profile := range fixtureProfiles() {
		for _, id := range required {
			if !seen[fmt.Sprintf("%s/%s/1", id, profile.ID)] {
				t.Errorf("missing result %s for profile %s", id, profile.ID)
			}
		}
	}

	assertNoFixtureLeak(t, first)
	if !reflect.DeepEqual(withoutWallTiming(first), withoutWallTiming(second)) {
		t.Fatal("same-seed reports differ after excluding wall-clock duration")
	}
}

func TestRegressedSuiteFailsWithSafeReasons(t *testing.T) {
	suite := loadFixture(t, "regressed.json")
	report := runFixture(t, suite, fixtureOptions())
	if len(report.Results) == 0 {
		t.Fatal("regressed suite produced no results")
	}
	for _, result := range report.Results {
		if result.Passed {
			t.Errorf("regressed case %s passed on %s", result.CaseID, result.ProfileID)
		}
		if result.Reason == "" || result.Reason == "matched" {
			t.Errorf("regressed case %s lacks a failure reason: %+v", result.CaseID, result)
		}
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"OBSERVED", "EXPECTED_OTHER", "guaranteed", "viral"} {
		if strings.Contains(string(data), raw) {
			t.Errorf("failure report echoed fixture content %q", raw)
		}
	}
}

func TestMalformedFixtureIsRejected(t *testing.T) {
	if _, err := eval.LoadSuite(filepath.Join("testdata", "malformed.json")); err == nil {
		t.Fatal("malformed fixture loaded without an error")
	}
}

func assertNoFixtureLeak(t *testing.T, report eval.Report) {
	t.Helper()
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, planted := range []string{fixtureSecret, fixtureUsername, fixtureAPIKey} {
		if strings.Contains(string(data), planted) {
			t.Errorf("report leaked planted fixture value %q", planted)
		}
	}
	if strings.Contains(report.ConfigFingerprint, fixtureAPIKey) || report.ConfigFingerprint == "" {
		t.Errorf("config fingerprint is missing or leaked a credential")
	}
}
