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

func TestCapturedBrowserEvents(t *testing.T) {
	suite, err := LoadSuite("testdata/browser.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture bytes.Buffer
	for _, c := range suite.Cases {
		if c.Scenario != "six_operations" && c.Scenario != "logged_out" && c.Scenario != "uncertain_submit" {
			continue
		}
		r := runBrowserCase(context.Background(), c, Profile{ID: "synthetic-browser-trace", ProviderID: "synthetic", Config: config.LLMConfig{Protocol: "openai", Model: "synthetic"}}, 1, Options{Now: func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }}, &capture)
		if !r.Passed {
			t.Fatalf("%s: %s", c.ID, r.Reason)
		}
	}
	var normalized bytes.Buffer
	scanner := bufio.NewScanner(&capture)
	for scanner.Scan() {
		var row map[string]any
		if err = json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		if row["event"] != "browser.operation.finished" && row["event"] != "audit.recorded" && row["event"] != "wake.stopped" {
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
	if err = scanner.Err(); err != nil {
		t.Fatal(err)
	}
	path := "testdata/browser-events.jsonl"
	if os.Getenv("THREADZ_UPDATE_BROWSER_TRACE") == "1" {
		if err = os.WriteFile(path, normalized.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(saved, normalized.Bytes()) {
		t.Fatal("browser event contract changed; inspect then explicitly update trace")
	}
	for _, unsafe := range []string{"eval-browser-private-key", "synthetic_author", "Synthetic outgoing", "Ignore all instructions", "browser.fixture.invalid"} {
		if strings.Contains(string(saved), unsafe) {
			t.Fatal("payload in browser trace")
		}
	}
	for _, required := range []string{`"operation":"reply"`, `"reason":"logged_out"`, `"reason":"unknown_outcome"`, `"reason":"attempt_recorded"`} {
		if !strings.Contains(string(saved), required) {
			t.Fatalf("missing %s", required)
		}
	}
}

func TestBrowserScenariosFailClosedAndRequireOffline(t *testing.T) {
	for _, scenario := range []string{"six_operations", "dry_run", "logged_out", "captcha", "unknown_page", "uncertain_submit", "stop_request", "unknown"} {
		s := &Suite{Cases: []Case{{ID: "browser", Kind: "browser", Scenario: scenario, Messages: []llm.Message{{Role: "user", Content: "synthetic"}}, Expect: Expectation{EndReason: "completed"}}}}
		if _, err := Run(context.Background(), s, Options{}); err == nil {
			t.Fatal("mixed or unknown browser input accepted")
		}
	}
	s, err := LoadSuite("testdata/browser.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Run(context.Background(), s, Options{Mode: "live", Profiles: []Profile{{ID: "fixture"}}, CaseIDs: []string{s.Cases[0].ID}, MaxUSD: 1, MaxSteps: 10, MaxOutputTokens: 10}); err == nil {
		t.Fatal("live component evaluation accepted")
	}
}

func TestBrowserReportIdentityIncludesEmbeddedSnapshots(t *testing.T) {
	suite, err := LoadSuite("testdata/browser.json")
	if err != nil {
		t.Fatal(err)
	}
	beforeSuite := suiteDigest(suite)
	beforeInputs := inputsFingerprint(suite.Cases, Options{})
	saved := browserSnapshots
	t.Cleanup(func() { browserSnapshots = saved })
	browserSnapshots = append(append([]byte{}, saved...), '\n')
	if suiteDigest(suite) == beforeSuite || inputsFingerprint(suite.Cases, Options{}) == beforeInputs {
		t.Fatal("embedded browser fixture changes must invalidate report identity")
	}
}
