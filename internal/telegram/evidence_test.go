package telegram_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canhta/threadwalker/internal/eval"
)

func TestSavedOwnerEvidenceUsesSharedContractAndNoPayload(t *testing.T) {
	root := filepath.Join("..", "..", "docs", "research", "testdata", "telegram")
	report, err := eval.ReadReport(filepath.Join(root, "eval.json"))
	if err != nil {
		t.Fatal(err)
	}
	if report.Mode != "offline" || report.SuiteVersion != "v1" || report.Repeat != 2 || report.Seed != 7 || len(report.Results) != 16 || report.Revision == "" || report.InputsFingerprint == "" || report.ConfigFingerprint == "" {
		t.Fatal("incorrect synthetic owner report contract")
	}
	for _, r := range report.Results {
		if !r.Passed {
			t.Fatalf("failed saved case %s", r.CaseID)
		}
		if r.CaseID == "owner_async_question_next_wake" && (r.TokensIn == 0 || r.TokensOut == 0 || r.CostUSD <= 0 || len(r.ToolCalls) != 2) {
			t.Fatal("owner eval omitted actual guarded wake accounting")
		}
	}
	f, err := os.Open(filepath.Join(root, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	allowed := map[string]bool{}
	for _, name := range strings.Fields("time level msg event operation outcome reason agent_id provider_id profile_id model protocol tool_call_id tool wake_id request attempt http_status duration_ms steps tokens_in tokens_out cost_usd") {
		allowed[name] = true
	}
	found := map[string]bool{}
	for scanner.Scan() {
		line := scanner.Text()
		for _, secret := range []string{"synthetic-private-bot", "offline-dummy-key", "700007", "900009", "synthetic_user", "Synthetic private", "Synthetic answer", "https://", "chat_id", "message_id", "reply_to_message"} {
			if strings.Contains(line, secret) {
				t.Fatalf("payload leaked in saved trace: %s", secret)
			}
		}
		var row map[string]any
		if json.Unmarshal(scanner.Bytes(), &row) != nil {
			t.Fatal("invalid event JSON")
		}
		for key := range row {
			if !allowed[key] {
				t.Fatalf("non-allowlisted field %s", key)
			}
		}
		if event, ok := row["event"].(string); ok {
			found[event] = true
		}
		if op, ok := row["operation"].(string); ok {
			found[op] = true
		}
		if reason, ok := row["reason"].(string); ok {
			found[reason] = true
		}
	}
	if err = scanner.Err(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"telegram.operation.finished", "pause", "resume", "dnc", "status", "question", "answer_saved", "unauthorized_chat", "digest", "notice", "delivered", "delivery_failed", "wake.started", "guard.checked", "audit.recorded"} {
		if !found[name] {
			t.Errorf("missing evidence %s", name)
		}
	}
}
