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

	"github.com/canhta/threadwalker/internal/config"
)

func TestCapturedIsolationEventsAreRedactedAndScoped(t *testing.T) {
	suite, err := LoadSuite("testdata/isolation-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var trace bytes.Buffer
	for _, c := range suite.Cases {
		if c.IsolationScenario == "logical_soak" {
			continue
		}
		result := runIsolationCaptured(context.Background(), c, Profile{ID: "synthetic-isolation-trace", ProviderID: "synthetic", Config: config.LLMConfig{Protocol: "openai", Model: "synthetic"}}, 1, Options{Now: func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }}, &trace)
		if !result.Passed {
			t.Fatalf("%s: %s", c.ID, result.Reason)
		}
	}
	var normalized bytes.Buffer
	scanner := bufio.NewScanner(&trace)
	scopes := map[string]int{}
	reasons := map[string]bool{}
	for scanner.Scan() {
		var event map[string]any
		if err = json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		id, _ := event["agent_id"].(string)
		if id != "synthetic-isolation-a" && id != "synthetic-isolation-b" {
			t.Fatalf("foreign correlation ID: %q", id)
		}
		scopes[id]++
		reason, _ := event["reason"].(string)
		reasons[reason] = true
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
	if len(scopes) != 2 || !reasons["own_account"] || !reasons["shared_promotion_link"] || !reasons["budget"] {
		t.Fatalf("missing actual boundary events: %v %v", scopes, reasons)
	}
	for _, private := range []string{"PRIVATE_ISOLATION", "fixture_account", "fixture_stranger", "offline-dummy-key", "https://", "Synthetic permitted reply"} {
		if strings.Contains(normalized.String(), private) {
			t.Fatalf("payload in trace: %s", private)
		}
	}
	path := "testdata/isolation-v1-events.jsonl"
	if os.Getenv("THREADWALKER_UPDATE_ISOLATION_TRACE") == "1" {
		if err = os.WriteFile(path, normalized.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(saved, normalized.Bytes()) {
		t.Fatal("captured isolation event contract changed")
	}
}

func TestIsolationScenariosRejectMixedUnknownAndLiveInputs(t *testing.T) {
	suite, err := LoadSuite("testdata/isolation-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"unknown", "data"} {
		copy := *suite
		copy.Cases = append([]Case(nil), suite.Cases...)
		copy.Cases[0].IsolationScenario = scenario
		if scenario == "data" {
			copy.Cases[0].DashboardScenario = "privacy"
		}
		if _, err = Run(context.Background(), &copy, Options{}); err == nil {
			t.Fatal("invalid component scenario accepted")
		}
	}
	profile := Profile{ID: "synthetic-live", ProviderID: "synthetic", Config: config.LLMConfig{Protocol: "openai", Model: "synthetic", PricePerMTokInUSD: 1, PricePerMTokOutUSD: 2}}
	if _, err = Run(context.Background(), suite, Options{Mode: "live", Profiles: []Profile{profile}, CaseIDs: []string{suite.Cases[0].ID}, MaxUSD: 1, MaxSteps: 1, MaxOutputTokens: 1}); err == nil {
		t.Fatal("live isolation accepted")
	}
}
