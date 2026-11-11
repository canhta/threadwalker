// Package eval runs synthetic, reproducible evaluations through the real LLM client.
package eval

import (
	"encoding/json"
	"time"

	"github.com/canhta/threadwalker/internal/agent"
	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/llm"
	"github.com/canhta/threadwalker/internal/playbook"
)

type Suite struct {
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
	Cases       []Case `json:"cases"`
	Digest      string `json:"-"`
}
type RuntimeCase struct {
	Calls               []llm.ToolCall `json:"calls"`
	Restart             bool           `json:"restart,omitempty"`
	ExpectedNextMinutes int            `json:"expected_next_minutes"`
	ExpectedActions     int            `json:"expected_actions"`
	ExpectedNote        string         `json:"expected_note,omitempty"`
	CheckUntrusted      bool           `json:"check_untrusted,omitempty"`
}

type Case struct {
	DashboardScenario string                `json:"dashboard_scenario,omitempty"`
	IsolationScenario string                `json:"isolation_scenario,omitempty"`
	PlaybookScenario  string                `json:"playbook_scenario,omitempty"`
	MetricsScenario   string                `json:"metrics_scenario,omitempty"`
	BrowserScenario   string                `json:"browser_scenario,omitempty"`
	OwnerScenario     string                `json:"owner_scenario,omitempty"`
	Guard             *GuardCase            `json:"guard,omitempty"`
	Runtime           *RuntimeCase          `json:"runtime,omitempty"`
	ID                string                `json:"id"`
	Description       string                `json:"description,omitempty"`
	Messages          []llm.Message         `json:"messages"`
	Tools             []llm.Tool            `json:"tools,omitempty"`
	Responses         map[string][]Response `json:"responses"`
	ToolOutputs       map[string]string     `json:"tool_outputs,omitempty"`
	Expect            Expectation           `json:"expect"`
	Limits            Limits                `json:"limits,omitempty"`
}
type Response struct {
	Status  int             `json:"status,omitempty"`
	Body    json.RawMessage `json:"body,omitempty"`
	BodyRaw *string         `json:"body_raw,omitempty"`
	Error   string          `json:"error,omitempty"`
}
type Expectation struct {
	EndReason           string   `json:"end_reason"`
	Content             *string  `json:"content,omitempty"`
	Tools               []string `json:"tools"`
	RequiredSubstrings  []string `json:"required_substrings,omitempty"`
	ForbiddenSubstrings []string `json:"forbidden_substrings,omitempty"`
	MaxRunes            *int     `json:"max_runes,omitempty"`
}
type Limits struct {
	MaxSteps        int     `json:"max_steps,omitempty"`
	MaxUSD          float64 `json:"max_usd,omitempty"`
	MaxOutputTokens int     `json:"max_output_tokens,omitempty"`
}
type Profile struct {
	ID         string
	ProviderID string
	Config     config.LLMConfig
}
type Options struct {
	Seed            int64
	Repeat          int
	Profiles        []Profile
	Mode            string
	MaxUSD          float64
	MaxSteps        int
	MaxOutputTokens int
	Revision        string
	CaseIDs         []string
	Now             func() time.Time
}
type Report struct {
	Version           string       `json:"report_version"`
	SuiteVersion      string       `json:"suite_version"`
	SuiteDigest       string       `json:"suite_digest"`
	Mode              string       `json:"mode"`
	Revision          string       `json:"revision"`
	Seed              int64        `json:"seed"`
	Repeat            int          `json:"repeat"`
	ConfigFingerprint string       `json:"config_fingerprint"`
	InputsFingerprint string       `json:"inputs_fingerprint"`
	TimingKind        string       `json:"timing_kind"`
	Results           []CaseResult `json:"results"`
}
type CaseResult struct {
	Agents        []agent.IsolationAgentEvidence `json:"agents,omitempty"`
	Simulation    *playbook.SimulationReport     `json:"simulation,omitempty"`
	CaseID        string                         `json:"case_id"`
	ProfileID     string                         `json:"profile_id"`
	ProviderID    string                         `json:"provider_id"`
	Model         string                         `json:"model"`
	Protocol      string                         `json:"protocol"`
	Repeat        int                            `json:"repeat"`
	Passed        bool                           `json:"passed"`
	Reason        string                         `json:"reason"`
	EndReason     string                         `json:"end_reason"`
	Steps         int                            `json:"steps"`
	ToolCalls     []string                       `json:"tool_calls"`
	TokensIn      int64                          `json:"tokens_in"`
	TokensOut     int64                          `json:"tokens_out"`
	CostUSD       float64                        `json:"cost_usd"`
	DurationMS    float64                        `json:"duration_ms"`
	LogicalHours  int                            `json:"logical_hours,omitempty"`
	MetricSamples int                            `json:"metric_samples,omitempty"`
	MetricTargets int                            `json:"metric_targets,omitempty"`
}
