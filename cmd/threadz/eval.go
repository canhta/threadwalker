package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/eval"
)

func runEval(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) > 0 && (args[0] == "metrics" || args[0] == "soak") {
		return runSimulation(ctx, args[0], args[1:], stdout, stderr)
	}
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	suitePath := fs.String("suite", "internal/eval/testdata/default.json", "versioned synthetic fixture suite")
	outPath := fs.String("out", "", "optional JSON report destination")
	baselinePath := fs.String("compare", "", "optional baseline JSON report")
	catalogPath := fs.String("llms", "", "optional provider/model catalog")
	profileNames := fs.String("profiles", "", "comma-separated selected model profile IDs")
	caseNames := fs.String("cases", "", "comma-separated selected synthetic case IDs")
	seed := fs.Int64("seed", 1, "deterministic fixture seed")
	repeat := fs.Int("repeat", 1, "repetitions per case and profile")
	revision := fs.String("revision", evalRevision(), "code revision for this run")
	maxUSD := fs.Float64("max-usd", 0, "estimated run USD cap (required for live)")
	maxSteps := fs.Int("max-steps", 0, "per-case step cap (required for live)")
	maxOutput := fs.Int("max-output-tokens", 0, "per-request output cap (required for live)")
	live := fs.Bool("live", false, "explicitly opt in to bounded provider calls")
	help, parseErr := parseFlags(fs, args)
	if help {
		writeUsage(stdout, "eval")
		return exitOK
	}
	if parseErr != nil || fs.NArg() != 0 || *repeat < 1 || *repeat > 1000 || *maxSteps < 0 || *maxOutput < 0 || *maxUSD < 0 || math.IsNaN(*maxUSD) || math.IsInf(*maxUSD, 0) || strings.TrimSpace(*revision) == "" {
		fmt.Fprintln(stderr, "invalid eval arguments or limits")
		writeUsage(stderr, "eval")
		return exitUsage
	}
	profiles, err := evalIDs(*profileNames)
	if err != nil {
		fmt.Fprintln(stderr, "invalid eval profile selection")
		return exitUsage
	}
	cases, err := evalIDs(*caseNames)
	if err != nil {
		fmt.Fprintln(stderr, "invalid eval case selection")
		return exitUsage
	}
	if (len(profiles) > 0) != (*catalogPath != "") {
		fmt.Fprintln(stderr, "eval requires -llms and -profiles together")
		return exitUsage
	}
	if *live && (len(profiles) == 0 || len(cases) == 0 || *maxUSD <= 0 || *maxSteps <= 0 || *maxOutput <= 0) {
		fmt.Fprintln(stderr, "live eval requires explicit -llms, -profiles, -cases, positive -max-usd, -max-steps and -max-output-tokens")
		return exitUsage
	}
	if *outPath != "" {
		for _, input := range []string{*suitePath, *catalogPath, *baselinePath} {
			if input != "" && sameEvalPath(*outPath, input) {
				fmt.Fprintln(stderr, "eval report destination must differ from input files")
				return exitUsage
			}
		}
		if err := checkEvalOutput(*outPath); err != nil {
			fmt.Fprintln(stderr, err)
			return exitFailure
		}
	}
	var baseline eval.Report
	if *baselinePath != "" {
		baseline, err = eval.ReadReport(*baselinePath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitFailure
		}
	}
	suite, err := eval.LoadSuite(*suitePath)
	if err != nil {
		fmt.Fprintln(stderr, "loading eval suite failed")
		return exitFailure
	}
	opts := eval.Options{Seed: *seed, Repeat: *repeat, Mode: "offline", Revision: *revision, CaseIDs: cases, MaxUSD: *maxUSD, MaxSteps: *maxSteps, MaxOutputTokens: *maxOutput, Getenv: getenv}
	if *live {
		opts.Mode = "live"
	}
	if len(profiles) > 0 {
		catalog, err := config.LoadCatalog(*catalogPath)
		if err != nil {
			fmt.Fprintln(stderr, "loading eval catalog failed")
			return exitFailure
		}
		selectedEnv := func(string) string { return "synthetic-offline-key" }
		if *live {
			selectedEnv = getenv
		}
		for _, id := range profiles {
			cfg, err := catalog.Resolve(id, selectedEnv)
			provider, ok := catalog.ProviderID(id)
			if err != nil || !ok {
				fmt.Fprintln(stderr, "resolving selected eval profile failed")
				return exitFailure
			}
			opts.Profiles = append(opts.Profiles, eval.Profile{ID: id, ProviderID: provider, Config: cfg})
		}
	}
	report, runErr := eval.Run(ctx, suite, opts)
	if runErr != nil {
		fmt.Fprintln(stderr, "eval execution failed")
		if report.SuiteDigest == "" {
			return exitFailure
		}
	}
	if *outPath != "" {
		if err := eval.WriteReport(*outPath, report); err != nil {
			fmt.Fprintln(stderr, err)
			return exitFailure
		}
	}
	fmt.Fprint(stdout, eval.Summary(report))
	failed := runErr != nil || len(report.Results) == 0
	for _, result := range report.Results {
		failed = failed || !result.Passed
	}
	if *baselinePath != "" {
		compared, err := eval.Compare(baseline, report)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitFailure
		}
		fmt.Fprint(stdout, eval.ComparisonSummary(compared))
		failed = failed || compared.HasRegressions
	}
	if failed {
		return exitFailure
	}
	return 0
}

func evalIDs(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	ids := strings.Split(raw, ",")
	seen := make(map[string]bool, len(ids))
	for i, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return nil, errors.New("invalid selection")
		}
		seen[id] = true
		ids[i] = id
	}
	return ids, nil
}

func sameEvalPath(a, b string) bool {
	aa, ae := filepath.Abs(a)
	bb, be := filepath.Abs(b)
	if ae == nil && be == nil && aa == bb {
		return true
	}
	ai, ae := os.Stat(a)
	bi, be := os.Stat(b)
	return ae == nil && be == nil && os.SameFile(ai, bi)
}

// Validate the destination before any opt-in paid requests are attempted.
func checkEvalOutput(path string) error {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return errors.New("eval report destination is a directory")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".threadz-eval-probe-*")
	if err != nil {
		return errors.New("eval report destination is unavailable")
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Close(); err != nil {
		return errors.New("checking eval report destination failed")
	}
	return nil
}

func evalRevision() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		revision, dirty := "", false
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && setting.Value != "" {
				revision = setting.Value
			}
			if setting.Key == "vcs.modified" && setting.Value == "true" {
				dirty = true
			}
		}
		if revision != "" {
			if dirty {
				revision += "+dirty"
			}
			return revision
		}
	}
	return "unknown"
}
