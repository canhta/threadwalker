package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/eval"
	"github.com/canhta/threadz/internal/logging"
)

// runSimulation runs the offline logical-time simulations under `eval`: the
// metric-collection simulation ("metrics") and the runtime soak ("soak", one
// or two agents). Both use synthetic services and an injected clock.
func runSimulation(ctx context.Context, kind string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("eval "+kind, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	hours := fs.Int("soak-hours", 72, "48..720 logical hours; no wall-clock soak claim")
	evidence := fs.String("evidence", "", "optional new directory for metadata-only evidence")
	level := fs.String("log-level", "info", "debug/info/warn/error")
	soakAgents := fs.Int("soak-agents", 1, "synthetic agent count (1 or 2); soak only")
	help, parseErr := parseFlags(fs, args)
	if help {
		writeUsage(stdout, "eval")
		return exitOK
	}
	agents := *soakAgents
	if parseErr != nil || fs.NArg() != 0 || *hours < 48 || *hours > 720 || (agents != 1 && agents != 2) || (kind == "metrics" && agents != 1) {
		fmt.Fprintf(stderr, "eval %s requires --soak-hours 48..720 and --soak-agents 1 or 2 (soak only)\n", kind)
		writeUsage(stderr, "eval")
		return exitUsage
	}
	output := stdout
	if *evidence != "" {
		if err := os.Mkdir(*evidence, 0700); err != nil {
			fmt.Fprintln(stderr, "creating evidence directory failed; use a fresh directory")
			return exitFailure
		}
		f, err := os.OpenFile(filepath.Join(*evidence, "events.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			fmt.Fprintln(stderr, "creating evidence event file failed")
			return exitFailure
		}
		defer f.Close()
		output = io.MultiWriter(stdout, f)
	}
	logger, err := logging.New(output, *level, "json")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	dir, err := os.MkdirTemp("", "threadz-simulation-*")
	if err != nil {
		fmt.Fprintln(stderr, "creating simulation directory failed")
		return exitFailure
	}
	defer os.RemoveAll(dir)
	db := filepath.Join(dir, kind+".db")
	switch {
	case kind == "metrics":
		return runMetricSimulation(ctx, db, *hours, *evidence, logger, stdout, stderr)
	case agents == 2:
		return runIsolationSoakCLI(ctx, db, *hours, *evidence, logger, stdout, stderr)
	default:
		return runSoak(ctx, db, *hours, *evidence, logger, stdout, stderr)
	}
}

func runMetricSimulation(ctx context.Context, dbPath string, hours int, evidencePath string, logger *slog.Logger, stdout, stderr io.Writer) int {
	evidenceResult, err := eval.RunMetricSimulation(ctx, dbPath, hours, logger)
	if err != nil {
		fmt.Fprintln(stderr, "offline metric simulation failed; inspect safe event reasons")
		return exitFailure
	}
	suite, err := eval.MetricsSuite()
	if err != nil {
		fmt.Fprintln(stderr, "loading metric eval fixtures failed")
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
	if evidencePath != "" {
		data, _ := json.MarshalIndent(evidenceResult, "", "  ")
		if err = os.WriteFile(filepath.Join(evidencePath, "simulation.json"), append(data, '\n'), 0600); err != nil {
			fmt.Fprintln(stderr, "writing metric simulation evidence failed")
			return exitFailure
		}
		if err = eval.WriteReport(filepath.Join(evidencePath, "eval.json"), report); err != nil {
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

func runIsolationSoakCLI(ctx context.Context, dbPath string, hours int, evidencePath string, logger *slog.Logger, stdout, stderr io.Writer) int {
	result, err := agent.RunIsolationSoak(ctx, dbPath, hours, logger)
	if err != nil {
		fmt.Fprintln(stderr, "offline two-agent logical soak failed; use an isolated empty database")
		return exitFailure
	}
	result.SourceRevision = evalRevision()
	suite, err := eval.IsolationSuite()
	if err != nil {
		fmt.Fprintln(stderr, "loading isolation eval fixtures failed")
		return exitFailure
	}
	// The exported soak already executes the long component. Companion evals
	// independently exercise the focused data/control/guard boundaries.
	report, err := eval.Run(ctx, suite, eval.Options{Seed: 12, Revision: evalRevision(), CaseIDs: []string{"isolation_data_boundaries", "isolation_owner_budget_controls", "isolation_own_account_refusal", "isolation_shared_link_reservation"}})
	if err != nil {
		fmt.Fprintln(stderr, "isolation eval failed")
		return exitFailure
	}
	for _, r := range report.Results {
		if !r.Passed {
			fmt.Fprintln(stderr, "isolation eval expectation failed")
			return exitFailure
		}
	}
	if evidencePath != "" {
		data, _ := json.MarshalIndent(result, "", "  ")
		if err = os.WriteFile(filepath.Join(evidencePath, "isolation.json"), append(data, '\n'), 0600); err != nil {
			fmt.Fprintln(stderr, "writing isolation evidence failed")
			return exitFailure
		}
		if err = eval.WriteReport(filepath.Join(evidencePath, "eval.json"), report); err != nil {
			fmt.Fprintln(stderr, "writing isolation eval failed")
			return exitFailure
		}
	}
	summary := result
	summary.Wakes = nil
	summary.Hours = nil
	if err = json.NewEncoder(stdout).Encode(summary); err != nil {
		fmt.Fprintln(stderr, "writing isolation summary failed")
		return exitFailure
	}
	return 0
}

func runSoak(ctx context.Context, dbPath string, hours int, evidencePath string, logger *slog.Logger, stdout, stderr io.Writer) int {
	result, err := agent.RunOfflineSoak(ctx, dbPath, hours, logger)
	if err != nil {
		fmt.Fprintln(stderr, "offline logical soak failed; use an isolated empty database")
		return exitFailure
	}
	suite, err := eval.RuntimeSuite()
	if err != nil {
		fmt.Fprintln(stderr, "loading offline runtime eval fixtures failed")
		return exitFailure
	}
	report, err := eval.Run(ctx, suite, eval.Options{Seed: 1, Revision: evalRevision()})
	if err != nil {
		fmt.Fprintln(stderr, "runtime eval failed")
		return exitFailure
	}
	for _, c := range report.Results {
		if !c.Passed {
			fmt.Fprintln(stderr, "runtime eval expectation failed")
			return exitFailure
		}
	}
	if evidencePath != "" {
		data, _ := json.MarshalIndent(result, "", "  ")
		if err = os.WriteFile(filepath.Join(evidencePath, "wakes.json"), append(data, '\n'), 0600); err != nil {
			fmt.Fprintln(stderr, "writing soak evidence failed")
			return exitFailure
		}
		if err = eval.WriteReport(filepath.Join(evidencePath, "eval.json"), report); err != nil {
			fmt.Fprintln(stderr, "writing eval evidence failed")
			return exitFailure
		}
	}
	summary := result
	summary.Wakes = nil
	if err = json.NewEncoder(stdout).Encode(summary); err != nil {
		fmt.Fprintln(stderr, "writing soak summary failed")
		return exitFailure
	}
	return 0
}
