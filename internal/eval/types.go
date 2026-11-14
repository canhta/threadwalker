// Package eval runs synthetic, reproducible evaluations through the real LLM client.
package eval

import (
	"encoding/json"
	"io"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/quality"
)

type Suite struct {
	Description string `json:"description,omitempty"`
	Cases       []Case `json:"cases"`
	Digest      string `json:"-"`
}
type RuntimeCase struct {
	Calls               []llm.ToolCall `json:"calls"`
	Restart             bool           `json:"restart,omitempty"`
	ExpectedNextMinutes int            `json:"expected_next_minutes"`
	ExpectedActions     int            `json:"expected_actions"`
	CheckUntrusted      bool           `json:"check_untrusted,omitempty"`
	BlockedPhrases      []string       `json:"blocked_phrases,omitempty"`
	ExpectedBlocked     string         `json:"expected_blocked,omitempty"`
}

// QualityCase scores one draft written from a real wake context. Draft and
// Judge are the recorded model outputs an offline run replays; a live run
// ignores them. Fails lists the check ids the scorer must flag.
type QualityCase struct {
	TemplateDir string `json:"template_dir"`
	TemplateID  string `json:"template_id"`
	Kind        string `json:"kind"`
	// Said is what the account already said; the repeat_image check reads it.
	Said        []string `json:"said,omitempty"`
	Draft       string   `json:"draft"`
	Judge       string   `json:"judge"`
	Fails       []string `json:"fails,omitempty"`
	resolvedDir string
}

// Case is one eval case. Kind selects the runner; Scenario picks a variant
// for the kinds that have them.
type Case struct {
	Kind        string                `json:"kind"`
	Scenario    string                `json:"scenario,omitempty"`
	Quality     *QualityCase          `json:"quality,omitempty"`
	Guard       *GuardCase            `json:"guard,omitempty"`
	Runtime     *RuntimeCase          `json:"runtime,omitempty"`
	Replay      *ReplayCase           `json:"replay,omitempty"`
	Briefs      *BriefsCase           `json:"briefs,omitempty"`
	ID          string                `json:"id"`
	Description string                `json:"description,omitempty"`
	Messages    []llm.Message         `json:"messages"`
	Tools       []llm.Tool            `json:"tools,omitempty"`
	Responses   map[string][]Response `json:"responses"`
	ToolOutputs map[string]string     `json:"tool_outputs,omitempty"`
	Expect      Expectation           `json:"expect"`
	Limits      Limits                `json:"limits,omitempty"`
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
	// Getenv resolves the live credential named by a profile.
	Getenv func(string) string
	// BriefReviewer replaces the template's own writer and critic in a briefs run; tests script it.
	BriefReviewer *BriefReviewer
	// BriefsOut receives the plain text report of a briefs run.
	BriefsOut io.Writer
}
type Report struct {
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
	Quality       []quality.Check                `json:"quality,omitempty"`
	Agents        []agent.IsolationAgentEvidence `json:"agents,omitempty"`
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
