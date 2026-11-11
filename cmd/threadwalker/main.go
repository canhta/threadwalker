package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/store"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	logger, _ := logging.New(stdout, "", "")

	if len(args) == 0 {
		logging.Emit(ctx, logger, slog.LevelError, logging.Record{Event: logging.CheckFinished, Operation: "check", Outcome: "error", Reason: "missing_command"})
		fmt.Fprintln(stderr, "usage: threadwalker <check>")
		return 1
	}

	switch args[0] {
	case "check":
		return runCheck(ctx, args[1:], getenv, stdout, stderr)
	default:
		logging.Emit(ctx, logger, slog.LevelError, logging.Record{Event: logging.CheckFinished, Operation: "check", Outcome: "error", Reason: "unknown_command"})
		fmt.Fprintln(stderr, "unknown command (expected: check)")
		return 1
	}
}

func runCheck(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) (exitCode int) {
	logger, _ := logging.New(stdout, "", "")
	reason := "validation_failed"
	defer func() {
		outcome, level := "success", slog.LevelInfo
		if exitCode != 0 {
			outcome, level = "error", slog.LevelError
		} else {
			reason = "validated"
		}
		logging.Emit(ctx, logger, level, logging.Record{Event: logging.CheckFinished, Operation: "check", Outcome: outcome, Reason: reason})
	}()
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	agentsDir := fs.String("agents", "agents", "directory containing agent YAML templates")
	llmsPath := fs.String("llms", "", "optional LLM provider and model catalog YAML file")
	dbPath := fs.String("db", "data/threadwalker.db", "path to SQLite database")
	level := fs.String("log-level", "info", "operational log level: debug, info, warn or error")
	format := fs.String("log-format", "json", "operational log format: json or text")
	parseErr := fs.Parse(args)
	configured, err := logging.New(stdout, *level, *format)
	if err != nil {
		reason = "invalid_logging_options"
		fmt.Fprintln(stderr, err)
		return 1
	}
	logger = configured
	logging.Emit(ctx, logger, slog.LevelInfo, logging.Record{Event: logging.CheckStarted, Operation: "check", Outcome: "started"})
	if parseErr != nil || fs.NArg() != 0 {
		reason = "invalid_arguments"
		fmt.Fprintln(stderr, "invalid check arguments; use -agents, -llms, -db, -log-level and -log-format")
		return 1
	}

	var failed bool

	var catalog *config.Catalog
	if *llmsPath != "" {
		var err error
		catalog, err = config.LoadCatalog(*llmsPath)
		if err != nil {
			reason = "catalog_invalid"
			for _, subErr := range unwrapErrors(err) {
				fmt.Fprintln(stderr, subErr.Error())
			}
			return 1
		}
	}

	tmpls, err := config.LoadDirWithCatalog(*agentsDir, getenv, catalog)
	if err != nil {
		reason = "configuration_invalid"
		for _, subErr := range unwrapErrors(err) {
			fmt.Fprintln(stderr, subErr.Error())
		}
		failed = true
	}

	dbCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	st, err := store.Open(dbCtx, *dbPath)
	if err != nil {
		reason = "store_unavailable"
		fmt.Fprintln(stderr, "opening database failed")
		failed = true
	} else {
		_ = st.Close()
	}

	if failed {
		return 1
	}

	for _, t := range tmpls {
		logging.Emit(ctx, logger, slog.LevelInfo, logging.Record{Event: logging.CheckConfigured, Operation: "configuration", Outcome: "success", Reason: "validated", AgentID: t.ID, Model: t.LLM.Model, Protocol: t.LLM.Protocol})
	}

	return 0
}

func unwrapErrors(err error) []error {
	if err == nil {
		return nil
	}
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		var out []error
		for _, child := range multi.Unwrap() {
			out = append(out, unwrapErrors(child)...)
		}
		return out
	}
	return []error{err}
}
