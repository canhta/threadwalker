package main

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/eval"
)

// The live code path is exercised exclusively against a loopback fake server.
func TestEvalLiveOvershootWritesPartialReportAndStops(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"sensitive-synthetic-response @synthetic_actor"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9000000,"completion_tokens":2}}`)
	}))
	defer server.Close()
	const key = "synthetic-live-key-for-cli-test"
	directory := t.TempDir()
	catalogPath := filepath.Join(directory, "catalog.yaml")
	catalog := fmt.Sprintf(`providers:
  loopback:
    protocol: openai
    base_url: %s
    api_key_env: EVAL_CLI_FAKE_KEY
models:
  chosen:
    provider: loopback
    model: synthetic-live-model
    price_per_mtok_in_usd: 1
    price_per_mtok_out_usd: 2
`, server.URL)
	if err := os.WriteFile(catalogPath, []byte(catalog), 0600); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(directory, "partial.json")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out, errOut bytes.Buffer
	code := run(ctx, []string{"eval", "-live", "-suite", filepath.Join("..", "..", "internal", "eval", "testdata", "comparison.json"), "-llms", catalogPath, "-profiles", "chosen", "-cases", "seeded_provider_regression", "-max-usd", "1", "-max-steps", "3", "-max-output-tokens", "17", "-repeat", "2", "-revision", "fake-live-cli-test", "-out", reportPath}, func(name string) string {
		if name == "EVAL_CLI_FAKE_KEY" {
			return key
		}
		return ""
	}, &out, &errOut)
	if code == 0 {
		t.Fatal("reported usage overshoot did not fail")
	}
	report, err := eval.ReadReport(reportPath)
	if err != nil {
		t.Fatal("partial live report was not saved")
	}
	if calls.Load() != 1 || len(report.Results) != 1 || report.Results[0].Passed || report.Results[0].Reason != "spend_cap_exceeded" || report.Results[0].TokensIn != 9000000 || math.Abs(report.Results[0].CostUSD-9.000004) > 1e-12 {
		t.Fatal("overshoot lost accounting or continued requests")
	}
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	combined := string(raw) + out.String() + errOut.String()
	for _, forbidden := range []string{key, "sensitive-synthetic-response", "synthetic_actor"} {
		if strings.Contains(combined, forbidden) {
			t.Fatal("partial report or diagnostic leaked private data")
		}
	}
}
