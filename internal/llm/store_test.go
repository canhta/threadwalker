package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/store"
)

func TestLoopPersistsLedgerAndWakeTotals(t *testing.T) {
	ctx := context.Background()
	ledger, err := store.Open(ctx, filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	inserted, err := ledger.DB().ExecContext(ctx, `INSERT INTO wakes(agent_id,started_at) VALUES(?,?)`, "agent", "2026-11-11T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	wakeID, err := inserted.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/tool-loop.json")
	if err != nil {
		t.Fatal(err)
	}
	var responses []json.RawMessage
	if err = json.Unmarshal(raw, &responses); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(responses[requests]); requests++ }))
	defer server.Close()
	client, err := llm.New(config.LLMConfig{BaseURL: server.URL, Model: "test", APIKey: config.NewSecret("key"), PricePerMTokInUSD: 1, PricePerMTokOutUSD: 2}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, ledger, llm.Options{Now: func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Run(ctx, "agent", wakeID, []llm.Message{{Role: "user", Content: "go"}}, []llm.Tool{{Type: "function", Function: llm.Function{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}}}, llm.DispatchFunc(func(context.Context, llm.ToolCall) (string, error) { return "ok", nil }))
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "done" || result.Steps != 3 {
		t.Fatalf("result: %+v", result)
	}
	daily, err := ledger.LLMUsageForDay(ctx, "agent", "2026-11-11")
	if err != nil {
		t.Fatal(err)
	}
	var wake llm.Usage
	if err := ledger.DB().QueryRowContext(ctx, `SELECT tokens_in,tokens_out,cost_usd FROM wakes WHERE id=?`, wakeID).Scan(&wake.TokensIn, &wake.TokensOut, &wake.CostUSD); err != nil {
		t.Fatal(err)
	}
	for _, got := range []llm.Usage{daily, wake} {
		if got.TokensIn != 450 || got.TokensOut != 35 || math.Abs(got.CostUSD-0.00052) > 1e-12 {
			t.Fatalf("reported usage sum differs: %+v", got)
		}
	}
}

func TestCancelledWakeStillAccountsCompletedResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ledger, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	row, err := ledger.DB().Exec(`INSERT INTO wakes(agent_id,started_at) VALUES(?,?)`, "agent", "2026-11-11")
	if err != nil {
		t.Fatal(err)
	}
	wakeID, err := row.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	client, err := llm.New(config.LLMConfig{BaseURL: "https://example.test/v1", Model: "test", APIKey: config.NewSecret("key"), PricePerMTokInUSD: 1}, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 1}, ledger, llm.Options{Now: func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }, HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: closeHookBody{Reader: strings.NewReader(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call","type":"function","function":{"name":"echo","arguments":"{}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`), close: cancel}}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err = client.Run(ctx, "agent", wakeID, []llm.Message{{Role: "user", Content: "go"}}, []llm.Tool{{Type: "function", Function: llm.Function{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}}}, refusing(&calls, "ok", "echo"))
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	got, err := ledger.LLMUsageForDay(context.Background(), "agent", "2026-11-11")
	if err != nil {
		t.Fatal(err)
	}
	if got.TokensIn != 10 || got.TokensOut != 2 || math.Abs(got.CostUSD-0.00001) > 1e-12 {
		t.Fatalf("completed response usage lost: %+v", got)
	}
}

type closeHookBody struct {
	io.Reader
	close func()
}

func (b closeHookBody) Close() error { b.close(); return nil }
