package eval_test

import (
	"context"
	"encoding/json"
	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/eval"
	"github.com/canhta/threadwalker/internal/llm"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeSuiteUsesGuardedToolsAndPersistentWakes(t *testing.T) {
	suite, err := eval.LoadSuite("testdata/runtime-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	report, err := eval.Run(context.Background(), suite, eval.Options{Seed: 1, Revision: "synthetic-test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 16 {
		t.Fatalf("cases=%d", len(report.Results))
	}
	for _, c := range report.Results {
		if !c.Passed {
			t.Errorf("%s/%s: %s (%s)", c.CaseID, c.Protocol, c.Reason, c.EndReason)
		}
	}
	p := filepath.Join(t.TempDir(), "report.json")
	if err = eval.WriteReport(p, report); err != nil {
		t.Fatal(err)
	}
	if _, err = eval.ReadReport(p); err != nil {
		t.Fatal(err)
	}
	_, err = eval.Run(context.Background(), suite, eval.Options{Mode: "live"})
	if err == nil {
		t.Fatal("runtime suite allowed live calls")
	}
}

func TestRuntimeFixtureRejectsAmbiguousOrUnboundedInputs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*eval.Case)
	}{
		{"mixed_messages", func(c *eval.Case) { c.Messages = []llm.Message{{Role: "user", Content: "synthetic"}} }},
		{"mixed_responses", func(c *eval.Case) {
			c.Responses = map[string][]eval.Response{"openai": {{Status: 200, Body: json.RawMessage(`{}`)}}}
		}},
		{"mixed_tools", func(c *eval.Case) { c.Tools = []llm.Tool{{Type: "function"}} }},
		{"mixed_outputs", func(c *eval.Case) { c.ToolOutputs = map[string]string{"read_feed": "synthetic"} }},
		{"zero_schedule", func(c *eval.Case) { c.Runtime.ExpectedNextMinutes = 0 }},
		{"impossible_schedule", func(c *eval.Case) { c.Runtime.ExpectedNextMinutes = 1441 }},
		{"negative_actions", func(c *eval.Case) { c.Runtime.ExpectedActions = -1 }},
		{"unbounded_calls", func(c *eval.Case) {
			for len(c.Runtime.Calls) < 31 {
				c.Runtime.Calls = append(c.Runtime.Calls, c.Runtime.Calls[0])
			}
		}},
		{"invalid_type", func(c *eval.Case) { c.Runtime.Calls[0].Type = "other" }},
		{"invalid_name", func(c *eval.Case) { c.Runtime.Calls[0].Function.Name = "untrusted tool" }},
		{"invalid_arguments", func(c *eval.Case) { c.Runtime.Calls[0].Function.Arguments = `[]` }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			suite, err := eval.LoadSuite("testdata/runtime-v1.json")
			if err != nil {
				t.Fatal(err)
			}
			tc.change(&suite.Cases[0])
			if _, err = eval.Run(context.Background(), suite, eval.Options{}); err == nil {
				t.Fatal("invalid runtime fixture accepted")
			}
		})
	}
	suite, err := eval.LoadSuite("testdata/runtime-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	_, err = eval.Run(context.Background(), suite, eval.Options{Mode: "live", CaseIDs: []string{suite.Cases[0].ID}, Profiles: []eval.Profile{{ID: "synthetic", Config: config.LLMConfig{Model: "synthetic"}}}, MaxUSD: 1, MaxSteps: 10, MaxOutputTokens: 100})
	if err == nil || !strings.Contains(err.Error(), "runtime eval requires offline") {
		t.Fatalf("live runtime rejection=%v", err)
	}
}
