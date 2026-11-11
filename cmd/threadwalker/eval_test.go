package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// These tests drive the public `eval` command through the injected run()
// boundary. Fixtures are deliberately small temporary suites: the canonical
// fixture is owned elsewhere and is not copied here.

const (
	evalCLIPlantedKey  = "sk-eval-planted-secret-17"
	evalCLIPlantedUser = "stranger_eval_17"
)

type evalCLIReport struct {
	Version           string              `json:"report_version"`
	SuiteVersion      string              `json:"suite_version"`
	SuiteDigest       string              `json:"suite_digest"`
	Mode              string              `json:"mode"`
	Revision          string              `json:"revision"`
	Seed              int64               `json:"seed"`
	Repeat            int                 `json:"repeat"`
	ConfigFingerprint string              `json:"config_fingerprint"`
	InputsFingerprint string              `json:"inputs_fingerprint"`
	TimingKind        string              `json:"timing_kind"`
	Results           []evalCLICaseResult `json:"results"`
}

type evalCLICaseResult struct {
	CaseID     string   `json:"case_id"`
	ProfileID  string   `json:"profile_id"`
	ProviderID string   `json:"provider_id"`
	Model      string   `json:"model"`
	Protocol   string   `json:"protocol"`
	Repeat     int      `json:"repeat"`
	Passed     bool     `json:"passed"`
	Reason     string   `json:"reason"`
	EndReason  string   `json:"end_reason"`
	Steps      int      `json:"steps"`
	ToolCalls  []string `json:"tool_calls"`
	TokensIn   int64    `json:"tokens_in"`
	TokensOut  int64    `json:"tokens_out"`
	CostUSD    float64  `json:"cost_usd"`
	DurationMS float64  `json:"duration_ms"`
}

// evalCLIEnv records environment lookups so an offline run can prove it never
// resolves configured API key variables.
type evalCLIEnv struct {
	mu     sync.Mutex
	values map[string]string
	calls  []string
}

func (e *evalCLIEnv) Get(name string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, name)
	return e.values[name]
}

func (e *evalCLIEnv) Called(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, call := range e.calls {
		if call == name {
			return true
		}
	}
	return false
}

func evalCLIRun(t *testing.T, ctx context.Context, args []string, getenv func(string) string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(ctx, args, getenv, &out, &errOut)
	return code, out.String(), errOut.String()
}

func evalCLIWriteFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return path
}

func evalCLIWriteSuite(t *testing.T, suite map[string]any) string {
	t.Helper()
	data, err := json.MarshalIndent(suite, "", "  ")
	if err != nil {
		t.Fatalf("marshalling suite: %v", err)
	}
	return evalCLIWriteFile(t, "suite.json", string(data)+"\n")
}

func evalCLIReadReport(t *testing.T, path string) (evalCLIReport, string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading eval report %s: %v", path, err)
	}
	var report evalCLIReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("parsing eval report %s: %v\n%s", path, err, raw)
	}
	return report, string(raw)
}

func evalCLIOpenAIText(text string, tokensIn, tokensOut int64) map[string]any {
	return map[string]any{
		"status": 200,
		"body": map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": text},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": tokensIn, "completion_tokens": tokensOut},
		},
	}
}

func evalCLIAnthropicText(text string, tokensIn, tokensOut int64) map[string]any {
	return map[string]any{
		"status": 200,
		"body": map[string]any{
			"role":        "assistant",
			"content":     []any{map[string]any{"type": "text", "text": text}},
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": tokensIn, "output_tokens": tokensOut},
		},
	}
}

func evalCLIOpenAIToolCall(tokensIn, tokensOut int64) map[string]any {
	return map[string]any{
		"status": 200,
		"body": map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{
					"role": "assistant",
					"tool_calls": []any{map[string]any{
						"id": "call-1", "type": "function",
						"function": map[string]any{"name": "echo", "arguments": "{}"},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]any{"prompt_tokens": tokensIn, "completion_tokens": tokensOut},
		},
	}
}

func evalCLIAnthropicToolCall(tokensIn, tokensOut int64) map[string]any {
	return map[string]any{
		"status": 200,
		"body": map[string]any{
			"role": "assistant",
			"content": []any{map[string]any{
				"type": "tool_use", "id": "call-1", "name": "echo", "input": map[string]any{},
			}},
			"stop_reason": "tool_use",
			"usage":       map[string]any{"input_tokens": tokensIn, "output_tokens": tokensOut},
		},
	}
}

func evalCLIEchoTool() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "echo",
			"description": "returns the configured fixture output",
			"parameters":  map[string]any{"type": "object"},
		},
	}
}

func evalCLITextCase(id, text string) map[string]any {
	return map[string]any{
		"id":          id,
		"description": "one scripted final response",
		"messages":    []any{map[string]any{"role": "user", "content": "return the fixture text"}},
		"responses": map[string]any{
			"openai":    []any{evalCLIOpenAIText(text, 10, 5)},
			"anthropic": []any{evalCLIAnthropicText(text, 10, 5)},
		},
		"expect": map[string]any{"end_reason": "completed", "content": text},
	}
}

func evalCLIToolCase(id, text string) map[string]any {
	return map[string]any{
		"id":          id,
		"description": "one known tool call then a final response",
		"messages":    []any{map[string]any{"role": "user", "content": "call echo then answer"}},
		"tools":       []any{evalCLIEchoTool()},
		"responses": map[string]any{
			"openai": []any{
				evalCLIOpenAIToolCall(10, 5),
				evalCLIOpenAIText(text, 12, 6),
			},
			"anthropic": []any{
				evalCLIAnthropicToolCall(10, 5),
				evalCLIAnthropicText(text, 12, 6),
			},
		},
		"tool_outputs": map[string]any{"echo": "fixture tool output"},
		"expect":       map[string]any{"end_reason": "completed", "content": text, "tools": []any{"echo"}},
	}
}

func evalCLIStandardSuite(text string) map[string]any {
	return map[string]any{
		"version":     "v1",
		"description": "temporary eval CLI test suite",
		"cases": []any{
			evalCLITextCase("text-case", text),
			evalCLIToolCase("tool-case", "tool final"),
		},
	}
}

func evalCLICostSuite() map[string]any {
	return map[string]any{
		"version": "v1",
		"cases": []any{
			func() map[string]any {
				c := evalCLITextCase("cost-case", "cost final")
				c["responses"] = map[string]any{
					"openai":    []any{evalCLIOpenAIText("cost final", 1000, 500)},
					"anthropic": []any{evalCLIAnthropicText("cost final", 1000, 500)},
				}
				return c
			}(),
		},
	}
}

func evalCLIRegressionSuite() map[string]any {
	c := evalCLITextCase("regression-case", "expected final")
	c["responses"] = map[string]any{
		"openai":    []any{evalCLIOpenAIText("expected final", 10, 5)},
		"anthropic": []any{evalCLIAnthropicText("unexpected final", 10, 5)},
	}
	return map[string]any{"version": "v1", "cases": []any{c}}
}

// evalCLICatalog uses arbitrary provider/profile/model names and local-only
// endpoints. Offline code must never dial these or read the key variables.
const evalCLICatalog = `providers:
  arbitrary-openai:
    protocol: openai
    base_url: http://127.0.0.1:1/v1
    api_key_env: EVAL_FAKE_KEY_ONE
  arbitrary-anthropic:
    protocol: anthropic
    base_url: http://127.0.0.1:1
    api_key_env: EVAL_FAKE_KEY_TWO
models:
  base-openai:
    provider: arbitrary-openai
    model: arbitrary-openai-model-7
    price_per_mtok_in_usd: 1
    price_per_mtok_out_usd: 2
  base-anthropic:
    provider: arbitrary-anthropic
    model: arbitrary-anthropic-model-9
    price_per_mtok_in_usd: 1
    price_per_mtok_out_usd: 2
  cheap-model:
    provider: arbitrary-openai
    model: arbitrary-cheap-model-3
    price_per_mtok_in_usd: 1
    price_per_mtok_out_usd: 2
  costly-model:
    provider: arbitrary-openai
    model: arbitrary-costly-model-5
    price_per_mtok_in_usd: 10
    price_per_mtok_out_usd: 20
`

func evalCLIWriteCatalog(t *testing.T) string {
	t.Helper()
	return evalCLIWriteFile(t, "catalog.yaml", evalCLICatalog)
}

func evalCLIFindResult(t *testing.T, report evalCLIReport, caseID, profileID string) evalCLICaseResult {
	t.Helper()
	for _, result := range report.Results {
		if result.CaseID == caseID && result.ProfileID == profileID {
			return result
		}
	}
	t.Fatalf("report has no result for case=%q profile=%q: %+v", caseID, profileID, report.Results)
	return evalCLICaseResult{}
}

func evalCLINormalized(report evalCLIReport) evalCLIReport {
	for i := range report.Results {
		report.Results[i].DurationMS = 0
	}
	return report
}

func TestEvalCLIDefaultOfflineReport(t *testing.T) {
	suitePath := evalCLIWriteSuite(t, evalCLIStandardSuite("fixture final answer"))
	reportPath := filepath.Join(t.TempDir(), "report.json")
	env := func(string) string { return "" }

	code, stdout, stderr := evalCLIRun(t, context.Background(), []string{
		"eval", "-suite", suitePath, "-out", reportPath,
		"-seed", "11", "-revision", "eval-cli-test",
	}, env)
	if code != 0 {
		t.Fatalf("run(eval) = %d, want 0; stderr=%s stdout=%s", code, stderr, stdout)
	}
	if !strings.Contains(strings.ToLower(stdout), "passed") {
		t.Errorf("human summary %q does not report pass counts", stdout)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}

	report, raw := evalCLIReadReport(t, reportPath)
	if report.Version != "v1" {
		t.Errorf("report_version = %q, want v1", report.Version)
	}
	if report.SuiteVersion != "v1" {
		t.Errorf("suite_version = %q, want v1", report.SuiteVersion)
	}
	if report.SuiteDigest == "" || report.ConfigFingerprint == "" || report.InputsFingerprint == "" {
		t.Errorf("report must carry suite, configuration and evaluation-input fingerprints: %+v", report)
	}
	if report.Mode != "offline" {
		t.Errorf("mode = %q, want offline", report.Mode)
	}
	if report.Revision != "eval-cli-test" {
		t.Errorf("revision = %q, want eval-cli-test", report.Revision)
	}
	if report.Seed != 11 || report.Repeat != 1 {
		t.Errorf("seed/repeat = %d/%d, want 11/1", report.Seed, report.Repeat)
	}
	if !strings.Contains(strings.ToLower(report.TimingKind), "wall") {
		t.Errorf("timing_kind = %q, want a distinct wall-clock label", report.TimingKind)
	}
	if len(report.Results) == 0 {
		t.Fatal("report has no results")
	}
	for _, result := range report.Results {
		if !result.Passed {
			t.Errorf("case %q profile %q failed: reason=%s end_reason=%s", result.CaseID, result.ProfileID, result.Reason, result.EndReason)
		}
		if result.ProfileID == "" || result.ProviderID == "" || result.Model == "" {
			t.Errorf("result lacks profile/provider/model metadata: %+v", result)
		}
		if result.Protocol != "openai" && result.Protocol != "anthropic" {
			t.Errorf("protocol = %q, want openai or anthropic", result.Protocol)
		}
	}

	textResults := 0
	for _, result := range report.Results {
		switch result.CaseID {
		case "text-case":
			textResults++
			if result.TokensIn != 10 || result.TokensOut != 5 {
				t.Errorf("text-case tokens = %d/%d, want 10/5", result.TokensIn, result.TokensOut)
			}
			if result.Steps != 1 || len(result.ToolCalls) != 0 {
				t.Errorf("text-case steps/tools = %d/%v, want 1/[]", result.Steps, result.ToolCalls)
			}
		case "tool-case":
			if result.Steps != 2 || !reflect.DeepEqual(result.ToolCalls, []string{"echo"}) {
				t.Errorf("tool-case steps/tools = %d/%v, want 2/[echo]", result.Steps, result.ToolCalls)
			}
		default:
			t.Errorf("unexpected case id %q", result.CaseID)
		}
	}
	if textResults == 0 {
		t.Error("report has no text-case result")
	}
	if strings.Contains(raw, evalCLIPlantedKey) || strings.Contains(raw, evalCLIPlantedUser) {
		t.Errorf("report contains planted fixture text: %s", raw)
	}
}

func TestEvalCLIDeterministicRepeatedRunsExcludeTiming(t *testing.T) {
	suitePath := evalCLIWriteSuite(t, evalCLIStandardSuite("deterministic final"))
	env := func(string) string { return "" }
	args := []string{
		"eval", "-suite", suitePath, "-seed", "99", "-repeat", "2",
		"-revision", "deterministic-revision",
	}

	firstPath := filepath.Join(t.TempDir(), "first.json")
	code, stdout, stderr := evalCLIRun(t, context.Background(), append(append([]string{}, args...), "-out", firstPath), env)
	if code != 0 {
		t.Fatalf("first run = %d, want 0; stderr=%s stdout=%s", code, stderr, stdout)
	}
	secondPath := filepath.Join(t.TempDir(), "second.json")
	code, stdout, stderr = evalCLIRun(t, context.Background(), append(append([]string{}, args...), "-out", secondPath), env)
	if code != 0 {
		t.Fatalf("second run = %d, want 0; stderr=%s stdout=%s", code, stderr, stdout)
	}

	first, _ := evalCLIReadReport(t, firstPath)
	second, _ := evalCLIReadReport(t, secondPath)
	if first.Repeat != 2 || second.Repeat != 2 {
		t.Fatalf("repeat = %d/%d, want 2/2", first.Repeat, second.Repeat)
	}
	if len(first.Results) < 2 {
		t.Fatalf("repeat run produced %d results, want at least 2", len(first.Results))
	}
	for _, result := range first.Results {
		if result.Repeat < 1 || result.Repeat > 2 {
			t.Errorf("result repeat = %d, want 1..2", result.Repeat)
		}
	}
	if !reflect.DeepEqual(evalCLINormalized(first), evalCLINormalized(second)) {
		t.Errorf("semantic reports differ across identical runs (timing excluded)\nfirst:  %+v\nsecond: %+v", first, second)
	}
}

func TestEvalCLIFailingCaseNonzeroAndNoRawContent(t *testing.T) {
	suite := map[string]any{
		"version": "v1",
		"cases": []any{
			func() map[string]any {
				c := evalCLITextCase("failing-case", "actual content that must not leak")
				c["expect"] = map[string]any{"end_reason": "completed", "content": "expected content that must not leak"}
				return c
			}(),
		},
	}
	suitePath := evalCLIWriteSuite(t, suite)
	reportPath := filepath.Join(t.TempDir(), "failing.json")

	code, stdout, stderr := evalCLIRun(t, context.Background(), []string{
		"eval", "-suite", suitePath, "-out", reportPath, "-revision", "failing-test",
	}, func(string) string { return "" })
	if code == 0 {
		t.Fatalf("run(eval) with mismatched expectation = 0, want nonzero; stdout=%s", stdout)
	}
	combined := stdout + "\n" + stderr
	if !strings.Contains(combined, "failing-case") {
		t.Errorf("output does not identify the failing case: %s", combined)
	}
	if !strings.Contains(strings.ToLower(combined), "fail") {
		t.Errorf("output does not report a failure: %s", combined)
	}

	report, raw := evalCLIReadReport(t, reportPath)
	result := evalCLIFindResult(t, report, "failing-case", report.Results[0].ProfileID)
	if result.Passed {
		t.Error("failing case was reported as passed")
	}
	if result.Reason == "" {
		t.Error("failing case has no stable reason code")
	}
	for _, value := range []string{"actual content that must not leak", "expected content that must not leak"} {
		if strings.Contains(raw, value) || strings.Contains(combined, value) {
			t.Errorf("raw fixture content %q leaked into report or diagnostics", value)
		}
	}
}

func TestEvalCLICompareRegressionAndMetricDeltas(t *testing.T) {
	catalogPath := evalCLIWriteCatalog(t)
	baseEnv := func(string) string { return "" }

	t.Run("seeded regression exits nonzero", func(t *testing.T) {
		suitePath := evalCLIWriteSuite(t, evalCLIRegressionSuite())
		baselinePath := filepath.Join(t.TempDir(), "baseline.json")
		code, stdout, stderr := evalCLIRun(t, context.Background(), []string{
			"eval", "-suite", suitePath, "-llms", catalogPath, "-profiles", "base-openai",
			"-out", baselinePath, "-revision", "regression-baseline",
		}, baseEnv)
		if code != 0 {
			t.Fatalf("baseline run = %d, want 0; stderr=%s stdout=%s", code, stderr, stdout)
		}

		currentPath := filepath.Join(t.TempDir(), "current.json")
		code, stdout, stderr = evalCLIRun(t, context.Background(), []string{
			"eval", "-suite", suitePath, "-llms", catalogPath, "-profiles", "base-anthropic",
			"-compare", baselinePath, "-out", currentPath, "-revision", "regression-current",
		}, baseEnv)
		if code == 0 {
			t.Fatalf("regressed comparison = 0, want nonzero; stdout=%s", stdout)
		}
		combined := stdout + "\n" + stderr
		if !strings.Contains(combined, "regression-case") {
			t.Errorf("comparison output does not name the regressed case: %s", combined)
		}
		lower := strings.ToLower(combined)
		if !strings.Contains(lower, "regress") && !strings.Contains(lower, "fail") {
			t.Errorf("comparison output does not report a regression: %s", combined)
		}

		report, _ := evalCLIReadReport(t, currentPath)
		result := evalCLIFindResult(t, report, "regression-case", "base-anthropic")
		if result.Passed {
			t.Error("regressed run was reported as passed")
		}
		if result.Reason == "" {
			t.Error("regressed case has no reason code")
		}
	})

	t.Run("cost delta reports without regression", func(t *testing.T) {
		suitePath := evalCLIWriteSuite(t, evalCLICostSuite())
		baselinePath := filepath.Join(t.TempDir(), "baseline.json")
		code, stdout, stderr := evalCLIRun(t, context.Background(), []string{
			"eval", "-suite", suitePath, "-llms", catalogPath, "-profiles", "cheap-model",
			"-out", baselinePath, "-revision", "cost-baseline",
		}, baseEnv)
		if code != 0 {
			t.Fatalf("baseline run = %d, want 0; stderr=%s stdout=%s", code, stderr, stdout)
		}
		currentPath := filepath.Join(t.TempDir(), "current.json")
		code, stdout, stderr = evalCLIRun(t, context.Background(), []string{
			"eval", "-suite", suitePath, "-llms", catalogPath, "-profiles", "costly-model",
			"-compare", baselinePath, "-out", currentPath, "-revision", "cost-current",
		}, baseEnv)
		if code != 0 {
			t.Fatalf("metric-only comparison = %d, want 0; stderr=%s stdout=%s", code, stderr, stdout)
		}
		baseline, _ := evalCLIReadReport(t, baselinePath)
		current, _ := evalCLIReadReport(t, currentPath)
		baseResult := evalCLIFindResult(t, baseline, "cost-case", "cheap-model")
		currResult := evalCLIFindResult(t, current, "cost-case", "costly-model")
		if baseline.InputsFingerprint != current.InputsFingerprint {
			t.Errorf("model/price change altered the evaluation-input fingerprint: %q vs %q", baseline.InputsFingerprint, current.InputsFingerprint)
		}
		if currResult.CostUSD <= baseResult.CostUSD {
			t.Errorf("cost delta not measured: cheap=%v costly=%v", baseResult.CostUSD, currResult.CostUSD)
		}
		if currResult.TokensIn != baseResult.TokensIn || currResult.TokensOut != baseResult.TokensOut {
			t.Errorf("token counts changed unexpectedly: cheap=%d/%d costly=%d/%d", baseResult.TokensIn, baseResult.TokensOut, currResult.TokensIn, currResult.TokensOut)
		}
		lower := strings.ToLower(stdout)
		if !strings.Contains(lower, "cost") && !strings.Contains(lower, "tokens") && !strings.Contains(lower, "metrics") {
			t.Errorf("comparison summary does not report measured token/cost deltas: %s", stdout)
		}
	})

	t.Run("ambiguous multi-profile comparison fails", func(t *testing.T) {
		suitePath := evalCLIWriteSuite(t, evalCLICostSuite())
		baselinePath := filepath.Join(t.TempDir(), "baseline.json")
		code, stdout, stderr := evalCLIRun(t, context.Background(), []string{
			"eval", "-suite", suitePath, "-llms", catalogPath, "-profiles", "cheap-model,costly-model",
			"-out", baselinePath, "-revision", "ambiguous-baseline",
		}, baseEnv)
		if code != 0 {
			t.Fatalf("multi-profile baseline = %d, want 0; stderr=%s stdout=%s", code, stderr, stdout)
		}
		code, stdout, stderr = evalCLIRun(t, context.Background(), []string{
			"eval", "-suite", suitePath, "-llms", catalogPath, "-profiles", "cheap-model",
			"-compare", baselinePath, "-revision", "ambiguous-current",
		}, baseEnv)
		if code == 0 {
			t.Fatalf("ambiguous comparison = 0, want nonzero; stdout=%s", stdout)
		}
		combined := strings.ToLower(stdout + "\n" + stderr)
		if !strings.Contains(combined, "profile") && !strings.Contains(combined, "ambiguous") && !strings.Contains(combined, "compar") {
			t.Errorf("ambiguous comparison error is not useful: %s", combined)
		}
	})

	t.Run("changed evaluation caps fail", func(t *testing.T) {
		suitePath := evalCLIWriteSuite(t, evalCLICostSuite())
		baselinePath := filepath.Join(t.TempDir(), "baseline.json")
		code, stdout, stderr := evalCLIRun(t, context.Background(), []string{
			"eval", "-suite", suitePath, "-llms", catalogPath, "-profiles", "cheap-model",
			"-out", baselinePath, "-revision", "inputs-baseline",
		}, baseEnv)
		if code != 0 {
			t.Fatalf("inputs baseline = %d, want 0; stderr=%s stdout=%s", code, stderr, stdout)
		}
		code, stdout, stderr = evalCLIRun(t, context.Background(), []string{
			"eval", "-suite", suitePath, "-llms", catalogPath, "-profiles", "cheap-model",
			"-max-steps", "3", "-compare", baselinePath, "-revision", "inputs-current",
		}, baseEnv)
		if code == 0 {
			t.Fatalf("changed-cap comparison = 0, want nonzero; stdout=%s", stdout)
		}
		combined := strings.ToLower(stdout + "\n" + stderr)
		if !strings.Contains(combined, "input") && !strings.Contains(combined, "compar") {
			t.Errorf("changed-cap comparison error is not useful: %s", combined)
		}
	})

	t.Run("suite digest mismatch fails", func(t *testing.T) {
		baselineSuite := evalCLIWriteSuite(t, evalCLIStandardSuite("baseline final"))
		baselinePath := filepath.Join(t.TempDir(), "baseline.json")
		code, stdout, stderr := evalCLIRun(t, context.Background(), []string{
			"eval", "-suite", baselineSuite, "-llms", catalogPath, "-profiles", "cheap-model",
			"-out", baselinePath, "-revision", "digest-baseline",
		}, baseEnv)
		if code != 0 {
			t.Fatalf("digest baseline = %d, want 0; stderr=%s stdout=%s", code, stderr, stdout)
		}
		currentSuite := evalCLIWriteSuite(t, evalCLICostSuite())
		code, stdout, stderr = evalCLIRun(t, context.Background(), []string{
			"eval", "-suite", currentSuite, "-llms", catalogPath, "-profiles", "cheap-model",
			"-compare", baselinePath, "-revision", "digest-current",
		}, baseEnv)
		if code == 0 {
			t.Fatalf("digest mismatch comparison = 0, want nonzero; stdout=%s", stdout)
		}
		combined := strings.ToLower(stdout + "\n" + stderr)
		if !strings.Contains(combined, "suite") && !strings.Contains(combined, "digest") && !strings.Contains(combined, "compar") {
			t.Errorf("digest mismatch error is not useful: %s", combined)
		}
	})
}

func TestEvalCLIMalformedSuitesAndPaths(t *testing.T) {
	validSuites := map[string]string{
		"invalid-json":        `{"version":"v1","cases":[` + evalCLIPlantedKey,
		"unsupported-version": `{"version":"v2","cases":[]}`,
		"empty-cases":         `{"version":"v1","cases":[]}`,
		"duplicate-case-ids": `{"version":"v1","cases":[` +
			`{"id":"same","messages":[{"role":"user","content":"x"}],"responses":{"openai":[{"status":200,"body":{}}]},"expect":{"end_reason":"completed"}},` +
			`{"id":"same","messages":[{"role":"user","content":"x"}],"responses":{"openai":[{"status":200,"body":{}}]},"expect":{"end_reason":"completed"}}]}`,
		"missing-case-id":    `{"version":"v1","cases":[{"messages":[{"role":"user","content":"x"}],"responses":{"openai":[{"status":200,"body":{}}]},"expect":{"end_reason":"completed"}}]}`,
		"body-and-body-raw":  `{"version":"v1","cases":[{"id":"x","messages":[{"role":"user","content":"x"}],"responses":{"openai":[{"status":200,"body":{},"body_raw":"{}"}]},"expect":{"end_reason":"completed"}}]}`,
		"zero-step-limit":    `{"version":"v1","cases":[{"id":"x","messages":[{"role":"user","content":"x"}],"responses":{"openai":[{"status":200,"body":{}}]},"limits":{"max_steps":0},"expect":{"end_reason":"step_limit"}}]}`,
		"negative-usd-limit": `{"version":"v1","cases":[{"id":"x","messages":[{"role":"user","content":"x"}],"responses":{"openai":[{"status":200,"body":{}}]},"limits":{"max_usd":-1},"expect":{"end_reason":"completed"}}]}`,
		"unknown-end-reason": `{"version":"v1","cases":[{"id":"x","messages":[{"role":"user","content":"x"}],"responses":{"openai":[{"status":200,"body":{}}]},"expect":{"end_reason":"made_up"}}]}`,
	}
	env := func(string) string { return "" }
	for name, body := range validSuites {
		t.Run(name, func(t *testing.T) {
			suitePath := evalCLIWriteFile(t, "suite.json", body)
			code, stdout, stderr := evalCLIRun(t, context.Background(), []string{"eval", "-suite", suitePath}, env)
			if code == 0 {
				t.Fatalf("malformed suite %q = 0, want nonzero; stdout=%s", name, stdout)
			}
			if strings.TrimSpace(stdout+stderr) == "" {
				t.Error("malformed suite produced no diagnostic")
			}
			if strings.Contains(stdout+stderr, evalCLIPlantedKey) {
				t.Errorf("malformed suite echoed fixture content: %s", stdout+stderr)
			}
		})
	}

	t.Run("missing-suite-file", func(t *testing.T) {
		code, _, _ := evalCLIRun(t, context.Background(), []string{
			"eval", "-suite", filepath.Join(t.TempDir(), "absent.json"),
		}, env)
		if code == 0 {
			t.Error("missing suite file = 0, want nonzero")
		}
	})

	t.Run("suite-path-is-directory", func(t *testing.T) {
		code, _, _ := evalCLIRun(t, context.Background(), []string{"eval", "-suite", t.TempDir()}, env)
		if code == 0 {
			t.Error("directory as suite = 0, want nonzero")
		}
	})

	t.Run("output-path-is-directory", func(t *testing.T) {
		suitePath := evalCLIWriteSuite(t, evalCLIStandardSuite("output path final"))
		code, stdout, _ := evalCLIRun(t, context.Background(), []string{
			"eval", "-suite", suitePath, "-out", t.TempDir(),
		}, env)
		if code == 0 {
			t.Fatalf("directory as -out = 0, want nonzero; stdout=%s", stdout)
		}
	})

	t.Run("output-parent-missing", func(t *testing.T) {
		suitePath := evalCLIWriteSuite(t, evalCLIStandardSuite("output parent final"))
		out := filepath.Join(t.TempDir(), "missing", "report.json")
		code, stdout, _ := evalCLIRun(t, context.Background(), []string{
			"eval", "-suite", suitePath, "-out", out,
		}, env)
		if code == 0 {
			t.Fatalf("missing -out parent = 0, want nonzero; stdout=%s", stdout)
		}
	})

	t.Run("cancelled-context", func(t *testing.T) {
		suitePath := evalCLIWriteSuite(t, evalCLIStandardSuite("cancelled final"))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		code, stdout, stderr := evalCLIRun(t, ctx, []string{
			"eval", "-suite", suitePath, "-out", filepath.Join(t.TempDir(), "cancelled.json"),
		}, env)
		if code == 0 {
			t.Fatalf("cancelled eval = 0, want nonzero; stdout=%s stderr=%s", stdout, stderr)
		}
	})
}

func TestEvalCLIInvalidFlagsAndCaps(t *testing.T) {
	catalogPath := evalCLIWriteCatalog(t)
	suitePath := evalCLIWriteSuite(t, evalCLIStandardSuite("invalid flags final"))
	env := func(string) string { return "" }

	cases := []struct {
		name  string
		extra []string
	}{
		{"repeat-zero", []string{"-repeat", "0"}},
		{"repeat-negative", []string{"-repeat", "-2"}},
		{"bad-seed", []string{"-seed", "not-a-number"}},
		{"negative-max-usd", []string{"-max-usd", "-1"}},
		{"nan-max-usd", []string{"-max-usd", "NaN"}},
		{"infinite-max-usd", []string{"-max-usd", "Inf"}},
		{"negative-max-steps", []string{"-max-steps", "-1"}},
		{"negative-output-tokens", []string{"-max-output-tokens", "-1"}},
		{"unknown-flag", []string{"-made-up-flag"}},
		{"unknown-case", []string{"-cases", "no-such-case"}},
		{"duplicate-case-selection", []string{"-cases", "text-case,text-case"}},
		{"unknown-profile", []string{"-llms", catalogPath, "-profiles", "not-a-profile"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"eval", "-suite", suitePath, "-revision", "invalid-flags"}
			args = append(args, tc.extra...)
			code, stdout, stderr := evalCLIRun(t, context.Background(), args, env)
			if code == 0 {
				t.Fatalf("invalid arguments %v = 0, want nonzero; stdout=%s", tc.extra, stdout)
			}
			if strings.TrimSpace(stdout+stderr) == "" {
				t.Errorf("invalid arguments %v produced no diagnostic", tc.extra)
			}
		})
	}
}

func TestEvalCLICatalogProfilesOfflineWithoutKeys(t *testing.T) {
	catalogPath := evalCLIWriteCatalog(t)
	suitePath := evalCLIWriteSuite(t, evalCLIStandardSuite("catalog final"))
	reportPath := filepath.Join(t.TempDir(), "catalog.json")
	env := &evalCLIEnv{values: map[string]string{
		"EVAL_FAKE_KEY_ONE": evalCLIPlantedKey,
		"EVAL_FAKE_KEY_TWO": evalCLIPlantedKey,
	}}

	code, stdout, stderr := evalCLIRun(t, context.Background(), []string{
		"eval", "-suite", suitePath, "-llms", catalogPath,
		"-profiles", "base-openai,base-anthropic",
		"-out", reportPath, "-revision", "catalog-offline",
	}, env.Get)
	if code != 0 {
		t.Fatalf("offline catalog eval = %d, want 0; stderr=%s stdout=%s", code, stderr, stdout)
	}
	if env.Called("EVAL_FAKE_KEY_ONE") || env.Called("EVAL_FAKE_KEY_TWO") {
		t.Errorf("offline eval read an API key environment variable: %v", env.calls)
	}
	if strings.Contains(stdout+stderr, evalCLIPlantedKey) {
		t.Errorf("offline diagnostics leaked a planted key: %s%s", stdout, stderr)
	}

	report, raw := evalCLIReadReport(t, reportPath)
	if strings.Contains(raw, evalCLIPlantedKey) {
		t.Errorf("offline report leaked a planted key: %s", raw)
	}
	type wantProfile struct {
		provider string
		model    string
		protocol string
	}
	wanted := map[string]wantProfile{
		"base-openai":    {provider: "arbitrary-openai", model: "arbitrary-openai-model-7", protocol: "openai"},
		"base-anthropic": {provider: "arbitrary-anthropic", model: "arbitrary-anthropic-model-9", protocol: "anthropic"},
	}
	for profileID, want := range wanted {
		results := 0
		for _, result := range report.Results {
			if result.ProfileID != profileID {
				continue
			}
			results++
			if result.ProviderID != want.provider || result.Model != want.model || result.Protocol != want.protocol {
				t.Errorf("profile %q metadata = provider=%q model=%q protocol=%q, want %q/%q/%q", profileID, result.ProviderID, result.Model, result.Protocol, want.provider, want.model, want.protocol)
			}
			if !result.Passed {
				t.Errorf("case %q failed for profile %q: %s", result.CaseID, profileID, result.Reason)
			}
			if result.TokensIn <= 0 || result.TokensOut <= 0 || result.CostUSD <= 0 {
				t.Errorf("profile %q has no measured tokens/cost: %+v", profileID, result)
			}
		}
		if results == 0 {
			t.Errorf("report has no results for profile %q", profileID)
		}
	}
}

func TestEvalCLIReportRedaction(t *testing.T) {
	catalogPath := evalCLIWriteCatalog(t)
	suite := map[string]any{
		"version": "v1",
		"cases": []any{
			evalCLITextCase("redacted-pass", "final text for "+evalCLIPlantedUser+" using "+evalCLIPlantedKey),
			func() map[string]any {
				c := evalCLITextCase("redacted-fail", "raw answer for "+evalCLIPlantedUser+" using "+evalCLIPlantedKey)
				c["expect"] = map[string]any{"end_reason": "completed", "content": "different expectation"}
				return c
			}(),
		},
	}
	suitePath := evalCLIWriteSuite(t, suite)
	reportPath := filepath.Join(t.TempDir(), "redacted.json")

	code, stdout, stderr := evalCLIRun(t, context.Background(), []string{
		"eval", "-suite", suitePath, "-llms", catalogPath, "-profiles", "base-openai",
		"-out", reportPath, "-revision", "redaction-test",
	}, func(string) string { return "" })
	if code == 0 {
		t.Fatalf("suite with a failing case = 0, want nonzero; stdout=%s", stdout)
	}
	report, raw := evalCLIReadReport(t, reportPath)
	pass := evalCLIFindResult(t, report, "redacted-pass", "base-openai")
	if !pass.Passed {
		t.Errorf("redacted-pass unexpectedly failed: %s", pass.Reason)
	}
	fail := evalCLIFindResult(t, report, "redacted-fail", "base-openai")
	if fail.Passed || fail.Reason == "" {
		t.Errorf("redacted-fail = passed %v reason %q, want failed with a reason", fail.Passed, fail.Reason)
	}
	combined := stdout + "\n" + stderr + "\n" + raw
	for _, planted := range []string{evalCLIPlantedKey, evalCLIPlantedUser} {
		if strings.Contains(combined, planted) {
			t.Errorf("report or diagnostics leaked %q: %s", planted, combined)
		}
	}
}

func TestEvalCLIExpectedRuntimeFailuresCanPass(t *testing.T) {
	catalogPath := evalCLIWriteCatalog(t)
	invalid := `{"choices":[` + evalCLIPlantedKey
	suite := map[string]any{
		"version": "v1",
		"cases": []any{
			map[string]any{
				"id":       "invalid-response-case",
				"messages": []any{map[string]any{"role": "user", "content": "malformed provider body"}},
				"responses": map[string]any{
					"openai":    []any{map[string]any{"status": 200, "body_raw": invalid}},
					"anthropic": []any{map[string]any{"status": 200, "body_raw": invalid}},
				},
				"expect": map[string]any{"end_reason": "invalid_response"},
			},
			map[string]any{
				"id":       "provider-error-case",
				"messages": []any{map[string]any{"role": "user", "content": "provider rejects the request"}},
				"responses": map[string]any{
					"openai":    []any{map[string]any{"status": 400, "body": map[string]any{"error": "synthetic"}}},
					"anthropic": []any{map[string]any{"status": 400, "body": map[string]any{"error": "synthetic"}}},
				},
				"expect": map[string]any{"end_reason": "provider_error"},
			},
			map[string]any{
				"id":       "step-limit-case",
				"messages": []any{map[string]any{"role": "user", "content": "keep calling the tool"}},
				"tools":    []any{evalCLIEchoTool()},
				"responses": map[string]any{
					"openai":    []any{evalCLIOpenAIToolCall(10, 5)},
					"anthropic": []any{evalCLIAnthropicToolCall(10, 5)},
				},
				"tool_outputs": map[string]any{"echo": "fixture tool output"},
				"limits":       map[string]any{"max_steps": 1},
				"expect":       map[string]any{"end_reason": "step_limit", "tools": []any{"echo"}},
			},
		},
	}
	suitePath := evalCLIWriteSuite(t, suite)
	reportPath := filepath.Join(t.TempDir(), "runtime.json")

	code, stdout, stderr := evalCLIRun(t, context.Background(), []string{
		"eval", "-suite", suitePath, "-llms", catalogPath, "-profiles", "base-openai",
		"-out", reportPath, "-revision", "runtime-outcomes",
	}, func(string) string { return "" })
	if code != 0 {
		t.Fatalf("expected runtime outcomes = %d, want 0; stderr=%s stdout=%s", code, stderr, stdout)
	}
	report, raw := evalCLIReadReport(t, reportPath)
	for _, want := range []string{"invalid-response-case", "provider-error-case", "step-limit-case"} {
		result := evalCLIFindResult(t, report, want, "base-openai")
		if !result.Passed {
			t.Errorf("case %q failed: reason=%s end_reason=%s", want, result.Reason, result.EndReason)
		}
	}
	if strings.Contains(raw, evalCLIPlantedKey) || strings.Contains(stdout+stderr, evalCLIPlantedKey) {
		t.Errorf("malformed response body leaked into report or diagnostics")
	}
}

func TestEvalCLILiveRequiresExplicitProfilesCasesAndCaps(t *testing.T) {
	catalogPath := evalCLIWriteCatalog(t)
	suitePath := evalCLIWriteSuite(t, evalCLIStandardSuite("live guard final"))
	reportPath := filepath.Join(t.TempDir(), "live.json")
	t.Setenv("EVAL_FAKE_KEY_ONE", "live-fixture-key")
	env := func(string) string { return "" }

	base := func() []string {
		return []string{
			"eval", "-live", "-suite", suitePath, "-llms", catalogPath,
			"-profiles", "base-openai", "-cases", "text-case",
			"-max-usd", "0.01", "-max-steps", "2", "-max-output-tokens", "128",
			"-out", reportPath, "-revision", "live-guard",
		}
	}
	remove := func(args []string, flag string) []string {
		out := make([]string, 0, len(args))
		for i := 0; i < len(args); i++ {
			if args[i] == flag {
				i++
				continue
			}
			out = append(out, args[i])
		}
		return out
	}

	cases := map[string]func() []string{
		"no-live-selection": func() []string { return []string{"eval", "-live", "-suite", suitePath} },
		"missing-profiles":  func() []string { return remove(base(), "-profiles") },
		"missing-cases":     func() []string { return remove(base(), "-cases") },
		"missing-usd-cap":   func() []string { return remove(base(), "-max-usd") },
		"missing-step-cap":  func() []string { return remove(base(), "-max-steps") },
		"missing-output-cap": func() []string {
			return remove(base(), "-max-output-tokens")
		},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := evalCLIRun(t, context.Background(), args(), env)
			if code == 0 {
				t.Fatalf("live eval %s = 0, want refusal; stdout=%s", name, stdout)
			}
			if strings.TrimSpace(stdout+stderr) == "" {
				t.Error("live refusal produced no diagnostic")
			}
		})
	}

	t.Run("missing-credentials", func(t *testing.T) {
		t.Setenv("EVAL_FAKE_KEY_ONE", "")
		code, stdout, stderr := evalCLIRun(t, context.Background(), base(), env)
		if code == 0 {
			t.Fatalf("live eval without environment credentials = 0, want refusal; stdout=%s", stdout)
		}
		if strings.TrimSpace(stdout+stderr) == "" {
			t.Error("credential refusal produced no diagnostic")
		}
	})
}
