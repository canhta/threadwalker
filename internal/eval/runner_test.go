package eval_test

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/eval"
	"github.com/canhta/threadz/internal/llm"
)

func profile(protocol string) eval.Profile {
	return eval.Profile{ID: "chosen-" + protocol, ProviderID: "custom-provider", Config: config.LLMConfig{Protocol: protocol, Model: "arbitrary-model", BaseURL: "https://never-contact.invalid", APIKey: config.NewSecret("planted-secret"), PricePerMTokInUSD: 1, PricePerMTokOutUSD: 2}}
}

func TestRunRealClientTruthfulOutcomesAndRepeatIsolation(t *testing.T) {
	suite, err := loadJSON(t, minimalSuite)
	if err != nil {
		t.Fatal(err)
	}
	opts := eval.Options{Seed: 42, Repeat: 2, Profiles: []eval.Profile{profile("openai")}}
	a, err := eval.Run(context.Background(), suite, opts)
	if err != nil {
		t.Fatal(err)
	}
	b, err := eval.Run(context.Background(), suite, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Results) != 2 || a.TimingKind != "wall_clock" {
		t.Fatalf("report: %+v", a)
	}
	for i, r := range a.Results {
		if !r.Passed || r.Reason != "matched" || r.EndReason != "completed" || r.Steps != 1 || r.TokensIn != 10 || r.TokensOut != 2 || math.Abs(r.CostUSD-0.000014) > 1e-15 || r.Model != "arbitrary-model" || r.Repeat != i+1 {
			t.Fatalf("result: %+v", r)
		}
		a.Results[i].DurationMS = 0
		b.Results[i].DurationMS = 0
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("semantic report changed with identical inputs")
	}
	wrong := "WRONG"
	suite.Cases[0].Expect.Content = &wrong
	regression, err := eval.Run(context.Background(), suite, opts)
	if err != nil {
		t.Fatal(err)
	}
	if regression.Results[0].Passed || regression.Results[0].Reason != "content_mismatch" {
		t.Fatalf("false pass: %+v", regression)
	}
}

func scripted(t *testing.T, protocol string, responses []eval.Response, expect eval.Expectation, limits eval.Limits, tools []string) *eval.Suite {
	t.Helper()
	c := eval.Case{ID: "synthetic", Kind: "chat", Messages: []llm.Message{{Role: "user", Content: "synthetic task"}}, Responses: map[string][]eval.Response{protocol: responses}, Expect: expect, Limits: limits, ToolOutputs: map[string]string{}}
	for _, name := range tools {
		c.Tools = append(c.Tools, llm.Tool{Type: "function", Function: llm.Function{Name: name, Parameters: json.RawMessage(`{"type":"object"}`)}})
		c.ToolOutputs[name] = "synthetic local result"
	}
	data, err := json.Marshal(eval.Suite{Cases: []eval.Case{c}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := loadJSON(t, string(data))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func finalResponse(protocol, content string) eval.Response {
	var body string
	if protocol == "openai" {
		body = `{"choices":[{"message":{"role":"assistant","content":` + quoted(content) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`
	} else {
		body = `{"role":"assistant","content":[{"type":"text","text":` + quoted(content) + `}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`
	}
	return eval.Response{Status: 200, Body: json.RawMessage(body)}
}
func quoted(s string) string { b, _ := json.Marshal(s); return string(b) }
func toolResponse(protocol, name, args string) eval.Response {
	var body string
	if protocol == "openai" {
		body = `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call1","type":"function","function":{"name":` + quoted(name) + `,"arguments":` + quoted(args) + `}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`
	} else {
		body = `{"role":"assistant","content":[{"type":"tool_use","id":"call1","name":` + quoted(name) + `,"input":` + args + `}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":2}}`
	}
	return eval.Response{Status: 200, Body: json.RawMessage(body)}
}

func TestRunBothProtocolsToolsAndActualAccounting(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			done := "DONE"
			s := scripted(t, protocol, []eval.Response{toolResponse(protocol, "lookup", "{}"), toolResponse(protocol, "append_marker", "{}"), finalResponse(protocol, done)}, eval.Expectation{EndReason: "completed", Content: &done, Tools: []string{"lookup", "append_marker"}}, eval.Limits{}, []string{"lookup", "append_marker"})
			r, err := eval.Run(context.Background(), s, eval.Options{Profiles: []eval.Profile{profile(protocol)}, Repeat: 2})
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range r.Results {
				if !c.Passed || c.Steps != 3 || c.TokensIn != 30 || c.TokensOut != 6 || math.Abs(c.CostUSD-0.000042) > 1e-15 {
					t.Fatalf("result: %+v", c)
				}
			}
		})
	}
}

func TestMalformedAndUnknownToolCallsNeverExecute(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		for _, tt := range []struct{ name, args string }{{"lookup", "[]"}, {"unconfigured", "{}"}} {
			t.Run(protocol+tt.name, func(t *testing.T) {
				s := scripted(t, protocol, []eval.Response{toolResponse(protocol, tt.name, tt.args), finalResponse(protocol, "DONE")}, eval.Expectation{EndReason: "completed", Tools: []string{}}, eval.Limits{}, []string{"lookup"})
				report, err := eval.Run(context.Background(), s, eval.Options{Profiles: []eval.Profile{profile(protocol)}})
				if err != nil {
					t.Fatal(err)
				}
				r := report.Results[0]
				if !r.Passed || r.Steps != 2 || len(r.ToolCalls) != 0 || r.TokensIn != 20 {
					t.Fatalf("malformed/unknown tool executed: %+v", r)
				}
			})
		}
	}
}

func TestUsageAndMalformedResponseOutcomesRemainTruthful(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		for _, tt := range []struct {
			name   string
			edit   func(string) string
			end    string
			tokens int64
		}{
			{"negative", func(s string) string {
				if protocol == "openai" {
					return strings.Replace(s, `"prompt_tokens":10`, `"prompt_tokens":-1`, 1)
				}
				return strings.Replace(s, `"input_tokens":10`, `"input_tokens":-1`, 1)
			}, "accounting_error", 0},
			{"incomplete", func(s string) string {
				if protocol == "openai" {
					return strings.Replace(s, `"completion_tokens":2`, `"unknown_tokens":2`, 1)
				}
				return strings.Replace(s, `"output_tokens":2`, `"unknown_tokens":2`, 1)
			}, "accounting_error", 0},
			{"invalid role", func(s string) string { return strings.Replace(s, `"role":"assistant"`, `"role":"invalid"`, 1) }, "invalid_response", 10},
		} {
			t.Run(protocol+tt.name, func(t *testing.T) {
				response := finalResponse(protocol, "DONE")
				response.Body = json.RawMessage(tt.edit(string(response.Body)))
				s := scripted(t, protocol, []eval.Response{response}, eval.Expectation{EndReason: tt.end}, eval.Limits{}, nil)
				report, err := eval.Run(context.Background(), s, eval.Options{Profiles: []eval.Profile{profile(protocol)}})
				if err != nil {
					t.Fatal(err)
				}
				r := report.Results[0]
				if !r.Passed || r.TokensIn != tt.tokens {
					t.Fatalf("usage outcome: %+v", r)
				}
			})
		}
	}
}

func TestRunSafeErrorOutcomesAndBounds(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			malformed := "malformed planted-secret @stranger"
			tests := []struct {
				name, end string
				responses []eval.Response
				limits    eval.Limits
				steps     int
				in        int64
			}{
				{name: "timeout", end: "provider_error", responses: []eval.Response{{Error: "timeout"}, {Error: "timeout"}}},
				{name: "cancelled", end: "cancelled", responses: []eval.Response{{Error: "cancelled"}}},
				{name: "transport", end: "provider_error", responses: []eval.Response{{Error: "transport"}, {Error: "transport"}}},
				{name: "http", end: "provider_error", responses: []eval.Response{{Status: 400, BodyRaw: &malformed}}},
				{name: "malformed", end: "invalid_response", responses: []eval.Response{{Status: 200, BodyRaw: &malformed}}},
				{name: "missing usage", end: "accounting_error", responses: []eval.Response{{Status: 200, Body: json.RawMessage(`{}`)}}},
				{name: "steps", end: "step_limit", responses: []eval.Response{toolResponse(protocol, "lookup", "{}"), finalResponse(protocol, "DONE")}, limits: eval.Limits{MaxSteps: 1}, steps: 1, in: 10},
				{name: "spend", end: "budget", responses: []eval.Response{toolResponse(protocol, "lookup", "{}"), finalResponse(protocol, "DONE")}, limits: eval.Limits{MaxUSD: 0.00001}, steps: 1, in: 10},
				{name: "retry", end: "completed", responses: []eval.Response{{Status: 429, BodyRaw: &malformed}, finalResponse(protocol, "DONE")}, steps: 1, in: 10},
				{name: "exhaust retries", end: "provider_error", responses: []eval.Response{{Status: 503, BodyRaw: &malformed}, {Status: 503, BodyRaw: &malformed}, {Status: 503, BodyRaw: &malformed}}},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					s := scripted(t, protocol, tt.responses, eval.Expectation{EndReason: tt.end}, tt.limits, []string{"lookup"})
					report, err := eval.Run(context.Background(), s, eval.Options{Profiles: []eval.Profile{profile(protocol)}})
					if err != nil {
						t.Fatal(err)
					}
					r := report.Results[0]
					if !r.Passed || r.EndReason != tt.end || r.Steps != tt.steps || r.TokensIn != tt.in {
						t.Fatalf("result: %+v", r)
					}
				})
			}
		})
	}
}

func TestRunMissingScriptAndOutputCapCannotFalsePass(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			s := scripted(t, protocol, []eval.Response{toolResponse(protocol, "lookup", "{}")}, eval.Expectation{EndReason: "provider_error"}, eval.Limits{}, []string{"lookup"})
			r, err := eval.Run(context.Background(), s, eval.Options{Profiles: []eval.Profile{profile(protocol)}})
			if err != nil {
				t.Fatal(err)
			}
			if r.Results[0].Passed || r.Results[0].Reason != "fixture_exhausted" {
				t.Fatalf("script exhaustion false pass: %+v", r)
			}
			s = scripted(t, protocol, []eval.Response{finalResponse(protocol, "DONE")}, eval.Expectation{EndReason: "completed"}, eval.Limits{}, nil)
			r, err = eval.Run(context.Background(), s, eval.Options{Profiles: []eval.Profile{profile(protocol)}, MaxOutputTokens: 1})
			if err != nil {
				t.Fatal(err)
			}
			if r.Results[0].Passed || r.Results[0].Reason != "output_limit_exceeded" || r.Results[0].TokensOut != 2 {
				t.Fatalf("untruthful output cap: %+v", r)
			}
		})
	}
}

func TestOutputCapStopsBeforeToolExecution(t *testing.T) {
	s := scripted(t, "openai", []eval.Response{toolResponse("openai", "lookup", "{}"), finalResponse("openai", "DONE")}, eval.Expectation{EndReason: "invalid_response"}, eval.Limits{}, []string{"lookup"})
	report, err := eval.Run(context.Background(), s, eval.Options{Profiles: []eval.Profile{profile("openai")}, MaxOutputTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	r := report.Results[0]
	if len(r.ToolCalls) != 0 || r.Steps != 0 || r.TokensIn != 10 || r.TokensOut != 2 || r.EndReason != "invalid_response" || r.Reason != "output_limit_exceeded" {
		t.Fatalf("output violation continued execution: %+v", r)
	}
}

func TestProfileSelectionFingerprintAndCancellation(t *testing.T) {
	s, err := loadJSON(t, minimalSuite)
	if err != nil {
		t.Fatal(err)
	}
	p := profile("openai")
	a, err := eval.Run(context.Background(), s, eval.Options{Profiles: []eval.Profile{p}, CaseIDs: []string{"answer"}})
	if err != nil {
		t.Fatal(err)
	}
	p.Config.APIKey = config.NewSecret("different-secret")
	p.Config.APIKeyEnv = "DIFFERENT_ENV"
	b, err := eval.Run(context.Background(), s, eval.Options{Profiles: []eval.Profile{p}})
	if err != nil {
		t.Fatal(err)
	}
	if a.ConfigFingerprint != b.ConfigFingerprint {
		t.Fatal("credential affected fingerprint")
	}
	p.Config.Model = "another-arbitrary-model"
	c, err := eval.Run(context.Background(), s, eval.Options{Profiles: []eval.Profile{p}})
	if err != nil {
		t.Fatal(err)
	}
	if a.ConfigFingerprint == c.ConfigFingerprint {
		t.Fatal("model selection missing from fingerprint")
	}
	if a.InputsFingerprint == "" || a.InputsFingerprint != c.InputsFingerprint {
		t.Fatal("profile changed eval inputs fingerprint")
	}
	changed, err := eval.Run(context.Background(), s, eval.Options{Profiles: []eval.Profile{p}, MaxSteps: 2})
	if err != nil {
		t.Fatal(err)
	}
	if a.InputsFingerprint == changed.InputsFingerprint {
		t.Fatal("step cap omitted from eval inputs fingerprint")
	}
	s.Cases[0].Expect = eval.Expectation{EndReason: "cancelled"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := eval.Run(ctx, s, eval.Options{Profiles: []eval.Profile{p}})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Results[0].Passed || r.Results[0].Steps != 0 {
		t.Fatalf("cancelled run: %+v", r)
	}
}

func TestLiveReservationOvershootPreservesActualUsageAndStops(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9000000,"completion_tokens":2}}`))
	}))
	defer server.Close()
	getenv := func(name string) string {
		if name == "EVAL_OVERSHOOT_KEY" {
			return "fake-key"
		}
		return ""
	}
	p := profile("openai")
	p.Config.BaseURL = server.URL
	p.Config.APIKeyEnv = "EVAL_OVERSHOOT_KEY"
	p.Config.APIKey = config.NewSecret("fake-key")
	s := scripted(t, "openai", []eval.Response{finalResponse("openai", "DONE")}, eval.Expectation{EndReason: "budget"}, eval.Limits{}, []string{"lookup"})
	report, err := eval.Run(context.Background(), s, eval.Options{Getenv: getenv, Mode: "live", Profiles: []eval.Profile{p}, CaseIDs: []string{"synthetic"}, MaxUSD: 1, MaxSteps: 3, MaxOutputTokens: 17, Repeat: 2})
	if err == nil || len(report.Results) != 1 {
		t.Fatalf("overshoot must return partial report and error: %+v %v", report, err)
	}
	r := report.Results[0]
	if r.Passed || r.Reason != "spend_cap_exceeded" || r.EndReason != "budget" || r.TokensIn != 9000000 || math.Abs(r.CostUSD-9.000004) > 1e-12 || calls != 1 || len(r.ToolCalls) != 0 {
		t.Fatalf("lost accounting or continued after overshoot: %+v calls=%d", r, calls)
	}
}

func TestLiveGlobalSpendAdmissionAcrossCasesProfilesRepeatsAndRetry(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(map[bool]string{false: "matrix", true: "retry"}[retry], func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if retry {
					w.WriteHeader(429)
				}
				_, _ = w.Write([]byte(strings.Replace(string(finalResponse("openai", "DONE").Body), `"prompt_tokens":10`, `"prompt_tokens":1000`, 1)))
			}))
			defer server.Close()
			getenv := func(name string) string {
				if name == "EVAL_GLOBAL_KEY" {
					return "fake-global-key"
				}
				return ""
			}
			p := profile("openai")
			p.Config.BaseURL = server.URL
			p.Config.APIKeyEnv = "EVAL_GLOBAL_KEY"
			p.Config.APIKey = config.NewSecret("fake-global-key")
			second := p
			second.ID = "second-profile"
			second.Config.Model = "second-model"
			s := scripted(t, "openai", []eval.Response{finalResponse("openai", "DONE")}, eval.Expectation{EndReason: "completed"}, eval.Limits{}, nil)
			other := s.Cases[0]
			other.ID = "second-case"
			s.Cases = append(s.Cases, other)
			opts := eval.Options{Getenv: getenv, Mode: "live", Profiles: []eval.Profile{p, second}, CaseIDs: []string{"synthetic", "second-case"}, MaxUSD: 0.0017, MaxSteps: 3, MaxOutputTokens: 17, Repeat: 2}
			if retry {
				s.Cases = s.Cases[:1]
				s.Cases[0].Expect.EndReason = "budget"
				opts.CaseIDs = []string{"synthetic"}
				opts.Profiles = opts.Profiles[:1]
				opts.Repeat = 1
			}
			report, err := eval.Run(context.Background(), s, opts)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || report.Results[0].TokensIn != 1000 || math.Abs(report.Results[0].CostUSD-0.001004) > 1e-15 {
				t.Fatalf("global admission/accounting: %+v calls=%d", report, calls)
			}
			if retry {
				if !report.Results[0].Passed || report.Results[0].EndReason != "budget" || report.Results[0].Steps != 0 {
					t.Fatalf("retry bypassed admission: %+v", report)
				}
			} else {
				if len(report.Results) != 8 || !report.Results[0].Passed {
					t.Fatalf("matrix result: %+v", report)
				}
				for _, r := range report.Results[1:] {
					if r.EndReason != "budget" || r.TokensIn != 0 || r.Passed {
						t.Fatalf("cap reset across case/profile/repeat: %+v", r)
					}
				}
			}
		})
	}
}

func TestRunDefaultProfilesAndDeterministicRubrics(t *testing.T) {
	s := scripted(t, "openai", []eval.Response{finalResponse("openai", "hello 世界")}, eval.Expectation{EndReason: "completed", RequiredSubstrings: []string{"世界"}, ForbiddenSubstrings: []string{"BAD"}}, eval.Limits{}, nil)
	s.Cases[0].Responses["anthropic"] = []eval.Response{finalResponse("anthropic", "hello 世界")}
	max := 8
	s.Cases[0].Expect.MaxRunes = &max
	r, err := eval.Run(context.Background(), s, eval.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Results) != 2 || !r.Results[0].Passed || !r.Results[1].Passed || r.Results[0].Protocol != "anthropic" || r.Results[1].Protocol != "openai" {
		t.Fatalf("default profiles: %+v", r)
	}
	for _, tt := range []struct {
		expect eval.Expectation
		reason string
	}{
		{eval.Expectation{EndReason: "completed", RequiredSubstrings: []string{"absent"}}, "required_substring_missing"},
		{eval.Expectation{EndReason: "completed", ForbiddenSubstrings: []string{"世界"}}, "forbidden_substring_present"},
		{eval.Expectation{EndReason: "budget"}, "end_reason_mismatch"},
		{eval.Expectation{EndReason: "completed", MaxRunes: newInt(7)}, "max_runes_exceeded"},
	} {
		s.Cases[0].Expect = tt.expect
		report, err := eval.Run(context.Background(), s, eval.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if report.Results[0].Passed || report.Results[0].Reason != tt.reason {
			t.Fatalf("rubric false pass: %+v", report)
		}
	}
}
func newInt(n int) *int { return &n }

func TestLiveRequiresExplicitInputsAndEnvironmentCredentials(t *testing.T) {
	s, err := loadJSON(t, minimalSuite)
	if err != nil {
		t.Fatal(err)
	}
	p := profile("openai")
	for _, opts := range []eval.Options{{Mode: "unknown"}, {Repeat: -1}, {MaxUSD: math.NaN()}, {MaxUSD: math.Inf(1)}, {MaxSteps: -1}, {MaxOutputTokens: -1}, {CaseIDs: []string{"absent"}}, {CaseIDs: []string{"answer", "answer"}}, {Mode: "live"}, {Mode: "live", Profiles: []eval.Profile{p}, CaseIDs: []string{"answer"}, MaxUSD: 1, MaxSteps: 1, MaxOutputTokens: 10}, {Profiles: []eval.Profile{p, p}}} {
		if _, err := eval.Run(context.Background(), s, opts); err == nil {
			t.Fatalf("accepted invalid options: %+v", opts)
		}
	}
}

func TestLiveUsesConfiguredProtocolAndCapsWithFakeServers(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				limit := "max_completion_tokens"
				if protocol == "anthropic" {
					limit = "max_tokens"
					if r.Header.Get("x-api-key") != "fake-environment-only-key" || r.URL.Path != "/v1/messages" {
						t.Error("wrong native endpoint/authorization")
					}
				} else if r.Header.Get("Authorization") != "Bearer fake-environment-only-key" || r.URL.Path != "/chat/completions" {
					t.Error("wrong openai endpoint/authorization")
				}
				if body["model"] != "arbitrary-model" || body[limit] != float64(17) {
					t.Errorf("wrong model/output cap: %v", body)
				}
				if protocol == "openai" && body["reasoning_effort"] != "none" {
					t.Errorf("openai reasoning_effort not forwarded: %v", body)
				}
				if protocol == "anthropic" {
					if _, present := body["reasoning_effort"]; present {
						t.Error("anthropic body carried reasoning_effort")
					}
				}
				_, _ = w.Write(finalResponse(protocol, "DONE").Body)
			}))
			defer server.Close()
			getenv := func(name string) string {
				if name == "EVAL_FAKE_KEY" {
					return "fake-environment-only-key"
				}
				return ""
			}
			p := profile(protocol)
			p.Config.BaseURL = server.URL
			p.Config.APIKeyEnv = "EVAL_FAKE_KEY"
			p.Config.APIKey = config.NewSecret("fake-environment-only-key")
			if protocol == "openai" {
				p.Config.ReasoningEffort = "none"
			}
			s := scripted(t, protocol, []eval.Response{{Error: "transport"}}, eval.Expectation{EndReason: "completed"}, eval.Limits{}, nil)
			opts := eval.Options{Getenv: getenv, Mode: "live", Profiles: []eval.Profile{p}, CaseIDs: []string{"synthetic"}, MaxUSD: 1, MaxSteps: 3, MaxOutputTokens: 17}
			report, err := eval.Run(context.Background(), s, opts)
			if err != nil {
				t.Fatal(err)
			}
			if !report.Results[0].Passed || calls != 1 || report.Results[0].TokensIn != 10 {
				t.Fatalf("live fake result: %+v calls=%d", report, calls)
			}
			opts.MaxUSD = 1e-12
			s.Cases[0].Expect.EndReason = "budget"
			report, err = eval.Run(context.Background(), s, opts)
			if err != nil {
				t.Fatal(err)
			}
			if !report.Results[0].Passed || calls != 1 || report.Results[0].TokensIn != 0 {
				t.Fatalf("cap did not prevent request: %+v calls=%d", report, calls)
			}
		})
	}
}

func TestRunDoesNotExportFixtureTextOrCredentials(t *testing.T) {
	suite, err := loadJSON(t, strings.ReplaceAll(minimalSuite, "DONE", "planted-secret @stranger-username"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := eval.Run(context.Background(), suite, eval.Options{Profiles: []eval.Profile{profile("openai")}})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(report)
	if strings.Contains(string(data), "planted-secret") || strings.Contains(string(data), "stranger-username") {
		t.Fatal("report contains fixture content or secret")
	}
}
