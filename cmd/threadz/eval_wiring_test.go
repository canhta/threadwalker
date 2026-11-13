package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/eval"
)

func TestEvalArgumentFailuresNeverReadEnvironmentOrEchoInput(t *testing.T) {
	for _, args := range [][]string{
		{"-unknown", "planted-credential @stranger"}, {"-repeat", "0"}, {"-repeat", "1001"}, {"-max-usd", "NaN"}, {"-max-usd", "+Inf"}, {"-max-usd", "-1"}, {"-max-steps", "-1"}, {"-max-output-tokens", "-1"}, {"-revision", " "}, {"-profiles", "one,,two"}, {"-cases", "a,a"}, {"-profiles", "one"}, {"-llms", "planted-credential @stranger"}, {"-live"}, {"extra"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := run(context.Background(), append([]string{"eval"}, args...), noSecretEnv(t, "invalid invocation read credentials"), &out, &errOut)
			if code == 0 {
				t.Fatal("invalid invocation succeeded")
			}
			if strings.Contains(out.String()+errOut.String(), "planted-credential") || strings.Contains(out.String()+errOut.String(), "@stranger") {
				t.Fatal("diagnostic echoed untrusted argument")
			}
		})
	}
}

func TestEvalRefusesInputOverwriteIncludingSymlinks(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "suite.json")
	alias := filepath.Join(dir, "alias.json")
	if err := os.WriteFile(input, []byte("original synthetic fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(input, alias); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{input, alias} {
		var out, errOut bytes.Buffer
		if code := runEval(context.Background(), []string{"-suite", input, "-out", output}, nil, &out, &errOut); code == 0 {
			t.Fatal("input overwrite allowed")
		}
	}
	data, err := os.ReadFile(input)
	if err != nil || string(data) != "original synthetic fixture" {
		t.Fatal("fixture changed")
	}
}

func TestEvalOutputIsValidatedBeforeOptInProviderCalls(t *testing.T) {
	for _, path := range []string{t.TempDir(), filepath.Join(t.TempDir(), "missing", "report.json")} {
		var out, errOut bytes.Buffer
		code := runEval(context.Background(), []string{"-live", "-llms", "unused.yaml", "-profiles", "chosen", "-cases", "one", "-max-usd", "0.01", "-max-steps", "1", "-max-output-tokens", "1", "-out", path}, func(string) string {
			t.Fatal("invalid report path reached credential resolution")
			return ""
		}, &out, &errOut)
		if code == 0 || !strings.Contains(errOut.String(), "destination") {
			t.Fatal("invalid report destination did not fail before suite/provider setup")
		}
	}
}

func TestCanonicalSeededFakeRegressionAndMetricComparison(t *testing.T) {
	suite := filepath.Join("..", "..", "internal", "eval", "testdata", "comparison.json")
	catalog := filepath.Join("..", "..", "internal", "eval", "testdata", "comparison-catalog.yaml")
	baselinePath := filepath.Join(t.TempDir(), "baseline.json")
	currentPath := filepath.Join(t.TempDir(), "current.json")
	env := noSecretEnv(t, "offline comparison read credentials")
	var out, errOut bytes.Buffer
	baseArgs := []string{"eval", "-suite", suite, "-llms", catalog, "-revision", "seeded-fake-test"}
	if code := run(context.Background(), append(append([]string{}, baseArgs...), "-profiles", "baseline", "-out", baselinePath), env, &out, &errOut); code != 0 {
		t.Fatal("known-good fake failed")
	}
	out.Reset()
	errOut.Reset()
	if code := run(context.Background(), append(append([]string{}, baseArgs...), "-profiles", "regressed", "-compare", baselinePath, "-out", currentPath), env, &out, &errOut); code == 0 {
		t.Fatal("regressed fake succeeded")
	}
	for _, want := range []string{"REGRESSION", "seeded_provider_regression", "content_mismatch", "tokens_in_delta=+8", "tokens_out_delta=+1", "estimated_cost_usd_delta=+0.000010000"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("comparison omitted %s", want)
		}
	}
	baseline, err := eval.ReadReport(baselinePath)
	if err != nil {
		t.Fatal(err)
	}
	current, err := eval.ReadReport(currentPath)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.SuiteDigest != current.SuiteDigest || baseline.InputsFingerprint != current.InputsFingerprint {
		t.Fatal("regression demonstration changed fixture inputs")
	}
}
