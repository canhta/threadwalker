package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/canhta/threadwalker/internal/agent"
	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/eval"
	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/store"
	"github.com/canhta/threadwalker/internal/telegram"
)

func runRuntime(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	agentsDir := fs.String("agents", "agents", "agent templates directory")
	catalogPath := fs.String("llms", "", "optional LLM catalog")
	dbPath := fs.String("db", "", "database path; offline defaults to a temporary isolated database")
	once := fs.Bool("once", false, "execute at most one due wake")
	offline := fs.Bool("offline", false, "use only a synthetic template and in-process fake services; no environment credentials")
	hours := fs.Int("soak-hours", 0, "offline logical hours (48..720); includes one restart")
	evidence := fs.String("evidence", "", "offline soak evidence directory")
	level := fs.String("log-level", "info", "debug/info/warn/error")
	format := fs.String("log-format", "json", "json/text")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *hours < 0 || (*hours > 0 && (!*offline || *once || *hours < 48 || *hours > 720)) || (*evidence != "" && *hours == 0) {
		fmt.Fprintln(stderr, "invalid run arguments; soak requires -offline -soak-hours 48..720 and optional -evidence")
		return 1
	}
	logOutput := stdout
	if *hours > 0 && *evidence != "" {
		if err := os.MkdirAll(*evidence, 0700); err != nil {
			fmt.Fprintln(stderr, "creating evidence directory failed")
			return 1
		}
		f, err := os.OpenFile(filepath.Join(*evidence, "events.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			fmt.Fprintln(stderr, "creating evidence event file failed; use a fresh directory")
			return 1
		}
		defer f.Close()
		logOutput = io.MultiWriter(stdout, f)
	}
	logger, err := logging.New(logOutput, *level, *format)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *dbPath == "" {
		if *offline {
			dir, err := os.MkdirTemp("", "threadwalker-offline-*")
			if err != nil {
				fmt.Fprintln(stderr, "creating isolated offline directory failed")
				return 1
			}
			defer os.RemoveAll(dir)
			*dbPath = filepath.Join(dir, "offline.db")
		} else {
			*dbPath = "data/threadwalker.db"
		}
	}
	if *hours > 0 {
		return runSoak(ctx, *dbPath, *hours, *evidence, logger, stdout, stderr)
	}
	explicitConfig := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "agents" || f.Name == "llms" {
			explicitConfig = true
		}
	})
	var templates []config.Template
	if *offline && !explicitConfig {
		templates = []config.Template{agent.OfflineTemplate()}
	} else {
		var catalog *config.Catalog
		if *catalogPath != "" {
			catalog, err = config.LoadCatalog(*catalogPath)
			if err != nil {
				fmt.Fprintln(stderr, "loading runtime catalog failed")
				return 1
			}
		}
		resolveEnv := getenv
		if *offline {
			resolveEnv = func(string) string { return "offline-dummy-key" }
		}
		templates, err = config.LoadDirWithCatalog(*agentsDir, resolveEnv, catalog)
		if err != nil {
			fmt.Fprintln(stderr, "loading runtime templates failed")
			return 1
		}
	}
	seen := map[string]bool{}
	for i := range templates {
		if *offline {
			// Explicit templates cannot enable a browser in offline mode.
			// Non-dry-run templates still fail closed below.
			templates[i].Threads.Backend = "stub"
			templates[i].Threads.BaseURL = ""
			templates[i].Threads.ExecutablePath = ""
		}
		tmpl := templates[i]
		if seen[tmpl.ID] || (!tmpl.DryRun && tmpl.Threads.Backend != "browser") {
			fmt.Fprintln(stderr, "runtime requires distinct agent IDs and explicit browser configuration for non-dry-run")
			return 1
		}
		seen[tmpl.ID] = true
	}
	if err := config.ValidateTemplates(templates); err != nil {
		fmt.Fprintln(stderr, "runtime requires isolated agent identities and profiles")
		return 1
	}
	ownUsernames := []string{}
	for _, tmpl := range templates {
		if tmpl.Threads.Username != "" {
			ownUsernames = append(ownUsernames, tmpl.Threads.Username)
		}
	}
	dbCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	st, err := store.Open(dbCtx, *dbPath)
	cancel()
	if err != nil {
		fmt.Fprintln(stderr, "opening runtime database failed")
		return 1
	}
	defer st.Close()
	// Default offline operation uses a fixed logical clock; waiting advances it
	// locally, with no wall-clock sleep. -once remains a single bounded wake.
	now := time.Now
	wait := agent.Wait
	if *offline {
		logical := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		now = func() time.Time { return logical }
		wait = func(ctx context.Context, d time.Duration) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			logical = logical.Add(d)
			return nil
		}
	}
	// Offline mode disables owner integration before client construction.
	if *offline {
		for i := range templates {
			templates[i].Telegram = config.TelegramConfig{}
		}
	}
	services, bindings, err := telegram.Services(st, templates, now, logger)
	if err != nil {
		fmt.Fprintln(stderr, "configuring owner service failed")
		return 1
	}
	secrets := []config.Secret{}
	for _, tmpl := range templates {
		secrets = append(secrets, tmpl.LLM.APIKey, tmpl.Telegram.BotToken, tmpl.Telegram.OwnerChatID)
		if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
			fmt.Fprintln(stderr, "initializing runtime agent failed")
			return 1
		}
	}
	runtimes := []*agent.Runtime{}
	for _, tmpl := range templates {
		if *offline {
			tmpl.LLM.BaseURL = "https://fixture.invalid"
			tmpl.LLM.APIKey = config.NewSecret("offline-dummy-key")
			tmpl.Telegram = config.TelegramConfig{}
		}
		opts := agent.Options{Store: st, Template: tmpl, OwnUsernames: ownUsernames, Logger: logger, Now: now, Wait: wait, Secrets: secrets}
		if owner := bindings[tmpl.ID]; owner != nil {
			opts.Owner = owner
		}
		if *offline {
			fixture := agent.NewOfflineFixture(now)
			opts.HTTPClient = fixture.Client()
			opts.FetchTrends = fixture.Trends
		}
		runtime, err := agent.New(opts)
		if err != nil {
			fmt.Fprintln(stderr, "configuring runtime failed")
			return 1
		}
		runtimes = append(runtimes, runtime)
	}
	serviceCtx, stopServices := context.WithCancel(ctx)
	var serviceWG sync.WaitGroup
	for _, svc := range services {
		serviceWG.Add(1)
		go func(svc *telegram.Service) { defer serviceWG.Done(); _ = svc.Run(serviceCtx) }(svc)
	}
	err = (agent.Scheduler{Agents: runtimes, Now: now, Wait: wait}).Run(serviceCtx, *once)
	stopServices()
	serviceWG.Wait()
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(stderr, "runtime stopped; inspect safe event reasons")
		return 1
	}
	return 0
}

func runSoak(ctx context.Context, dbPath string, hours int, evidencePath string, logger *slog.Logger, stdout, stderr io.Writer) int {
	result, err := agent.RunOfflineSoak(ctx, dbPath, hours, logger)
	if err != nil {
		fmt.Fprintln(stderr, "offline logical soak failed; use an isolated empty database")
		return 1
	}
	suite, err := eval.LoadSuite("internal/eval/testdata/runtime-v1.json")
	if err != nil {
		fmt.Fprintln(stderr, "loading offline runtime eval fixtures failed; run from repository root")
		return 1
	}
	report, err := eval.Run(ctx, suite, eval.Options{Seed: 1, Revision: evalRevision()})
	if err != nil {
		fmt.Fprintln(stderr, "runtime eval failed")
		return 1
	}
	for _, c := range report.Results {
		if !c.Passed {
			fmt.Fprintln(stderr, "runtime eval expectation failed")
			return 1
		}
	}
	if evidencePath != "" {
		data, _ := json.MarshalIndent(result, "", "  ")
		if err = os.WriteFile(filepath.Join(evidencePath, "wakes.json"), append(data, '\n'), 0600); err != nil {
			fmt.Fprintln(stderr, "writing soak evidence failed")
			return 1
		}
		if err = eval.WriteReport(filepath.Join(evidencePath, "eval.json"), report); err != nil {
			fmt.Fprintln(stderr, "writing eval evidence failed")
			return 1
		}
	}
	summary := result
	summary.Wakes = nil
	if err = json.NewEncoder(stdout).Encode(summary); err != nil {
		fmt.Fprintln(stderr, "writing soak summary failed")
		return 1
	}
	return 0
}
