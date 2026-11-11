//go:build llm_live

package llm_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/llm"
	"github.com/canhta/threadwalker/internal/store"
)

// TestLiveTwoToolLoop is a paid, explicit opt-in spike. Default tests exclude
// this file and stay offline. Its dispatcher has only two fake local tools.
func TestLiveTwoToolLoop(t *testing.T) {
	if os.Getenv("THREADWALKER_LIVE_LLM") != "1" {
		t.Skip("requires explicit live-check authorization")
	}
	key := config.NewSecret(os.Getenv("OPENAI_API_KEY"))
	if key.Expose() == "" {
		t.Fatal("OPENAI_API_KEY is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	now := time.Now().UTC()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	row, err := db.DB().ExecContext(ctx, `INSERT INTO wakes(agent_id,started_at) VALUES(?,?)`, "llm-spike", now.Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	wakeID, err := row.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	ledger := &liveLedger{Store: db}
	cfg := config.LLMConfig{BaseURL: "https://api.openai.com/v1", Model: "gpt-5.6-luna", APIKey: key, PricePerMTokInUSD: 0.20, PricePerMTokOutUSD: 1.20}
	client, err := llm.New(cfg, config.LimitsConfig{MaxStepsPerWake: 3, MaxUSDPerDay: 0.01}, ledger, llm.Options{Now: func() time.Time { return now }, RequestTimeout: 25 * time.Second, MaxCompletionTokens: 256, ReasoningEffort: "none"})
	if err != nil {
		t.Fatal(err)
	}
	tools := []llm.Tool{
		{Type: "function", Function: llm.Function{Name: "lookup_seed", Description: "Return the test seed for a label.", Strict: true, Parameters: json.RawMessage(`{"type":"object","properties":{"label":{"type":"string"}},"required":["label"],"additionalProperties":false}`)}},
		{Type: "function", Function: llm.Function{Name: "append_marker", Description: "Append a marker to the seed returned by lookup_seed.", Strict: true, Parameters: json.RawMessage(`{"type":"object","properties":{"seed":{"type":"string"}},"required":["seed"],"additionalProperties":false}`)}},
	}
	var names []string
	result, err := client.Run(ctx, "llm-spike", wakeID, []llm.Message{{Role: "user", Content: "Perform exactly two tool calls in sequence: first lookup_seed with label demo; after receiving its result, call append_marker with the returned seed. After the second result, reply with exactly DONE. Do not call any tool twice."}}, tools, llm.DispatchFunc(func(ctx context.Context, call llm.ToolCall) (string, error) {
		var args map[string]string
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", err
		}
		names = append(names, call.Function.Name)
		switch call.Function.Name {
		case "lookup_seed":
			if len(names) != 1 || args["label"] != "demo" {
				return "", fmt.Errorf("unexpected seed call")
			}
			return `{"seed":"threadwalker-test-seed"}`, nil
		case "append_marker":
			if len(names) != 2 || args["seed"] != "threadwalker-test-seed" {
				return "", fmt.Errorf("unexpected marker call")
			}
			return `{"value":"threadwalker-test-seed-ok"}`, nil
		default:
			return "", fmt.Errorf("unknown fake tool")
		}
	}))
	// Only safe accounting fields and fake tool names are printed, including on failure.
	for i, u := range ledger.responses {
		t.Logf("response=%d prompt_tokens=%d completion_tokens=%d computed_cost_usd=%.10f", i+1, u.TokensIn, u.TokensOut, u.CostUSD)
	}
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "DONE" || result.Steps != 3 || len(names) != 2 || names[0] != "lookup_seed" || names[1] != "append_marker" {
		t.Fatalf("unexpected live loop: steps=%d tools=%v", result.Steps, names)
	}
	total, err := db.LLMUsageForDay(ctx, "llm-spike", now.Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	var sum llm.Usage
	for _, u := range ledger.responses {
		sum.TokensIn += u.TokensIn
		sum.TokensOut += u.TokensOut
		sum.CostUSD += u.CostUSD
	}
	if total.TokensIn != sum.TokensIn || total.TokensOut != sum.TokensOut || math.Abs(total.CostUSD-sum.CostUSD) > 1e-12 {
		t.Fatal("ledger and provider usage differ")
	}
	t.Logf("date=%s model=%s tools=%v responses=%d steps=%d tokens_in=%d tokens_out=%d ledger_cost_usd=%.10f", now.Format(time.RFC3339), cfg.Model, names, len(ledger.responses), result.Steps, total.TokensIn, total.TokensOut, total.CostUSD)
}

type liveLedger struct {
	*store.Store
	responses []llm.Usage
}

func (l *liveLedger) RecordLLMUsage(ctx context.Context, agentID string, wakeID int64, day string, u llm.Usage) error {
	if err := l.Store.RecordLLMUsage(ctx, agentID, wakeID, day, u); err != nil {
		return err
	}
	l.responses = append(l.responses, u)
	return nil
}
