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
	agents := fs.String("agents", "agents", "agent template directory")
	catalogPath := fs.String("llms", "", "optional LLM catalog")
	dbPath := fs.String("db", "data/threadwalker.db", "existing SQLite database")
	listen := fs.String("listen", "127.0.0.1:8080", "numeric loopback listen address")
	level := fs.String("log-level", "info", "operational log level")
	format := fs.String("log-format", "json", "operational log format")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "invalid dashboard arguments; use -agents, -llms, -db, -listen, -log-level and -log-format")
		return 1
	}
	host, port, err := net.SplitHostPort(*listen)
	ip := net.ParseIP(host)
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || ip == nil || !ip.IsLoopback() || portErr != nil || portNumber < 0 || portNumber > 65535 || strings.Contains(host, "%") {
		fmt.Fprintln(stderr, "dashboard requires a numeric loopback listen address")
		return 1
	}
	logger, err := logging.New(stdout, *level, *format)
	if err != nil {
		fmt.Fprintln(stderr, "invalid dashboard logging options")
		return 1
	}
	if ctx.Err() != nil {
		return 0
	}
	var catalog *config.Catalog
	if *catalogPath != "" {
		catalog, err = config.LoadCatalog(*catalogPath)
		if err != nil {
			fmt.Fprintln(stderr, "dashboard catalog is invalid")
			return 1
		}
	}
	templates, err := config.LoadPublicDirWithCatalog(*agents, catalog)
	if err != nil {
		fmt.Fprintln(stderr, "dashboard public configuration is invalid")
		return 1
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
		return 1
	}
	defer st.Close()
	handler, err := dashboard.New(dashboard.Options{DB: st.DB(), Agents: selected, Now: time.Now, Logger: logger})
	if err != nil {
		fmt.Fprintln(stderr, "dashboard public configuration is invalid")
		return 1
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", *listen)
	if err != nil {
		fmt.Fprintln(stderr, "dashboard loopback listener is unavailable")
		return 1
	}
	defer listener.Close()
	if err = dashboard.Serve(ctx, listener, handler); err != nil {
		fmt.Fprintln(stderr, "dashboard server is unavailable")
		return 1
	}
	return 0
}
