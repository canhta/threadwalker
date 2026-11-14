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

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/dashboard"
	"github.com/canhta/threadz/internal/store"
)

func runDashboard(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	common := addCommonFlags(fs, getenv, "data/threadz.db")
	listen := fs.String("listen", "127.0.0.1:8080", "numeric loopback listen address")
	content := fs.Bool("content", false, "show private notes and lessons")
	rate := fs.Float64("rate", 0, "per-client requests per second (0 uses the safe default)")
	burst := fs.Int("burst", 0, "per-client burst (0 uses the safe default)")
	maxConcurrent := fs.Int("max-concurrent", 0, "concurrent renders (0 uses the safe default)")
	logOpts := addLogFlags(fs)
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
	logger, err := logOpts.logger(stdout)
	if err != nil {
		fmt.Fprintln(stderr, "invalid dashboard logging options")
		return exitUsage
	}
	if ctx.Err() != nil {
		return 0
	}
	templates, err := common.load(config.Options{Public: true})
	if err != nil {
		fmt.Fprintln(stderr, "dashboard public configuration is invalid")
		return exitFailure
	}
	selected := make([]dashboard.Agent, 0, len(templates))
	for _, t := range templates {
		tz := t.Telegram.Timezone
		if tz == "" {
			tz = t.Schedule.Timezone
		}
		selected = append(selected, dashboard.Agent{ID: t.ID, Username: t.Threads.Username, DryRun: t.DryRun, DailyBudgetUSD: t.Limits.MaxUSDPerDay, Timezone: tz, Limits: t.Limits})
	}
	var token string
	if *content {
		token = strings.TrimSpace(getenv("THREADZ_DASHBOARD_TOKEN"))
		if token == "" {
			fmt.Fprintln(stderr, "dashboard content mode requires THREADZ_DASHBOARD_TOKEN")
			return exitFailure
		}
	}
	openCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	st, err := store.OpenReadOnly(openCtx, *common.db)
	cancel()
	if err != nil {
		fmt.Fprintln(stderr, "dashboard database is unavailable; initialize it with check first")
		return exitFailure
	}
	defer st.Close()
	options := dashboard.Options{Source: st, Agents: selected, Now: time.Now, Logger: logger, Content: *content, ContentToken: token, RatePerSec: *rate, RateBurst: *burst, MaxConcurrent: *maxConcurrent}
	handler, err := dashboard.New(options)
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
