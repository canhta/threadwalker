package eval_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canhta/threadwalker/internal/eval"
)

func loadJSON(t *testing.T, body string) (*eval.Suite, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "suite.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return eval.LoadSuite(path)
}

func TestLoadSuiteRejectsMalformedContractsWithoutEchoingData(t *testing.T) {
	tests := map[string]string{
		"unknown":             strings.Replace(minimalSuite, `"version":"v1"`, `"version":"v1","planted-secret @stranger":true`, 1),
		"duplicate":           strings.Replace(minimalSuite, `"version":"v1"`, `"version":"v1","version":"v1"`, 1),
		"unsupported version": strings.Replace(minimalSuite, `"version":"v1"`, `"version":"v2"`, 1),
		"missing expectation": strings.Replace(minimalSuite, `"end_reason":"completed",`, "", 1),
		"unknown expectation": strings.Replace(minimalSuite, `"end_reason":"completed"`, `"end_reason":"whatever"`, 1),
		"empty case id":       strings.Replace(minimalSuite, `"id":"answer"`, `"id":""`, 1),
		"zero limit":          strings.Replace(minimalSuite, `"id":"answer"`, `"id":"answer","limits":{"max_steps":0}`, 1),
		"null limit":          strings.Replace(minimalSuite, `"id":"answer"`, `"id":"answer","limits":{"max_usd":null}`, 1),
		"negative limit":      strings.Replace(minimalSuite, `"id":"answer"`, `"id":"answer","limits":{"max_output_tokens":-1}`, 1),
		"unknown protocol":    strings.Replace(minimalSuite, `"openai":`, `"other":`, 1),
		"both bodies":         strings.Replace(minimalSuite, `"status":200`, `"status":200,"body_raw":"bad"`, 1),
		"unknown error":       strings.Replace(minimalSuite, `"status":200`, `"error":"planted-secret @stranger"`, 1),
		"trailing document":   minimalSuite + ` {}`,
		"empty cases":         `{"version":"v1","cases":[]}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := loadJSON(t, body)
			if err == nil {
				t.Fatal("accepted malformed suite")
			}
			if strings.Contains(err.Error(), "planted-secret") || strings.Contains(err.Error(), "stranger") {
				t.Fatal("unsafe diagnostic")
			}
		})
	}
	if _, err := eval.LoadSuite(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("accepted missing file")
	}
}

func TestSuiteDigestIgnoresObjectKeyOrdering(t *testing.T) {
	a, err := loadJSON(t, minimalSuite)
	if err != nil {
		t.Fatal(err)
	}
	reordered := strings.Replace(minimalSuite, `"role":"assistant","content":"DONE"`, `"content":"DONE","role":"assistant"`, 1)
	b, err := loadJSON(t, reordered)
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest != b.Digest {
		t.Fatal("JSON object ordering changed digest")
	}
}

func TestSuiteDigestDistinguishesAbsentAndEmptyExpectedTools(t *testing.T) {
	a, err := loadJSON(t, minimalSuite)
	if err != nil {
		t.Fatal(err)
	}
	b, err := loadJSON(t, strings.Replace(minimalSuite, `"end_reason":"completed"`, `"end_reason":"completed","tools":[]`, 1))
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest == b.Digest {
		t.Fatal("digest erased an explicit no-tools expectation")
	}
}

func TestSuiteRawJSONRetainsUnrepresentableProviderNumbers(t *testing.T) {
	s, err := loadJSON(t, strings.Replace(minimalSuite, `"prompt_tokens":10`, `"prompt_tokens":1e999`, 1))
	if err != nil || s == nil {
		t.Fatalf("provider raw JSON rejected instead of evaluated: %v", err)
	}
}

const minimalSuite = `{"version":"v1","cases":[{"id":"answer","messages":[{"role":"user","content":"answer"}],"responses":{"openai":[{"status":200,"body":{"choices":[{"message":{"role":"assistant","content":"DONE"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}}]},"expect":{"end_reason":"completed","content":"DONE"}}]}`

func TestLoadSuiteCanonicalDigest(t *testing.T) {
	a, err := loadJSON(t, minimalSuite)
	if err != nil {
		t.Fatal(err)
	}
	b, err := loadJSON(t, " \n"+minimalSuite+" \n")
	if err != nil {
		t.Fatal(err)
	}
	if a.Version != "v1" || len(a.Cases) != 1 || len(a.Digest) != 64 || a.Digest != b.Digest {
		t.Fatalf("invalid suite/digest: %+v %+v", a, b)
	}
}
