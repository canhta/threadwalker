package eval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/llm"
)

// Run executes each selected case/profile/repeat with an isolated conversation,
// script cursor and ledger. A failed expectation is a result, not a Go error.
// Invalid run inputs fail the invocation; live reservation overruns return a
// partial report and a safe error after retaining actual reported accounting.
func Run(ctx context.Context, suite *Suite, opts Options) (Report, error) {
	if err := validateSuite(suite); err != nil {
		return Report{}, err
	}
	if opts.Mode == "" {
		opts.Mode = "offline"
	}
	if opts.Repeat == 0 {
		opts.Repeat = 1
	}
	if opts.Revision == "" {
		opts.Revision = "unknown"
	}
	if (opts.Mode != "offline" && opts.Mode != "live") || opts.Repeat < 1 || opts.MaxSteps < 0 || opts.MaxOutputTokens < 0 || !finiteNonnegative(opts.MaxUSD) || !reportText(opts.Revision) {
		return Report{}, errors.New("configuring eval: invalid mode, repeat or limits")
	}
	if opts.Mode == "live" && (len(opts.Profiles) == 0 || len(opts.CaseIDs) == 0 || opts.MaxUSD <= 0 || opts.MaxSteps <= 0 || opts.MaxOutputTokens <= 0) {
		return Report{}, errors.New("configuring live eval: explicit profiles, cases and positive spend/step/output caps required")
	}
	if opts.Mode == "live" {
		for _, c := range suite.Cases {
			if c.Guard != nil {
				return Report{}, errors.New("guard eval requires offline mode")
			}
			if c.Runtime != nil || c.OwnerScenario != "" || c.BrowserScenario != "" || c.MetricsScenario != "" || c.PlaybookScenario != "" || c.DashboardScenario != "" || c.IsolationScenario != "" {
				return Report{}, errors.New("runtime eval requires offline mode")
			}
		}
	}
	profiles := append([]Profile(nil), opts.Profiles...)
	if len(profiles) == 0 {
		for _, protocol := range []string{"openai", "anthropic"} {
			profiles = append(profiles, Profile{ID: "synthetic-" + protocol, ProviderID: "fixture-" + protocol, Config: config.LLMConfig{Protocol: protocol, Model: "synthetic-model", BaseURL: "https://fixture.invalid", PricePerMTokInUSD: 1, PricePerMTokOutUSD: 2}})
		}
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].ID < profiles[j].ID })
	seen := map[string]bool{}
	for i := range profiles {
		p := &profiles[i]
		if p.Config.Protocol == "" {
			p.Config.Protocol = "openai"
		}
		if !reportText(p.ID) || seen[p.ID] || !reportText(p.ProviderID) || !reportText(p.Config.Model) || strings.TrimSpace(p.Config.Model) == "" || (p.Config.Protocol != "openai" && p.Config.Protocol != "anthropic") || !finiteNonnegative(p.Config.PricePerMTokInUSD) || !finiteNonnegative(p.Config.PricePerMTokOutUSD) {
			return Report{}, errors.New("configuring eval: invalid or duplicate profile")
		}
		seen[p.ID] = true
		if opts.Mode == "live" {
			key := os.Getenv(p.Config.APIKeyEnv)
			if p.Config.APIKeyEnv == "" || key == "" || key != p.Config.APIKey.Expose() || p.Config.PricePerMTokInUSD <= 0 || p.Config.PricePerMTokOutUSD <= 0 {
				return Report{}, errors.New("configuring live eval: environment credential and positive configured rates required")
			}
		}
	}
	selected := map[string]bool{}
	for _, id := range opts.CaseIDs {
		if selected[id] {
			return Report{}, errors.New("selecting eval cases: duplicate case")
		}
		selected[id] = true
	}
	cases := make([]Case, 0, len(suite.Cases))
	for _, c := range suite.Cases {
		if len(opts.CaseIDs) == 0 || selected[c.ID] {
			cases = append(cases, c)
			delete(selected, c.ID)
		}
	}
	if len(selected) > 0 {
		return Report{}, errors.New("selecting eval cases: unknown case")
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Unix(0, 0).UTC() }
	}
	report := Report{Version: "v1", SuiteVersion: suite.Version, SuiteDigest: suiteDigest(suite), Mode: opts.Mode, Revision: opts.Revision, Seed: opts.Seed, Repeat: opts.Repeat, TimingKind: "wall_clock", Results: []CaseResult{}}
	report.ConfigFingerprint = fingerprint(profiles, opts)
	report.InputsFingerprint = inputsFingerprint(cases, opts)
	liveBudget := &reservation{cap: opts.MaxUSD}
	for _, c := range cases {
		for _, p := range profiles {
			for repeat := 1; repeat <= opts.Repeat; repeat++ {
				report.Results = append(report.Results, runCase(ctx, c, p, repeat, opts, liveBudget))
				if liveBudget.overrun {
					for _, profile := range profiles {
						if key := profile.Config.APIKey.Expose(); key != "" {
							redactReport(&report, key)
						}
					}
					return report, errors.New("running live eval: configured spend estimate exceeded")
				}
			}
		}
	}
	// Profile labels are operator metadata, never inferred from fixture bodies.
	// Also redact explicitly supplied keys if an operator accidentally reused one
	// as a label or revision. Offline does not resolve environment credentials.
	for _, p := range profiles {
		key := p.Config.APIKey.Expose()
		if key != "" {
			redactReport(&report, key)
		}
	}
	return report, nil
}

func runCase(ctx context.Context, c Case, p Profile, repeat int, opts Options, liveBudget *reservation) CaseResult {
	if c.IsolationScenario != "" {
		return runIsolationCase(ctx, c, p, repeat, opts)
	}
	if c.DashboardScenario != "" {
		return runDashboardCase(ctx, c, p, repeat, opts)
	}
	if c.PlaybookScenario != "" {
		return runPlaybookCase(ctx, c, p, repeat, opts, nil)
	}
	if c.MetricsScenario != "" {
		return runMetricsCase(ctx, c, p, repeat, opts)
	}
	if c.BrowserScenario != "" {
		return runBrowserCase(ctx, c, p, repeat, opts, nil)
	}
	if c.OwnerScenario != "" {
		return runOwnerCase(ctx, c, p, repeat, opts)
	}
	if c.Guard != nil {
		return runGuardCase(ctx, c, p, repeat, opts, nil)
	}
	if c.Runtime != nil {
		return runRuntimeCase(ctx, c, p, repeat, opts)
	}
	started := time.Now()
	r := CaseResult{CaseID: c.ID, ProfileID: p.ID, ProviderID: p.ProviderID, Model: p.Config.Model, Protocol: p.Config.Protocol, Repeat: repeat, ToolCalls: []string{}}
	steps, output, usd := effectiveLimits(opts, c.Limits)
	ledger := &memoryLedger{outputCap: output}
	cfg := p.Config
	script := &scriptTransport{responses: c.Responses[cfg.Protocol]}
	var transport http.RoundTripper = script
	if opts.Mode == "offline" {
		cfg.BaseURL = "https://fixture.invalid"
		cfg.APIKey = config.NewSecret("eval-offline-dummy-key")
	} else {
		t := &cappedTransport{base: http.DefaultTransport, budget: liveBudget, caseBudget: &reservation{cap: usd}, config: cfg, output: output}
		transport = t
		ledger.reconcile = t.reconcile
	}
	if opts.Mode == "offline" && len(script.responses) == 0 {
		r.Reason = "missing_protocol_fixture"
		r.EndReason = "provider_error"
		r.DurationMS = float64(time.Since(started).Nanoseconds()) / 1e6
		return r
	}
	client, err := llm.New(cfg, config.LimitsConfig{MaxStepsPerWake: steps, MaxUSDPerDay: usd}, ledger, llm.Options{HTTPClient: &http.Client{Transport: transport}, Now: opts.Now, MaxCompletionTokens: output, RequestTimeout: 30 * time.Second, Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }, ProfileID: p.ID, ProviderID: p.ProviderID})
	if err != nil {
		r.Reason = "invalid_profile"
		r.EndReason = "provider_error"
		return r
	}
	// This is a fixture-local dispatcher with no production operations or registry.
	dispatcher := llm.DispatchFunc(func(ctx context.Context, call llm.ToolCall) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		r.ToolCalls = append(r.ToolCalls, call.Function.Name)
		return c.ToolOutputs[call.Function.Name], nil
	})
	result, runErr := client.Run(ctx, "synthetic-eval", 1, c.Messages, c.Tools, dispatcher)
	r.Steps = result.Steps
	r.TokensIn = ledger.usage.TokensIn
	r.TokensOut = ledger.usage.TokensOut
	r.CostUSD = ledger.usage.CostUSD
	r.EndReason = endReason(runErr)
	if opts.Mode == "live" {
		if t := transport.(*cappedTransport); t.blocked {
			r.EndReason = "budget"
		}
	}
	if ledger.usage.CostUSD > usd {
		r.EndReason = "budget"
	}
	r.Passed, r.Reason = matches(c.Expect, r, result.Content)
	if script.exhausted && opts.Mode == "offline" {
		r.Passed = false
		r.Reason = "fixture_exhausted"
	}
	if ledger.outputExceeded {
		r.EndReason = "invalid_response"
		r.Passed = false
		r.Reason = "output_limit_exceeded"
	}
	if liveBudget.overrun {
		r.EndReason = "budget"
		r.Passed = false
		r.Reason = "spend_cap_exceeded"
	}
	r.DurationMS = float64(time.Since(started).Nanoseconds()) / 1e6
	return r
}

func matches(e Expectation, r CaseResult, content string) (bool, string) {
	if e.EndReason != r.EndReason {
		return false, "end_reason_mismatch"
	}
	if e.Content != nil && *e.Content != content {
		return false, "content_mismatch"
	}
	if e.Tools != nil {
		if len(e.Tools) != len(r.ToolCalls) {
			return false, "tools_mismatch"
		}
		for i, name := range e.Tools {
			if name != r.ToolCalls[i] {
				return false, "tools_mismatch"
			}
		}
	}
	for _, s := range e.RequiredSubstrings {
		if !strings.Contains(content, s) {
			return false, "required_substring_missing"
		}
	}
	for _, s := range e.ForbiddenSubstrings {
		if strings.Contains(content, s) {
			return false, "forbidden_substring_present"
		}
	}
	if e.MaxRunes != nil && utf8.RuneCountInString(content) > *e.MaxRunes {
		return false, "max_runes_exceeded"
	}
	return true, "matched"
}
func endReason(err error) string {
	switch {
	case err == nil:
		return "completed"
	case errors.Is(err, llm.ErrStepLimit):
		return "step_limit"
	case errors.Is(err, llm.ErrBudget):
		return "budget"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "provider_error"
	}
	// llm deliberately exports safe error strings rather than raw provider text.
	s := err.Error()
	if strings.HasPrefix(s, "accounting ") || strings.HasPrefix(s, "recording LLM usage") || strings.HasPrefix(s, "checking daily LLM budget") {
		return "accounting_error"
	}
	if strings.HasPrefix(s, "reading chat response") || strings.HasPrefix(s, "decoding chat") || strings.HasPrefix(s, "encoding chat") {
		return "invalid_response"
	}
	return "provider_error"
}

type memoryLedger struct {
	usage          llm.Usage
	outputCap      int
	outputExceeded bool
	reconcile      func(llm.Usage) error
}

func (l *memoryLedger) LLMUsageForDay(ctx context.Context, _ string, _ string) (llm.Usage, error) {
	if err := ctx.Err(); err != nil {
		return llm.Usage{}, err
	}
	return l.usage, nil
}
func (l *memoryLedger) RecordLLMUsage(ctx context.Context, _ string, _ int64, _ string, u llm.Usage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if u.TokensIn > math.MaxInt64-l.usage.TokensIn || u.TokensOut > math.MaxInt64-l.usage.TokensOut || !finiteNonnegative(l.usage.CostUSD+u.CostUSD) {
		return errors.New("usage overflow")
	}
	l.usage.TokensIn += u.TokensIn
	l.usage.TokensOut += u.TokensOut
	l.usage.CostUSD += u.CostUSD
	if u.TokensOut > int64(l.outputCap) {
		l.outputExceeded = true
	}
	if l.reconcile != nil {
		if err := l.reconcile(u); err != nil {
			return err
		}
	}
	if l.outputExceeded {
		return errors.New("output limit exceeded")
	}
	return nil
}

type scriptTransport struct {
	responses []Response
	next      int
	exhausted bool
}

func (t *scriptTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	if t.next >= len(t.responses) {
		t.exhausted = true
		return nil, errors.New("fixture exhausted")
	}
	r := t.responses[t.next]
	t.next++
	switch r.Error {
	case "timeout":
		return nil, context.DeadlineExceeded
	case "cancelled":
		return nil, context.Canceled
	case "transport":
		return nil, errors.New("synthetic transport failure")
	}
	body := r.Body
	if r.BodyRaw != nil {
		body = []byte(*r.BodyRaw)
	}
	return &http.Response{StatusCode: r.Status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: req}, nil
}

// Reservations bound configured estimated spend across all live attempts,
// including retries and transport failures of unknown billing status. The
// byte-based input estimate and configured flat prices are not provider billing.
type reservation struct {
	cap, used float64
	overrun   bool
}
type cappedTransport struct {
	base               http.RoundTripper
	budget, caseBudget *reservation
	config             config.LLMConfig
	output             int
	blocked            bool
	pending            float64
}

func (t *cappedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	estimate := (float64(req.ContentLength)+1024)/1e6*t.config.PricePerMTokInUSD + float64(t.output)/1e6*t.config.PricePerMTokOutUSD
	if t.budget.overrun || !finiteNonnegative(estimate) || estimate > t.budget.cap-t.budget.used || estimate > t.caseBudget.cap-t.caseBudget.used {
		t.blocked = true
		return nil, llm.ErrBudget
	}
	t.budget.used += estimate
	t.caseBudget.used += estimate
	t.pending = estimate
	return t.base.RoundTrip(req)
}
func (t *cappedTransport) reconcile(u llm.Usage) error {
	reserved := t.pending
	t.pending = 0
	t.budget.used += u.CostUSD - reserved
	t.caseBudget.used += u.CostUSD - reserved
	if u.CostUSD > reserved || t.budget.used > t.budget.cap || t.caseBudget.used > t.caseBudget.cap {
		t.budget.overrun = true
		return llm.ErrBudget
	}
	return nil
}
func minPositive(fallback int, values ...int) int {
	for _, v := range values {
		if v > 0 && v < fallback {
			fallback = v
		}
	}
	return fallback
}
func minPositiveFloat(fallback float64, values ...float64) float64 {
	for _, v := range values {
		if v > 0 && v < fallback {
			fallback = v
		}
	}
	return fallback
}
func effectiveLimits(opts Options, l Limits) (int, int, float64) {
	steps, output, usd := 16, 1024, 1.0
	if opts.MaxSteps > 0 || l.MaxSteps > 0 {
		steps = minPositive(math.MaxInt, opts.MaxSteps, l.MaxSteps)
	}
	if opts.MaxOutputTokens > 0 || l.MaxOutputTokens > 0 {
		output = minPositive(math.MaxInt, opts.MaxOutputTokens, l.MaxOutputTokens)
	}
	if opts.MaxUSD > 0 || l.MaxUSD > 0 {
		usd = minPositiveFloat(math.MaxFloat64, opts.MaxUSD, l.MaxUSD)
	}
	return steps, output, usd
}
func inputsFingerprint(cases []Case, opts Options) string {
	type input struct {
		ID              string
		Steps, Output   int
		USD             float64
		BrowserFixture  string `json:",omitempty"`
		PlaybookFixture string `json:",omitempty"`
	}
	selected := make([]input, 0, len(cases))
	for _, c := range cases {
		steps, output, usd := effectiveLimits(opts, c.Limits)
		selected = append(selected, input{c.ID, steps, output, usd, browserFixtureDigest([]Case{c}), playbookFixtureDigest([]Case{c})})
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].ID < selected[j].ID })
	b, _ := json.Marshal(struct {
		Mode                      string
		Seed                      int64
		Repeat                    int
		GlobalSteps, GlobalOutput int
		GlobalUSD                 float64
		Cases                     []input
	}{opts.Mode, opts.Seed, opts.Repeat, opts.MaxSteps, opts.MaxOutputTokens, opts.MaxUSD, selected})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func fingerprint(profiles []Profile, opts Options) string {
	type publicProfile struct {
		ID, Provider, Protocol, Model, Endpoint string
		Input, Output                           float64
	}
	clean := make([]publicProfile, 0, len(profiles))
	for _, p := range profiles {
		clean = append(clean, publicProfile{p.ID, p.ProviderID, p.Config.Protocol, p.Config.Model, p.Config.BaseURL, p.Config.PricePerMTokInUSD, p.Config.PricePerMTokOutUSD})
	}
	b, _ := json.Marshal(struct {
		Profiles      []publicProfile
		Steps, Output int
		USD           float64
	}{clean, opts.MaxSteps, opts.MaxOutputTokens, opts.MaxUSD})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func redactReport(r *Report, key string) {
	clean := func(s string) string { return strings.ReplaceAll(s, key, "[REDACTED]") }
	r.Revision = clean(r.Revision)
	for i := range r.Results {
		c := &r.Results[i]
		c.CaseID = clean(c.CaseID)
		c.ProfileID = clean(c.ProfileID)
		c.ProviderID = clean(c.ProviderID)
		c.Model = clean(c.Model)
		for j := range c.ToolCalls {
			c.ToolCalls[j] = clean(c.ToolCalls[j])
		}
	}
}
