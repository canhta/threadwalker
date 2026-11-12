package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/dashboard"
	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/store"
)

func runDashboard(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	agents := fs.String("agents", defaultAgentsDir(), "agent template directory")
	catalogPath := fs.String("llms", defaultLLMsPath(), "optional LLM catalog")
	dbPath := fs.String("db", defaultDBPath(), "existing SQLite database")
	listen := fs.String("listen", "127.0.0.1:8080", "numeric loopback listen address")
	level := fs.String("log-level", "info", "operational log level")
	format := fs.String("log-format", "json", "operational log format")
	fs.StringVar(agents, "a", *agents, "shorthand for --agents")
	fs.StringVar(dbPath, "d", *dbPath, "shorthand for --db")
	fs.StringVar(catalogPath, "l", *catalogPath, "shorthand for --llms")
	help, parseErr := parseFlags(fs, args)
	if help {
		writeUsage(stdout, "dashboard")
		return exitOK
	}
	if parseErr != nil || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "invalid dashboard arguments")
		writeUsage(stderr, "dashboard")
		return exitUsage
	}
	host, port, err := net.SplitHostPort(*listen)
	ip := net.ParseIP(host)
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || ip == nil || !ip.IsLoopback() || portErr != nil || portNumber < 0 || portNumber > 65535 || strings.Contains(host, "%") {
		fmt.Fprintln(stderr, "dashboard requires a numeric loopback listen address")
		return exitUsage
	}
	logger, err := logging.New(stdout, *level, *format)
	if err != nil {
		fmt.Fprintln(stderr, "invalid dashboard logging options")
		return exitUsage
	}
	if ctx.Err() != nil {
		return 0
	}
	var catalog *config.Catalog
	if *catalogPath != "" {
		catalog, err = config.LoadCatalog(*catalogPath)
		if err != nil {
			fmt.Fprintln(stderr, "dashboard catalog is invalid")
			return exitFailure
		}
	}
	templates, err := config.LoadPublicDirWithCatalog(*agents, catalog)
	if err != nil {
		fmt.Fprintln(stderr, "dashboard public configuration is invalid")
		return exitFailure
	}
	selected := make([]dashboard.Agent, 0, len(templates))
	for _, t := range templates {
		selected = append(selected, dashboard.Agent{ID: t.ID, Username: t.Threads.Username, DailyBudgetUSD: t.Limits.MaxUSDPerDay})
	}
	openCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	st, err := store.OpenReadOnly(openCtx, *dbPath)
	cancel()
	if err != nil {
		fmt.Fprintln(stderr, "dashboard database is unavailable; initialize it with check first")
		return exitFailure
	}
	defer st.Close()
	handler, err := dashboard.New(dashboard.Options{DB: st.DB(), Agents: selected, Now: time.Now, Logger: logger})
	if err != nil {
		fmt.Fprintln(stderr, "dashboard public configuration is invalid")
		return exitFailure
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", *listen)
	if err != nil {
		fmt.Fprintln(stderr, "dashboard loopback listener is unavailable")
		return exitFailure
	}
	defer listener.Close()
	if err = dashboard.Serve(ctx, listener, handler); err != nil {
		fmt.Fprintln(stderr, "dashboard server is unavailable")
		return exitFailure
	}
	return 0
}
