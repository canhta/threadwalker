package llm_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/llm"
	"github.com/canhta/threadwalker/internal/logging"
)

// TestSyntheticRetryFailureTrace reproduces the saved operational trace entirely
// offline. Only handler timestamps are normalized; client durations use Now.
func TestSyntheticRetryFailureTrace(t *testing.T) {
	var output bytes.Buffer
	logger, err := logging.New(&output, "debug", "json")
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(503)
			w.Write([]byte(`{"error":"fake-private-provider-response","usage":{"prompt_tokens":100,"completion_tokens":10}}`))
			return
		}
		w.Write([]byte(`{"choices":[],"usage":{"prompt_tokens":200,"completion_tokens":20}}`))
	}))
	defer server.Close()
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	ledger := &memoryLedger{}
	client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "synthetic-model", Protocol: "openai", APIKey: config.NewSecret("fake-secret"), PricePerMTokInUSD: 1, PricePerMTokOutUSD: 2}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, ledger, llm.Options{Logger: logger, ProviderID: "synthetic-provider", ProfileID: "synthetic-profile", Now: func() time.Time { now = now.Add(time.Millisecond); return now }, Sleep: func(context.Context, time.Duration) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Run(context.Background(), "synthetic-agent", 42, []llm.Message{{Role: "system", Content: "fake-private-prompt"}, {Role: "user", Content: "fake-private-notes @fake-third-party-user"}}, nil, nil)
	if err == nil || requests != 2 || ledger.usage.TokensIn != 300 || ledger.usage.TokensOut != 30 || ledger.usage.CostUSD != 0.00036 {
		t.Fatal("synthetic accounting/stop behavior changed")
	}
	var normalized bytes.Buffer
	scanner := bufio.NewScanner(&output)
	records := 0
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if record["agent_id"] != "synthetic-agent" || record["wake_id"] != float64(42) {
			t.Fatal("uncorrelated trace")
		}
		record["time"] = "2026-11-11T00:00:00Z"
		if err := json.NewEncoder(&normalized).Encode(record); err != nil {
			t.Fatal(err)
		}
		records++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if records != 9 {
		t.Fatalf("trace has %d records, want 9", records)
	}
	path := filepath.Join("testdata", "retry-failure-trace.jsonl")
	if os.Getenv("UPDATE_LOGGING_TRACE") == "1" {
		if err := os.WriteFile(path, normalized.Bytes(), 0644); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(saved, normalized.Bytes()) {
		t.Fatal("saved trace differs; inspect events before regenerating with UPDATE_LOGGING_TRACE=1")
	}
}
