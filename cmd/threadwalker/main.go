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
	"github.com/canhta/threadwalker/internal/store"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	logger := slog.New(slog.NewTextHandler(stdout, nil))

	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: threadwalker <check>")
		return 1
	}

	switch args[0] {
	case "check":
		return runCheck(ctx, args[1:], getenv, logger, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q (expected: check)\n", args[0])
		return 1
	}
}

func runCheck(ctx context.Context, args []string, getenv func(string) string, logger *slog.Logger, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	agentsDir := fs.String("agents", "agents", "directory containing agent YAML templates")
	dbPath := fs.String("db", "data/threadwalker.db", "path to SQLite database")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	var failed bool

	tmpls, err := config.LoadDir(*agentsDir, getenv)
	if err != nil {
		for _, subErr := range unwrapErrors(err) {
			fmt.Fprintln(stderr, subErr.Error())
		}
		failed = true
	}

	dbCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	st, err := store.Open(dbCtx, *dbPath)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		failed = true
	} else {
		_ = st.Close()
	}

	if failed {
		return 1
	}

	ids := make([]string, 0, len(tmpls))
	for _, t := range tmpls {
		ids = append(ids, t.ID)
	}
	logger.Info("check passed", "template_count", len(tmpls), "agent_ids", ids, "db", *dbPath)
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
