package eval_test

import (
	"context"
	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/eval"
	"github.com/canhta/threadwalker/internal/llm"
	"strings"
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

func TestOwnerScenariosRejectMixedOrUnknownInputsAndLiveMode(t *testing.T) {
	for _, scenario := range []string{"controls", "question", "notices", "delivery_failure"} {
		suite := &eval.Suite{Version: "v1", Cases: []eval.Case{{ID: "owner", OwnerScenario: scenario, Messages: []llm.Message{{Role: "user", Content: "synthetic"}}, Expect: eval.Expectation{EndReason: "completed"}}}}
		if _, err := eval.Run(context.Background(), suite, eval.Options{}); err == nil {
			t.Fatal("mixed owner scenario accepted")
		}
	}
	suite := &eval.Suite{Version: "v1", Cases: []eval.Case{{ID: "owner", OwnerScenario: "unknown", Expect: eval.Expectation{EndReason: "completed"}}}}
	if _, err := eval.Run(context.Background(), suite, eval.Options{}); err == nil {
		t.Fatal("unknown owner scenario accepted")
	}
	suite, err := eval.LoadSuite("testdata/owner-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = eval.Run(context.Background(), suite, eval.Options{Mode: "live", Profiles: []eval.Profile{{ID: "synthetic", Config: config.LLMConfig{Model: "synthetic"}}}, CaseIDs: []string{suite.Cases[0].ID}, MaxUSD: 1, MaxSteps: 10, MaxOutputTokens: 100}); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("live owner scenario accepted: %v", err)
	}
}
