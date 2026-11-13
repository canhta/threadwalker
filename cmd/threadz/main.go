package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/store"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if _, err := loadEnvFile(defaultEnvFile(os.Getenv), os.Getenv, os.Setenv); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitUsage)
	}
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "missing command")
		writeUsage(stderr, "")
		return exitUsage
	}

	switch args[0] {
	case "help", "-h", "--help", "-help":
		if len(args) > 2 {
			fmt.Fprintln(stderr, "help takes at most one command name")
			writeUsage(stderr, "")
			return exitUsage
		}
		target := ""
		if len(args) == 2 {
			target = args[1]
			if _, ok := commandUsage[target]; !ok {
				fmt.Fprintln(stderr, "unknown command for help")
				writeUsage(stderr, "")
				return exitUsage
			}
		}
		writeUsage(stdout, target)
		return exitOK
	case "version", "-version", "--version":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "version takes no arguments")
			writeUsage(stderr, "")
			return exitUsage
		}
		fmt.Fprintf(stdout, "threadz %s\n", versionText())
		return exitOK
	case "dashboard":
		return runDashboard(ctx, args[1:], getenv, stdout, stderr)
	case "ctl":
		return runCtl(ctx, args[1:], getenv, stdout, stderr, defaultCtlDeps())
	case "run":
		return runRuntime(ctx, args[1:], getenv, stdout, stderr)
	case "eval":
		return runEval(ctx, args[1:], getenv, stdout, stderr)
	case "update":
		return runUpdate(ctx, args[1:], getenv, stdout, stderr)
	case "check":
		return runCheck(ctx, args[1:], getenv, stdout, stderr)
	default:
		fmt.Fprintln(stderr, "unknown command")
		writeUsage(stderr, "")
		return exitUsage
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
	common := addCommonFlags(fs, getenv, "data/threadz.db")
	logOpts := addLogFlags(fs)
	help, parseErr := parseFlags(fs, args)
	if help {
		writeUsage(stdout, "check")
		return exitOK
	}
	configured, err := logOpts.logger(stdout)
	if err != nil {
		reason = "invalid_logging_options"
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	logger = configured
	logging.Emit(ctx, logger, slog.LevelInfo, logging.Record{Event: logging.CheckStarted, Operation: "check", Outcome: "started"})
	if parseErr != nil || fs.NArg() != 0 {
		reason = "invalid_arguments"
		fmt.Fprintln(stderr, "invalid check arguments")
		writeUsage(stderr, "check")
		return exitUsage
	}

	var failed bool

	tmpls, err := common.load(config.Options{Getenv: getenv})
	if err != nil {
		for _, subErr := range unwrapErrors(err) {
			fmt.Fprintln(stderr, subErr.Error())
		}
		reason = "configuration_invalid"
		failed = true
	}

	dbCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	st, err := store.Open(dbCtx, *common.db)
	if err != nil {
		reason = "store_unavailable"
		fmt.Fprintln(stderr, "opening database failed")
		failed = true
	} else {
		_ = st.Close()
	}

	if failed {
		return exitFailure
	}

	for _, t := range tmpls {
		logging.Emit(ctx, logger, slog.LevelInfo, logging.Record{Event: logging.CheckConfigured, Operation: "configuration", Outcome: "success", Reason: "validated", AgentID: t.ID, Model: t.LLM.Model, Protocol: t.LLM.Protocol})
	}

	return exitOK
}

// parseFlags distinguishes an explicit help request from a malformed flag set.
func parseFlags(fs *flag.FlagSet, args []string) (help bool, err error) {
	err = fs.Parse(args)
	if err == flag.ErrHelp {
		return true, nil
	}
	return false, err
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
