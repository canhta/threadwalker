package eval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/quality"
)

const qualitySuite = "testdata/quality.json"

func runQuality(t *testing.T, suite *Suite) Report {
	t.Helper()
	report, err := Run(context.Background(), suite, Options{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestQualitySuiteScoresEveryRecordedDraftAsExpected(t *testing.T) {
	suite, err := LoadSuite(qualitySuite)
	if err != nil {
		t.Fatal(err)
	}
	report := runQuality(t, suite)
	if len(report.Results) != 2*len(suite.Cases) {
		t.Fatalf("results=%d", len(report.Results))
	}
	for _, r := range report.Results {
		if !r.Passed {
			t.Errorf("case %s/%s: reason=%s", r.CaseID, r.ProfileID, r.Reason)
		}
	}
	rates := map[string]qualityRate{}
	for _, rate := range qualityRates(report.Results) {
		rates[rate.id] = rate
	}
	for id, failing := range map[string]int{"max_runes": 1, "max_lines": 1, "style_avoid": 1, "repeat_image": 1, "ungrounded_number": 1, "style_rule_2": 1, "bad_example": 2} {
		if got := rates[id]; got.total != 22 || got.passed != 22-2*failing {
			t.Errorf("%s = %d/%d, want %d/22", id, got.passed, got.total, 22-2*failing)
		}
	}
	if !strings.Contains(Summary(report), "Quality ungrounded_number: 20/22 passed") {
		t.Error("summary lacks per-rule pass rates")
	}
}

func TestQualityWakeIsTheRealSystemPrompt(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("..", "agent", "testdata", "viral.system.golden"))
	if err != nil {
		t.Fatal(err)
	}
	suite, _ := LoadSuite(qualitySuite)
	_, messages, err := qualityWake(context.Background(), suite.Cases[0].Quality, time.Unix(0, 0).UTC().Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if messages[0].Content != string(golden) {
		t.Fatal("quality wake prompt differs from the agent's golden system prompt")
	}
}

func TestQualityWakeErrors(t *testing.T) {
	suite, _ := LoadSuite(qualitySuite)
	q := *suite.Cases[0].Quality
	q.TemplateID = "missing"
	if _, _, err := qualityWake(context.Background(), &q, time.Unix(0, 0).UTC()); err == nil {
		t.Error("missing template accepted")
	}
	q = *suite.Cases[0].Quality
	q.resolvedDir = t.TempDir()
	if _, _, err := qualityWake(context.Background(), &q, time.Unix(0, 0).UTC()); err == nil {
		t.Error("empty template dir accepted")
	}
}

func TestQualityRejectsUnparsableJudgeVerdict(t *testing.T) {
	suite, _ := LoadSuite(qualitySuite)
	c := suite.Cases[0]
	c.Quality.Judge = "looks fine to me"
	r := runQualityCase(context.Background(), c, offlineProfile("openai"), 1, Options{Mode: "offline", Now: func() time.Time { return time.Unix(0, 0).UTC() }}, &reservation{})
	if r.Passed || r.Reason != "judge_verdict_invalid" {
		t.Fatalf("passed=%v reason=%s", r.Passed, r.Reason)
	}
}

func TestQualityMismatchWhenExpectationDiffers(t *testing.T) {
	suite, _ := LoadSuite(qualitySuite)
	c := suite.Cases[0]
	c.Quality.Fails = []string{"max_runes"}
	r := runQualityCase(context.Background(), c, offlineProfile("anthropic"), 1, Options{Mode: "offline", Now: func() time.Time { return time.Unix(0, 0).UTC() }}, &reservation{})
	if r.Passed || r.Reason != "quality_mismatch" {
		t.Fatalf("passed=%v reason=%s", r.Passed, r.Reason)
	}
}

func TestQualityFailedDraftCallEndsTheCase(t *testing.T) {
	suite, _ := LoadSuite(qualitySuite)
	c := suite.Cases[0]
	c.Limits.MaxUSD = 1e-12
	r := runQualityCase(context.Background(), c, offlineProfile("openai"), 1, Options{Mode: "offline", Now: func() time.Time { return time.Unix(0, 0).UTC() }}, &reservation{})
	if r.Passed || r.EndReason != "budget" || len(r.Quality) != 0 {
		t.Fatalf("r=%+v", r)
	}
}

func TestQualityDigestFollowsTheTemplate(t *testing.T) {
	suite, _ := LoadSuite(qualitySuite)
	dir := t.TempDir()
	b, _ := os.ReadFile("../../templates/viral.agent.yaml")
	if err := os.WriteFile(filepath.Join(dir, "viral.agent.yaml"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	cases := append([]Case(nil), suite.Cases...)
	q := *cases[0].Quality
	q.resolvedDir = dir
	cases[0].Quality = &q
	same := qualityFixtureDigest(cases[:1])
	if err := os.WriteFile(filepath.Join(dir, "viral.agent.yaml"), append(b, '#'), 0o644); err != nil {
		t.Fatal(err)
	}
	if same == "" || same == qualityFixtureDigest(cases[:1]) {
		t.Fatal("digest ignores the template")
	}
	if qualityFixtureDigest(nil) != "" {
		t.Fatal("digest without quality cases must be empty")
	}
}

func TestQualitySuiteValidation(t *testing.T) {
	good := func() Case {
		return Case{ID: "q", Kind: "quality", Expect: Expectation{EndReason: "completed"}, Quality: &QualityCase{TemplateDir: ".", TemplateID: "viral", Kind: quality.Post, Draft: "d", Judge: "{}"}}
	}
	if err := validateSuite(&Suite{Cases: []Case{good()}}); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Case){
		"kind":        func(c *Case) { c.Quality.Kind = "dm" },
		"no draft":    func(c *Case) { c.Quality.Draft = "" },
		"no judge":    func(c *Case) { c.Quality.Judge = "" },
		"no dir":      func(c *Case) { c.Quality.TemplateDir = "" },
		"payload":     func(c *Case) { c.Kind = "guard"; c.Guard = &GuardCase{} },
		"stale":       func(c *Case) { c.Quality.OwnActions = []QualityAction{{Kind: "post", Text: "x", HoursAgo: 24}} },
		"action kind": func(c *Case) { c.Quality.OwnActions = []QualityAction{{Kind: "dm", Text: "x"}} },
		"check id":    func(c *Case) { c.Quality.Fails = []string{"Bad-ID"} },
	} {
		c := good()
		mutate(&c)
		if err := validateSuite(&Suite{Cases: []Case{c}}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestQualityReportKeepsChecksAndRejectsBadIDs(t *testing.T) {
	suite, _ := LoadSuite(qualitySuite)
	report := runQuality(t, suite)
	if _, err := canonicalReport(report); err != nil {
		t.Fatal(err)
	}
	report.Results[0].Quality = append([]quality.Check(nil), report.Results[0].Quality...)
	report.Results[0].Quality[0].ID = "Bad ID"
	if _, err := canonicalReport(report); err == nil {
		t.Fatal("bad check id accepted")
	}
}

func TestQualityLiveRunNeedsNoRecordedOutputs(t *testing.T) {
	suite, _ := LoadSuite(qualitySuite)
	if _, err := Run(context.Background(), suite, Options{Mode: "live", CaseIDs: []string{"clean_post"}}); err == nil || !strings.Contains(err.Error(), "explicit profiles") {
		t.Fatalf("err=%v", err)
	}
}

func offlineProfile(protocol string) Profile {
	return Profile{ID: "p-" + protocol, ProviderID: "fixture", Config: config.LLMConfig{Protocol: protocol, Model: "m", PricePerMTokInUSD: 1, PricePerMTokOutUSD: 2}}
}

func TestQualityOutcomeByMode(t *testing.T) {
	checks := []quality.Check{{ID: "max_runes", Pass: false}, {ID: "bad_example", Pass: true}}
	if ok, reason := qualityOutcome("live", checks, nil); !ok || reason != "scored" {
		t.Errorf("live: %v %s", ok, reason)
	}
	if ok, _ := qualityOutcome("offline", checks, nil); ok {
		t.Error("offline accepted an unexpected failure")
	}
	if ok, reason := qualityOutcome("offline", checks, []string{"max_runes"}); !ok || reason != "matched" {
		t.Errorf("offline: %v %s", ok, reason)
	}
	if ok, _ := qualityOutcome("offline", checks, []string{"max_runes", "bad_example"}); ok {
		t.Error("offline accepted a missing failure")
	}
}

func TestValidKindRejectsMismatchedCases(t *testing.T) {
	chat := Case{Kind: "chat", Messages: []llm.Message{{Role: "user", Content: "x"}}, Responses: map[string][]Response{"openai": {{Status: 200}}}}
	if !validKind(chat) {
		t.Fatal("valid chat case rejected")
	}
	for name, c := range map[string]Case{
		"no kind":              {Messages: chat.Messages, Responses: chat.Responses},
		"unknown kind":         {Kind: "bogus"},
		"chat without script":  {Kind: "chat"},
		"scenario on chat":     {Kind: "chat", Scenario: "x", Messages: chat.Messages, Responses: chat.Responses},
		"missing scenario":     {Kind: "owner"},
		"unknown scenario":     {Kind: "owner", Scenario: "bogus"},
		"transcript on owner":  {Kind: "owner", Scenario: "controls", Messages: chat.Messages},
		"payload without kind": {Kind: "chat", Guard: &GuardCase{}, Messages: chat.Messages, Responses: chat.Responses},
		"kind without payload": {Kind: "guard"},
	} {
		if validKind(c) {
			t.Errorf("%s accepted", name)
		}
	}
	for kind := range runners {
		if _, ok := scenarios[kind]; ok && kind == "chat" {
			t.Errorf("chat has scenarios")
		}
	}
}
