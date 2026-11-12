package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/canhta/threadwalker/internal/eval"
	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/metrics"
)

// runMetrics is deliberately offline-only. Normal configured polling runs in
// the existing run scheduler; this command reproduces deterministic evidence.
func runMetrics(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("metrics", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	offline := fs.Bool("offline", false, "required: synthetic source and injected logical time")
	hours := fs.Int("soak-hours", 72, "48..720 logical hours; no wall-clock soak claim")
	evidence := fs.String("evidence", "", "optional new directory for metadata-only evidence")
	level := fs.String("log-level", "info", "debug/info/warn/error")
	help, parseErr := parseFlags(fs, args)
	if help {
		writeUsage(stdout, "metrics")
		return exitOK
	}
	if parseErr != nil || fs.NArg() != 0 || !*offline || *hours < 48 || *hours > 720 {
		fmt.Fprintln(stderr, "metrics requires --offline and --soak-hours 48..720; optional --evidence must be a new directory")
		writeUsage(stderr, "metrics")
		return exitUsage
	}
	output := stdout
	if *evidence != "" {
		if err := os.Mkdir(*evidence, 0700); err != nil {
			fmt.Fprintln(stderr, "creating metric evidence directory failed; use a fresh directory")
			return exitFailure
		}
		f, err := os.OpenFile(filepath.Join(*evidence, "events.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			fmt.Fprintln(stderr, "creating metric event evidence failed")
			return exitFailure
		}
		defer f.Close()
		output = io.MultiWriter(stdout, f)
	}
	logger, err := logging.New(output, *level, "json")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	dir, err := os.MkdirTemp("", "threadwalker-metric-simulation-*")
	if err != nil {
		fmt.Fprintln(stderr, "creating metric simulation directory failed")
		return exitFailure
	}
	defer os.RemoveAll(dir)
	evidenceResult, err := metrics.RunOfflineSimulation(ctx, filepath.Join(dir, "metrics.db"), *hours, logger)
	if err != nil {
		fmt.Fprintln(stderr, "offline metric simulation failed; inspect safe event reasons")
		return exitFailure
	}
	suite, err := eval.LoadSuite("internal/eval/testdata/metrics-v1.json")
	if err != nil {
		fmt.Fprintln(stderr, "loading metric eval fixtures failed; run from repository root")
		return exitFailure
	}
	report, err := eval.Run(ctx, suite, eval.Options{Seed: 19, Revision: evalRevision()})
	if err != nil {
		fmt.Fprintln(stderr, "running metric eval failed")
		return exitFailure
	}
	for _, r := range report.Results {
		if !r.Passed {
			fmt.Fprintln(stderr, "metric eval expectation failed")
			return exitFailure
		}
	}
	if *evidence != "" {
		data, _ := json.MarshalIndent(evidenceResult, "", "  ")
		if err = os.WriteFile(filepath.Join(*evidence, "simulation.json"), append(data, '\n'), 0600); err != nil {
			fmt.Fprintln(stderr, "writing metric simulation evidence failed")
			return exitFailure
		}
		if err = eval.WriteReport(filepath.Join(*evidence, "eval.json"), report); err != nil {
			fmt.Fprintln(stderr, "writing metric eval evidence failed")
			return exitFailure
		}
	}
	evidenceResult.Observations = nil
	if err = json.NewEncoder(stdout).Encode(evidenceResult); err != nil {
		fmt.Fprintln(stderr, "writing metric simulation summary failed")
		return exitFailure
	}
	return 0
}
