package eval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"strings"
)

// LoadSuite rejects unknown/duplicate fields and malformed contracts without
// reflecting fixture content into diagnostics. Responses are intentionally raw:
// malformed provider payloads belong in body_raw.
func LoadSuite(path string) (*Suite, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("reading eval suite: unavailable file")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil || len(data) > 16<<20 {
		return nil, errors.New("reading eval suite: unreadable or oversized file")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := uniqueJSON(dec); err != nil {
		return nil, errors.New("parsing eval suite: invalid or duplicate JSON")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("parsing eval suite: trailing JSON")
	}
	dec = json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var suite Suite
	if err := dec.Decode(&suite); err != nil {
		return nil, errors.New("parsing eval suite: invalid fields or types")
	}
	// Distinguish absent limits from explicitly invalid zero/null values.
	var raw struct {
		Cases []struct {
			Limits map[string]json.RawMessage `json:"limits"`
		} `json:"cases"`
	}
	_ = json.Unmarshal(data, &raw)
	for _, c := range raw.Cases {
		for _, value := range c.Limits {
			var n float64
			if json.Unmarshal(value, &n) != nil || n <= 0 {
				return nil, errors.New("validating eval suite: limits must be positive")
			}
		}
	}
	if err := validateSuite(&suite); err != nil {
		return nil, err
	}
	suite.Digest = suiteDigest(&suite)
	return &suite, nil
}

func uniqueJSON(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return errors.New("duplicate key")
			}
			seen[s] = true
			if err := uniqueJSON(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueJSON(d); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected delimiter")
	}
	_, err = d.Token()
	return err
}

func validateSuite(s *Suite) error {
	invalid := func() error {
		return errors.New("validating eval suite: invalid version, case, script, expectation or limits")
	}
	if s == nil || s.Version != "v1" || len(s.Cases) == 0 {
		return invalid()
	}
	seen := map[string]bool{}
	for _, c := range s.Cases {
		if !safeID(c.ID) || seen[c.ID] || (c.Guard == nil && c.Runtime == nil && c.OwnerScenario == "" && c.BrowserScenario == "" && c.MetricsScenario == "" && c.PlaybookScenario == "" && (len(c.Messages) == 0 || len(c.Responses) == 0)) {
			return invalid()
		}
		seen[c.ID] = true
		switch c.Expect.EndReason {
		case "completed", "step_limit", "budget", "provider_error", "invalid_response", "cancelled", "accounting_error", "persistence_error", "stall_stop", "action_limit", "unknown_outcome", "logged_out", "captcha":
		default:
			return invalid()
		}
		if c.Expect.MaxRunes != nil && *c.Expect.MaxRunes < 0 {
			return invalid()
		}
		if c.Limits.MaxSteps < 0 || c.Limits.MaxOutputTokens < 0 || !finiteNonnegative(c.Limits.MaxUSD) {
			return invalid()
		}
		if c.PlaybookScenario != "" {
			if !validPlaybookScenario(c.PlaybookScenario) || c.MetricsScenario != "" || c.BrowserScenario != "" || c.OwnerScenario != "" || c.Guard != nil || c.Runtime != nil || len(c.Messages) > 0 || len(c.Responses) > 0 || len(c.Tools) > 0 || len(c.ToolOutputs) > 0 {
				return invalid()
			}
			continue
		}
		if c.MetricsScenario != "" {
			switch c.MetricsScenario {
			case "windows", "catchup_restart", "isolation", "cancelled", "missing", "db_failure", "logical_soak":
			default:
				return invalid()
			}
			if c.BrowserScenario != "" || c.OwnerScenario != "" || c.Guard != nil || c.Runtime != nil || len(c.Messages) > 0 || len(c.Responses) > 0 || len(c.Tools) > 0 || len(c.ToolOutputs) > 0 {
				return invalid()
			}
			continue
		}
		if c.BrowserScenario != "" {
			switch c.BrowserScenario {
			case "six_operations", "dry_run", "logged_out", "captcha", "unknown_page", "uncertain_submit", "stop_request":
			default:
				return invalid()
			}
			if c.OwnerScenario != "" || c.Guard != nil || c.Runtime != nil || len(c.Messages) > 0 || len(c.Responses) > 0 || len(c.Tools) > 0 || len(c.ToolOutputs) > 0 {
				return invalid()
			}
			continue
		}
		if c.OwnerScenario != "" {
			switch c.OwnerScenario {
			case "controls", "question", "notices", "delivery_failure":
			default:
				return invalid()
			}
			if c.Guard != nil || c.Runtime != nil || len(c.Messages) > 0 || len(c.Responses) > 0 || len(c.Tools) > 0 || len(c.ToolOutputs) > 0 {
				return invalid()
			}
			continue
		}
		if c.Guard != nil {
			g := c.Guard
			if c.Runtime != nil || len(c.Messages) > 0 || len(c.Responses) > 0 || len(c.Tools) > 0 || len(c.ToolOutputs) > 0 || g.PostsPerDay < 0 || g.RepliesPerDay < 0 || g.RepliesPerUser7d < 0 || g.SpacingSeconds < 0 || g.ExpectedActions < 0 || len(g.Steps) > 100 || len(g.Steps) == 0 {
				return invalid()
			}
			for _, step := range g.Steps {
				if step.OffsetNS < 0 {
					return invalid()
				}
				if step.Status != "" && step.Status != "running" && step.Status != "paused" && step.Status != "killed" {
					return invalid()
				}
				if step.Call != nil && (step.Call.Type != "function" || !safeID(step.Call.Function.Name) || !jsonObject([]byte(step.Call.Function.Arguments))) {
					return invalid()
				}
			}
			continue
		}
		if c.Runtime != nil {
			rt := c.Runtime
			if len(c.Messages) > 0 || len(c.Responses) > 0 || len(c.Tools) > 0 || len(c.ToolOutputs) > 0 || rt.ExpectedNextMinutes < 1 || rt.ExpectedNextMinutes > 1440 || rt.ExpectedActions < 0 || len(rt.Calls) > 30 {
				return invalid()
			}
			for _, call := range rt.Calls {
				if call.Type != "function" || !safeID(call.Function.Name) || !jsonObject([]byte(call.Function.Arguments)) {
					return invalid()
				}
			}
			continue
		}
		names := map[string]bool{}
		for _, t := range c.Tools {
			if t.Type != "function" || !safeID(t.Function.Name) || names[t.Function.Name] || !jsonObject(t.Function.Parameters) {
				return invalid()
			}
			names[t.Function.Name] = true
		}
		for name := range c.ToolOutputs {
			if !names[name] {
				return invalid()
			}
		}
		for _, name := range c.Expect.Tools {
			if !names[name] {
				return invalid()
			}
		}
		for _, m := range c.Messages {
			switch m.Role {
			case "system", "developer", "user", "assistant", "tool":
			default:
				return invalid()
			}
		}
		for protocol, responses := range c.Responses {
			if (protocol != "openai" && protocol != "anthropic") || len(responses) == 0 {
				return invalid()
			}
			for _, r := range responses {
				if r.Error != "" {
					if (r.Error != "timeout" && r.Error != "cancelled" && r.Error != "transport") || r.Status != 0 || len(r.Body) != 0 || r.BodyRaw != nil {
						return invalid()
					}
					continue
				}
				if r.Status < 100 || r.Status > 599 || (len(r.Body) == 0) == (r.BodyRaw == nil) {
					return invalid()
				}
				if len(r.Body) > 0 {
					b := bytes.TrimSpace(r.Body)
					if len(b) == 0 || (b[0] != '{' && b[0] != '[') || !json.Valid(b) {
						return invalid()
					}
				}
			}
		}
	}
	return nil
}
func safeID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.-", r)) {
			return false
		}
	}
	return true
}
func jsonObject(b []byte) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(b, &m) == nil && m != nil
}
func finiteNonnegative(n float64) bool { return n >= 0 && !math.IsInf(n, 0) && !math.IsNaN(n) }
func suiteDigest(s *Suite) string {
	b, _ := json.Marshal(s)
	// UseNumber avoids rounding large token counters in scripted responses.
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var canonical any
	_ = dec.Decode(&canonical)
	b, _ = json.Marshal(canonical)
	if fixture := browserFixtureDigest(s.Cases); fixture != "" {
		b = append(b, []byte("\x00browser-snapshots:"+fixture)...)
	}
	if fixture := playbookFixtureDigest(s.Cases); fixture != "" {
		b = append(b, []byte("\x00playbook:"+fixture)...)
	}
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:])
}
