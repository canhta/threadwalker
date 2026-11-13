package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/config"
)

func reviewTemplate(t *testing.T, review string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fixture.agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Replace(string(raw), "\nlimits:", "\n"+review+"limits:", 1)
}

const judgeBlock = "review:\n  llm:\n    protocol: anthropic\n    base_url: https://api.anthropic.com\n    model: %s\n    api_key_env: JUDGE_API_KEY\n    price_per_mtok_in_usd: 1\n    price_per_mtok_out_usd: 5\n"

func judgeEnv() func(string) string {
	base := validEnv()
	return func(k string) string {
		if k == "JUDGE_API_KEY" {
			return "secret-judge-key-value"
		}
		return base(k)
	}
}

func TestReviewLLMIsOptionalAndDecodedLikeLLM(t *testing.T) {
	plain, err := loadOne(writeProfileFile(t, reviewTemplate(t, "")), judgeEnv())
	if err != nil || plain.Review.Enabled {
		t.Fatalf("plain template: review=%+v err=%v", plain.Review, err)
	}
	tmpl, err := loadOne(writeProfileFile(t, reviewTemplate(t, strings.Replace(judgeBlock, "%s", "judge-model", 1))), judgeEnv())
	if err != nil {
		t.Fatal(err)
	}
	r := tmpl.Review
	if !r.Enabled || r.LLM.Protocol != "anthropic" || r.LLM.Model != "judge-model" || r.LLM.APIKey.Expose() != "secret-judge-key-value" || r.LLM.PricePerMTokOutUSD != 5 {
		t.Fatalf("review = %+v", r)
	}
	if got := config.NewRedactor(tmpl).String("key secret-judge-key-value"); strings.Contains(got, "secret-judge-key-value") {
		t.Errorf("judge key not redacted: %s", got)
	}
}

func TestReviewModelMustDifferFromTheAgentsModel(t *testing.T) {
	text := reviewTemplate(t, strings.Replace(judgeBlock, "%s", "deepseek-chat", 1))
	if _, err := loadOne(writeProfileFile(t, text), judgeEnv()); err == nil || !strings.Contains(err.Error(), "review.llm.model") {
		t.Fatalf("error = %v", err)
	}
}

func TestReviewLLMKeyMustBeSet(t *testing.T) {
	text := reviewTemplate(t, strings.Replace(judgeBlock, "%s", "judge-model", 1))
	if _, err := loadOne(writeProfileFile(t, text), validEnv()); err == nil || !strings.Contains(err.Error(), "review.llm.api_key_env") {
		t.Fatalf("error = %v", err)
	}
}
