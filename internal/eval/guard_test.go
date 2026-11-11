package eval

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"github.com/canhta/threadwalker/internal/config"
	"os"
	"strings"
	"testing"
	"time"
)

func TestGuardPolicyVersionedComponentSuite(t *testing.T) {
	suite, err := LoadSuite("testdata/guard-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), suite, Options{Repeat: 2, Revision: "synthetic-guard-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) < 40 {
		t.Fatalf("missing guard component cases %d", len(report.Results))
	}
	for _, result := range report.Results {
		if !result.Passed {
			t.Errorf("%s/%s: %s", result.CaseID, result.ProfileID, result.Reason)
		}
	}
}

func TestCapturedGuardPolicyEvents(t *testing.T) {
	suite, err := LoadSuite("testdata/guard-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture bytes.Buffer
	selected := map[string]bool{"guard_contact_exact_7day": true, "guard_reply_foreign_mention": true, "guard_stop_vietnamese": true, "guard_killed_every_tool": true, "guard_dispatcher_no_bypass": true}
	opts := Options{Now: func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }}
	for _, c := range suite.Cases {
		if !selected[c.ID] {
			continue
		}
		result := runGuardCase(context.Background(), c, Profile{ID: "synthetic-guard-trace", ProviderID: "synthetic", Config: config.LLMConfig{Protocol: "openai", Model: "synthetic"}}, 1, opts, &capture)
		if !result.Passed {
			t.Fatalf("trace %s: %s", c.ID, result.Reason)
		}
	}
	var normalized bytes.Buffer
	scanner := bufio.NewScanner(&capture)
	for scanner.Scan() {
		var event map[string]any
		if err = json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		delete(event, "time")
		b, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		normalized.Write(b)
		normalized.WriteByte('\n')
	}
	if err = scanner.Err(); err != nil {
		t.Fatal(err)
	}
	path := "testdata/guard-v1-events.jsonl"
	if os.Getenv("THREADWALKER_UPDATE_GUARD_TRACE") == "1" {
		if err = os.WriteFile(path, normalized.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(saved, normalized.Bytes()) {
		t.Fatal("captured guard events changed; inspect then explicitly update trace")
	}
	for _, v := range []string{"fixture_author", "ĐỪNG", "@third_author", "https://", "offline-dummy-key"} {
		if strings.Contains(string(saved), v) {
			t.Fatal("payload in captured guard evidence")
		}
	}
	for _, v := range []string{`"event":"guard.checked"`, `"event":"tool.blocked"`, `"reason":"replies_per_user_7d"`, `"reason":"stop_request"`, `"reason":"agent_not_running"`} {
		if !strings.Contains(string(saved), v) {
			t.Fatalf("missing trace evidence %s", v)
		}
	}
}

func TestGuardComponentRejectsInvalidContractsAndLiveMode(t *testing.T) {
	suite, err := LoadSuite("testdata/guard-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Run(context.Background(), suite, Options{Mode: "live", Profiles: []Profile{{ID: "fixture"}}, CaseIDs: []string{suite.Cases[0].ID}, MaxUSD: 1, MaxSteps: 1, MaxOutputTokens: 1})
	if err == nil {
		t.Fatal("guard component accepted live mode")
	}
	for _, mutate := range []func(*Case){func(c *Case) { c.Guard.PostsPerDay = -1 }, func(c *Case) { c.Runtime = &RuntimeCase{} }, func(c *Case) { c.Guard.Steps[0].OffsetNS = -1 }, func(c *Case) { c.Guard.Steps[0].Status = "invalid" }, func(c *Case) { c.Guard.Steps[0].Call.Type = "other" }} {
		s, err := LoadSuite("testdata/guard-v1.json")
		if err != nil {
			t.Fatal(err)
		}
		mutate(&s.Cases[0])
		if _, err = Run(context.Background(), s, Options{}); err == nil {
			t.Fatal("invalid guard component accepted")
		}
	}
}
