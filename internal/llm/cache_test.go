package llm_test

import (
	"context"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
)

type cacheTransport struct {
	body     string
	response string
}

func (t *cacheTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	t.body = string(b)
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(t.response)), Request: r}, nil
}

func TestCachedTokensPricedAndCacheKeySent(t *testing.T) {
	half := 0.5
	const oa = `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":100`
	for _, tc := range []struct {
		name, base, protocol, response string
		cachedRate                     *float64
		wantCached                     int64
		wantCost                       float64
		wantKey                        bool
	}{
		// 1000 in (600 cached), 100 out; in $1, out $2, cached $0.5 per Mtok: 400 + 300 + 200 = 900 micro-USD.
		{"openai cached", "https://api.openai.com/v1", "openai", oa + `,"prompt_tokens_details":{"cached_tokens":600}}}`, &half, 600, 0.0009, true},
		{"openai unset rate", "https://api.openai.com/v1", "openai", oa + `,"prompt_tokens_details":{"cached_tokens":600}}}`, nil, 600, 0.0012, true},
		{"openai no details", "https://api.openai.com/v1", "openai", oa + `}}`, &half, 0, 0.0012, true},
		{"compatible host", "https://compat.example/v1", "openai", oa + `}}`, nil, 0, 0.0012, false},
		{"anthropic cache read", "https://api.anthropic.com", "anthropic", `{"role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":400,"output_tokens":100,"cache_read_input_tokens":600}}`, &half, 600, 0.0009, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &cacheTransport{response: tc.response}
			ledger := &memoryLedger{}
			client, err := llm.New(config.LLMConfig{Protocol: tc.protocol, BaseURL: tc.base, Model: "m", APIKey: config.NewSecret("k"), PricePerMTokInUSD: 1, PricePerMTokOutUSD: 2, PricePerMTokCachedInUSD: tc.cachedRate}, config.LimitsConfig{MaxStepsPerWake: 2, MaxUSDPerDay: 1}, ledger, llm.Options{HTTPClient: &http.Client{Transport: tr}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = client.Run(context.Background(), "agent-1", 1, []llm.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
				t.Fatal(err)
			}
			if ledger.usage.TokensCached != tc.wantCached || math.Abs(ledger.usage.CostUSD-tc.wantCost) > 1e-12 {
				t.Fatalf("usage %+v", ledger.usage)
			}
			if got := strings.Contains(tr.body, `"prompt_cache_key":"agent-1:agent"`); got != tc.wantKey {
				t.Fatalf("prompt_cache_key present=%v body=%s", got, tr.body)
			}
		})
	}
}
