package agent_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/agent"
	"github.com/canhta/threadwalker/internal/eval"
)

func TestSavedLogicalSoakEvidenceAndEventRedaction(t *testing.T) {
	root := filepath.Join("..", "..", "docs", "research", "testdata", "runtime")
	raw, err := os.ReadFile(filepath.Join(root, "wakes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var e agent.SoakEvidence
	if err = json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	if !e.Synthetic || e.TimingKind != "logical_time" || e.LogicalHours != 48 || e.CompletedWakes != 48 || e.InterruptedWakes != 1 || e.DryRunActions != 49 || e.Restarts != 1 || len(e.Wakes) != 49 {
		t.Fatalf("incorrect evidence labels or counts: %+v", e)
	}
	first, _ := time.Parse(time.RFC3339Nano, e.Wakes[0].StartedAt)
	last, _ := time.Parse(time.RFC3339Nano, e.Wakes[len(e.Wakes)-1].StartedAt)
	if last.Sub(first) < 48*time.Hour {
		t.Fatal("less than 48 logical hours")
	}
	seen := map[int64]bool{}
	interrupted := 0
	for _, w := range e.Wakes {
		if seen[w.ID] || w.EndedAt == "" {
			t.Fatal("replayed or open wake in saved evidence")
		}
		seen[w.ID] = true
		if w.EndReason == "interrupted" {
			interrupted++
		}
	}
	if interrupted != 1 {
		t.Fatal("restart evidence missing")
	}
	report, err := eval.ReadReport(filepath.Join(root, "eval.json"))
	if err != nil {
		t.Fatal(err)
	}
	if report.Mode != "offline" || len(report.Results) != 32 {
		t.Fatal("missing repeated offline evals")
	}
	for _, c := range report.Results {
		if !c.Passed {
			t.Fatal("saved evaluation failed")
		}
	}
	allowed := map[string]bool{}
	for _, k := range strings.Fields("time level msg event operation outcome reason agent_id provider_id profile_id model protocol tool_call_id tool wake_id request attempt http_status duration_ms steps tokens_in tokens_out cost_usd") {
		allowed[k] = true
	}
	events := map[string]bool{}
	for _, name := range []string{"smoke.jsonl", "events-excerpt.jsonl"} {
		f, err := os.Open(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			for _, secret := range []string{"offline-dummy-key", "Synthetic note for the next wake.", "Synthetic local dry-run action.", "https://", "target_username", "args_json", "self_note"} {
				if strings.Contains(line, secret) {
					f.Close()
					t.Fatalf("payload in event evidence: %s", secret)
				}
			}
			var record map[string]any
			if json.Unmarshal(scanner.Bytes(), &record) != nil {
				f.Close()
				t.Fatal("invalid event evidence")
			}
			for key := range record {
				if !allowed[key] {
					t.Errorf("non-allowlisted event field %q", key)
				}
			}
			if event, ok := record["event"].(string); ok {
				events[event] = true
			}
		}
		err = scanner.Err()
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, event := range []string{"wake.started", "wake.finished", "wake.stopped", "scheduler.scheduled", "context.built", "tool.started", "tool.finished", "guard.checked", "audit.recorded", "note.written", "trends.fetched"} {
		if !events[event] {
			t.Errorf("missing evidence event %s", event)
		}
	}
}
