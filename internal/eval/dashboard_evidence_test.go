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
)

func TestCapturedDashboardEvents(t *testing.T) {
	suite, err := LoadSuite("testdata/dashboard-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture bytes.Buffer
	for _, c := range suite.Cases {
		result := runDashboardCaseCaptured(context.Background(), c, Profile{ID: "synthetic-dashboard-trace"}, 1, Options{Now: func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }}, &capture)
		if !result.Passed {
			t.Fatalf("%s: %s", c.ID, result.Reason)
		}
	}
	var normalized bytes.Buffer
	scanner := bufio.NewScanner(&capture)
	for scanner.Scan() {
		var row map[string]any
		if err = json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		delete(row, "time")
		raw, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		normalized.Write(raw)
		normalized.WriteByte('\n')
	}
	if err = scanner.Err(); err != nil {
		t.Fatal(err)
	}
	path := "testdata/dashboard-v1-events.jsonl"
	if os.Getenv("THREADWALKER_UPDATE_DASHBOARD_TRACE") == "1" {
		if err = os.WriteFile(path, normalized.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(saved, normalized.Bytes()) {
		t.Fatal("dashboard event contract changed; inspect and explicitly update trace")
	}
	for _, private := range []string{"foreign_handle", "sk-synthetic-eval-key", "SYSTEM-PROMPT", "private-content", "unselected-agent", "https://", "args_json", "result_json"} {
		if strings.Contains(string(saved), private) {
			t.Fatal("private data in public dashboard trace")
		}
	}
	for _, required := range []string{`"event":"dashboard.request.finished"`, `"reason":"state_unavailable"`, `"reason":"unknown_path"`, `"reason":"method_not_allowed"`, `"reason":"query_not_supported"`, `"http_status":503`} {
		if !strings.Contains(string(saved), required) {
			t.Fatalf("missing %s", required)
		}
	}
}
