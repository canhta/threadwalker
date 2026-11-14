package config_test

import (
	"strings"
	"testing"
)

const writerBlock = "writer:\n  llm:\n    base_url: https://api.example\n    model: luna\n    api_key_env: LLM_API_KEY\n    price_per_mtok_in_usd: 0.1\n    price_per_mtok_out_usd: 0.5\n"
const postWriterLine = "  post_llm:\n    base_url: https://api.example\n    model: sol\n    api_key_env: LLM_API_KEY\n    reasoning_effort: high\n    price_per_mtok_in_usd: 2\n    price_per_mtok_out_usd: 10\n"

func TestPostWriterIsOptionalAndDecodedLikeWriter(t *testing.T) {
	plain, err := loadOne(writeProfileFile(t, withBlock(t, writerBlock)), validEnv())
	if err != nil || plain.PostWriter.Model != "" {
		t.Fatalf("plain: post writer %+v err %v", plain.PostWriter, err)
	}
	tmpl, err := loadOne(writeProfileFile(t, withBlock(t, writerBlock+postWriterLine)), validEnv())
	if err != nil {
		t.Fatal(err)
	}
	p := tmpl.PostWriter
	if p.Model != "sol" || p.ReasoningEffort != "high" || p.PricePerMTokOutUSD != 10 || p.APIKey.Expose() == "" || tmpl.Writer.Model != "luna" {
		t.Fatalf("post writer %+v writer %+v", p, tmpl.Writer)
	}
}

func TestPostWriterIsValidatedLikeWriter(t *testing.T) {
	bad := strings.Replace(postWriterLine, "    model: sol\n", "", 1)
	if _, err := loadOne(writeProfileFile(t, withBlock(t, writerBlock+bad)), validEnv()); err == nil || !strings.Contains(err.Error(), "writer.post_llm.model") {
		t.Fatalf("missing model: %v", err)
	}
	unset := strings.Replace(postWriterLine, "LLM_API_KEY", "NOPE_KEY", 1)
	if _, err := loadOne(writeProfileFile(t, withBlock(t, writerBlock+unset)), validEnv()); err == nil || !strings.Contains(err.Error(), "writer.post_llm.api_key_env") {
		t.Fatalf("unset key: %v", err)
	}
	alone := "writer:\n" + postWriterLine
	if _, err := loadOne(writeProfileFile(t, withBlock(t, alone)), validEnv()); err == nil || !strings.Contains(err.Error(), "writer.post_llm") {
		t.Fatalf("without writer.llm: %v", err)
	}
}
