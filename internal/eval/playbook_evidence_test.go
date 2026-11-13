package eval

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
)

func TestCapturedPlaybookEvents(t *testing.T) {
	var capture bytes.Buffer
	for _, scenario := range []string{"outcomes", "revision"} {
		c := Case{ID: "playbook_" + scenario, Kind: "playbook", Scenario: scenario, Expect: Expectation{EndReason: "completed"}}
		r := runPlaybookCase(context.Background(), c, Profile{ID: "synthetic-playbook-trace", ProviderID: "synthetic", Config: config.LLMConfig{Protocol: "openai", Model: "synthetic"}}, 1, Options{Now: func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }}, &capture)
		if !r.Passed {
			t.Fatalf("%s: %s", scenario, r.Reason)
		}
	}
	var normalized bytes.Buffer
	scanner := bufio.NewScanner(&capture)
	for scanner.Scan() {
		var row map[string]any
		if e := json.Unmarshal(scanner.Bytes(), &row); e != nil {
			t.Fatal(e)
		}
		if row["event"] != "playbook.decided" {
			continue
		}
		delete(row, "time")
		raw, e := json.Marshal(row)
		if e != nil {
			t.Fatal(e)
		}
		normalized.Write(raw)
		normalized.WriteByte('\n')
	}
	if e := scanner.Err(); e != nil {
		t.Fatal(e)
	}
	path := "testdata/playbook-events.jsonl"
	if os.Getenv("THREADZ_UPDATE_PLAYBOOK_TRACE") == "1" {
		if e := os.WriteFile(path, normalized.Bytes(), 0600); e != nil {
			t.Fatal(e)
		}
	}
	saved, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(saved, normalized.Bytes()) {
		t.Fatal("playbook event contract changed; inspect and explicitly update trace")
	}
	for _, private := range []string{"PRIVATE", "FOREIGN", "fixture.invalid", "offline-dummy-key"} {
		if strings.Contains(string(saved), private) {
			t.Fatal("playbook payload leak")
		}
	}
	for _, required := range []string{`"event":"playbook.decided"`, `"reason":"positive_evidence"`, `"hypothesis_id":1`, `"n":8`} {
		if !strings.Contains(string(saved), required) {
			t.Fatalf("missing %s", required)
		}
	}
}

func TestPlaybookScenariosFailClosedAndRequireOffline(t *testing.T) {
	for _, scenario := range []string{"mutation", "outcomes", "revision", "unknown"} {
		s := &Suite{Cases: []Case{{ID: "playbook", Kind: "playbook", Scenario: scenario, Messages: []llm.Message{{Role: "user", Content: "mixed"}}, Expect: Expectation{EndReason: "completed"}}}}
		if _, e := Run(context.Background(), s, Options{}); e == nil {
			t.Fatal("mixed/unknown playbook input accepted")
		}
	}
	s := &Suite{Cases: []Case{{ID: "playbook", Kind: "playbook", Scenario: "mutation", Expect: Expectation{EndReason: "completed"}}}}
	if _, e := Run(context.Background(), s, Options{Mode: "live", Profiles: []Profile{{ID: "fixture"}}, CaseIDs: []string{"playbook"}, MaxUSD: 1, MaxSteps: 100, MaxOutputTokens: 100}); e == nil {
		t.Fatal("live component evaluation accepted")
	}
}
