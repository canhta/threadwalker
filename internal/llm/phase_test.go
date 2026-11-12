package llm_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/llm"
)

// phaseLedger exposes per-phase spend, as the SQLite store does.
type phaseLedger struct {
	daily llm.Usage
	dream llm.Usage
	agent llm.Usage
	study llm.Usage
	reads int
}

func (l *phaseLedger) LLMUsageForDay(context.Context, string, string) (llm.Usage, error) {
	l.reads++
	return l.daily, nil
}

func (l *phaseLedger) LLMUsageForDayPhases(_ context.Context, _, _ string, phases ...string) (llm.Usage, error) {
	l.reads++
	out := llm.Usage{}
	for _, phase := range phases {
		switch phase {
		case "dream":
			out = addUsage(out, l.dream)
		case "agent":
			out = addUsage(out, l.agent)
		case "study":
			out = addUsage(out, l.study)
		}
	}
	return out, nil
}

func addUsage(a, b llm.Usage) llm.Usage {
	return llm.Usage{TokensIn: a.TokensIn + b.TokensIn, TokensOut: a.TokensOut + b.TokensOut, CostUSD: a.CostUSD + b.CostUSD}
}

func (l *phaseLedger) RecordLLMUsage(context.Context, string, int64, string, llm.Usage) error {
	return nil
}

func phaseClient(t *testing.T, requests *int, ledger llm.Ledger, opts llm.Options) *llm.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		(*requests)++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	t.Cleanup(server.Close)
	opts.Now = func() time.Time { return time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC) }
	client, err := llm.New(config.LLMConfig{BaseURL: server.URL + "/v1", Model: "test", APIKey: config.NewSecret("key"), PricePerMTokInUSD: 1, PricePerMTokOutUSD: 1}, config.LimitsConfig{MaxStepsPerWake: 4, MaxUSDPerDay: 1}, ledger, opts)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestDreamSpendIsBoundedByItsOwnCapAndExcludedFromDailyCap(t *testing.T) {
	ledger := &phaseLedger{agent: llm.Usage{CostUSD: 0.9}, dream: llm.Usage{CostUSD: 0.5}, daily: llm.Usage{CostUSD: 1.4}}
	requests := 0
	client := phaseClient(t, &requests, ledger, llm.Options{Phase: "dream", CapUSD: 0.5})
	if _, err := client.Run(context.Background(), "agent", 1, nil, nil, nil); !errors.Is(err, llm.ErrBudget) || requests != 0 {
		t.Fatalf("dream cap: err=%v requests=%d", err, requests)
	}
	// The same ledger still allows a normal wake: dream spend is not daily spend.
	requests = 0
	client = phaseClient(t, &requests, ledger, llm.Options{Phase: "agent"})
	if _, err := client.Run(context.Background(), "agent", 1, nil, nil, nil); err != nil || requests != 1 {
		t.Fatalf("agent wake: err=%v requests=%d", err, requests)
	}
	// Study counts toward the same daily cap.
	ledger.agent = llm.Usage{CostUSD: 0.8}
	ledger.study = llm.Usage{CostUSD: 0.2}
	requests = 0
	client = phaseClient(t, &requests, ledger, llm.Options{Phase: "study"})
	if _, err := client.Run(context.Background(), "agent", 1, nil, nil, nil); !errors.Is(err, llm.ErrBudget) || requests != 0 {
		t.Fatalf("study daily cap: err=%v requests=%d", err, requests)
	}
}

func TestDreamRequiresPhaseAccountingAndPositiveCap(t *testing.T) {
	requests := 0
	if _, err := llm.New(config.LLMConfig{BaseURL: "http://127.0.0.1:1", Model: "m", APIKey: config.NewSecret("k")}, config.LimitsConfig{MaxStepsPerWake: 1, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{Phase: "dream"}); err == nil {
		t.Fatal("dream without a cap accepted")
	}
	client := phaseClient(t, &requests, &memoryLedger{}, llm.Options{Phase: "dream", CapUSD: 0.1})
	if _, err := client.Run(context.Background(), "agent", 1, nil, nil, nil); err == nil {
		t.Fatal("dream without phase accounting accepted")
	}
}

func TestDreamStepLimitOverride(t *testing.T) {
	ledger := &phaseLedger{}
	requests := 0
	client := phaseClient(t, &requests, ledger, llm.Options{Phase: "dream", CapUSD: 0.30, MaxSteps: 1})
	if _, err := client.Run(context.Background(), "agent", 1, nil, nil, nil); err != nil {
		t.Fatalf("single-step dream: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests=%d", requests)
	}
}
