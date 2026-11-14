package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"github.com/canhta/threadz/internal/stdx"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/canhta/threadz/internal/admin"
	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/api"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/telegram"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/trends"
)

func runRuntime(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	common := addCommonFlags(fs, getenv, "")
	once := fs.Bool("once", false, "execute at most one due wake")
	offline := fs.Bool("offline", false, "use only a synthetic template and in-process fake services; no environment credentials")
	socket := fs.String("socket", defaultSocketPath(getenv), "admin control socket; not served with --once, and with --offline only when given")
	apiListen := fs.String("api-listen", "", "numeric loopback address of the OAuth and webhook server; empty disables it")
	apiURL := fs.String("api-url", getenv("THREADZ_API_URL"), "public https origin the Meta app points at; required with -api-listen, defaults to THREADZ_API_URL")
	logOpts := addLogFlags(fs)
	help, parseErr := parseFlags(fs, args)
	if help {
		writeUsage(stdout, "run")
		return exitOK
	}
	if parseErr != nil || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "invalid run arguments")
		writeUsage(stderr, "run")
		return exitUsage
	}
	logger, err := logOpts.logger(stdout)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if *common.db == "" {
		if *offline {
			dir, err := os.MkdirTemp("", "threadz-offline-*")
			if err != nil {
				fmt.Fprintln(stderr, "creating isolated offline directory failed")
				return exitFailure
			}
			defer os.RemoveAll(dir)
			*common.db = filepath.Join(dir, "offline.db")
		} else {
			*common.db = "data/threadz.db"
		}
	}
	explicitConfig := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "agents" || f.Name == "a" {
			explicitConfig = true
		}
	})
	var templates []config.Template
	if *offline && !explicitConfig {
		templates = []config.Template{agent.OfflineTemplate()}
	} else {
		resolveEnv := getenv
		if *offline {
			resolveEnv = func(string) string { return "offline-dummy-key" }
		}
		templates, err = common.load(config.Options{Getenv: resolveEnv})
		if err != nil {
			fmt.Fprintln(stderr, "loading runtime templates failed")
			return exitFailure
		}
	}
	if err := validateTemplates(templates, *offline); err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	ownUsernames := []string{}
	for _, tmpl := range templates {
		if tmpl.Threads.Username != "" {
			ownUsernames = append(ownUsernames, tmpl.Threads.Username)
		}
	}
	dbCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	st, err := store.Open(dbCtx, *common.db)
	cancel()
	if err != nil {
		fmt.Fprintln(stderr, "opening runtime database failed")
		return exitFailure
	}
	defer st.Close()
	// Default offline operation uses a fixed logical clock; waiting advances it
	// locally, with no wall-clock sleep. -once remains a single bounded wake.
	now := time.Now
	wait := stdx.Sleep
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
		return exitFailure
	}
	redactor := config.NewRedactor(templates)
	for _, tmpl := range templates {
		if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
			fmt.Fprintln(stderr, "initializing runtime agent failed")
			return exitFailure
		}
	}
	tokens, err := newThreadsTokens(st, templates, redactor, now, logger)
	if err != nil {
		fmt.Fprintln(stderr, "configuring threads tokens failed")
		return exitFailure
	}
	var oauth *api.Server
	cookies := newCookieImporter(templates, st.WakeOpen, st.ResumePausedAgent, now)
	if *apiListen != "" {
		if oauth, err = newAPIServer(st, tokens, templates, redactor, *apiURL, getenv, now, logger); err != nil {
			fmt.Fprintln(stderr, err)
			return exitFailure
		}
	}
	runtimes := []*agent.Runtime{}
	client := &http.Client{}
	for _, tmpl := range templates {
		if *offline {
			tmpl.LLM.BaseURL = "https://fixture.invalid"
			tmpl.LLM.APIKey = config.NewSecret("offline-dummy-key")
			tmpl.WebSearch.BaseURL = "https://fixture.invalid"
		}
		opts := agent.Options{Tokens: tokens, EvidenceDir: filepath.Join(filepath.Dir(*common.db), "evidence"), Store: st, Template: tmpl, OwnUsernames: ownUsernames, Logger: logger, Now: now, Wait: wait, Redactor: redactor, SearchWeb: trends.NewWebSearcher(client), TokenMissing: tokenMissingNotice(st, oauth, now, logger)}
		opts.ProfileHeld = cookies.Held
		if owner := bindings[tmpl.ID]; owner != nil {
			opts.Owner = owner
		}
		if *offline {
			fixture := agent.NewOfflineFixture(now)
			opts.HTTPClient = fixture.Client()
			opts.FetchTrends = fixture.Trends
			opts.SearchWeb = trends.NewWebSearcher(fixture.Client())
		} else {
			opts.FetchTrends = func(ctx context.Context) ([]trends.Trend, error) {
				return trends.FetchTrends(ctx, trends.TrendsOptions{HTTPClient: client})
			}
			if len(tmpl.Trends.Sources) > 0 {
				sources := trends.SourcesFromTemplate(tmpl)
				sources.HTTPClient = client
				opts.FetchTrends = func(ctx context.Context) ([]trends.Trend, error) {
					feed, err := trends.FetchSources(ctx, sources)
					return feed.Items, err
				}
			}
		}
		runtime, err := agent.New(opts)
		if err != nil {
			fmt.Fprintln(stderr, "configuring runtime failed")
			return exitFailure
		}
		runtimes = append(runtimes, runtime)
	}
	// Owner polling has its own lifetime. A wake, browser cleanup or scheduler
	// error must never cancel the Telegram poller through a shared cancel func;
	// services stop only when this process is deliberately shutting them down.
	serviceCtx, stopServices := context.WithCancel(ctx)
	var serviceWG sync.WaitGroup
	for _, svc := range services {
		svc.SetCookieImport(cookies.Import)
		serviceWG.Add(1)
		go func(svc *telegram.Service) { defer serviceWG.Done(); _ = svc.Run(serviceCtx) }(svc)
	}
	if oauth != nil {
		if err = serveAPI(serviceCtx, &serviceWG, oauth, *apiListen); err != nil {
			stopServices()
			serviceWG.Wait()
			fmt.Fprintln(stderr, err)
			return exitFailure
		}
	}
	control := agent.NewControl()
	socketGiven := false
	fs.Visit(func(f *flag.Flag) { socketGiven = socketGiven || f.Name == "socket" })
	if !*once && (!*offline || socketGiven) {
		byID := map[string]*agent.Runtime{}
		for i, tmpl := range templates {
			byID[tmpl.ID] = runtimes[i]
		}
		listener, listenErr := admin.Listen(*socket)
		if listenErr != nil {
			stopServices()
			serviceWG.Wait()
			fmt.Fprintln(stderr, listenErr)
			return exitFailure
		}
		service := &adminService{templates: templates, runtimes: byID, control: control, store: st, oauth: oauth, cookies: cookies, deps: ctlDeps{httpClient: &http.Client{}, freeBytes: freeDiskBytes, now: now}}
		serviceWG.Add(1)
		go func() { defer serviceWG.Done(); _ = admin.Serve(serviceCtx, listener, service.Handle, logger) }()
	}
	schedulerCtx, stopScheduler := context.WithCancel(ctx)
	err = (agent.Scheduler{Agents: runtimes, Control: control, Now: now, Wait: wait}).Run(schedulerCtx, *once)
	stopScheduler()
	stopServices()
	serviceWG.Wait()
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(stderr, "runtime stopped; inspect safe event reasons")
		return exitFailure
	}
	return 0
}

// validateTemplates fails closed: every agent ID is distinct and a non-dry-run
// template must name a backend that can publish. Offline mode replaces the
// backend with the stub, so explicit templates cannot enable a browser.
func validateTemplates(templates []config.Template, offline bool) error {
	seen := map[string]bool{}
	for i := range templates {
		if offline {
			templates[i].Threads.Backend = "stub"
			templates[i].Threads.BaseURL = ""
			templates[i].Threads.ExecutablePath = ""
		}
		tmpl := templates[i]
		if seen[tmpl.ID] {
			return errors.New("runtime requires distinct agent IDs")
		}
		if _, canPublish := threads.NewBackend(tmpl, nil, nil); !tmpl.DryRun && !canPublish {
			return errors.New("runtime requires a publishing Threads backend for non-dry-run")
		}
		seen[tmpl.ID] = true
	}
	return nil
}

// newThreadsTokens returns the token manager when any template uses the api
// backend, else nil. The sealing key comes only from the environment.
func newThreadsTokens(st *store.Store, templates []config.Template, redactor *config.Redactor, now func() time.Time, logger *slog.Logger) (*threads.Tokens, error) {
	seed := map[string]config.Secret{}
	for _, t := range templates {
		if t.Threads.Backend == "api" {
			seed[t.ID] = t.Threads.AccessToken
		}
	}
	if len(seed) == 0 {
		return nil, nil
	}
	key, err := threads.DecodeKey(os.Getenv(threads.SecretKeyEnv))
	if err != nil {
		return nil, err
	}
	return threads.NewTokens(threads.TokensOptions{Slot: st, Key: key, Seed: seed, Now: now, Redact: redactor, Notify: func(ctx context.Context, agentID, text string) {
		day := now().UTC().Format("2006-01-02")
		if _, err := st.QueueOwner(ctx, agentID, "notice", text, "notice:threads_token_refresh:"+day, now()); err != nil {
			logger.Error("queueing token refresh notice failed", "agent", agentID)
		}
	}})
}

// newAPIServer wires the OAuth and webhook server for every api-backend agent.
// Its secrets come only from the environment and join the redactor.
func newAPIServer(st *store.Store, tokens *threads.Tokens, templates []config.Template, redactor *config.Redactor, publicURL string, getenv func(string) string, now func() time.Time, logger *slog.Logger) (*api.Server, error) {
	if tokens == nil {
		return nil, errors.New("api server requires an agent with the api Threads backend")
	}
	secret, verify := config.NewSecret(getenv("THREADS_APP_SECRET")), config.NewSecret(getenv("THREADS_WEBHOOK_VERIFY_TOKEN"))
	redactor.Add(secret)
	redactor.Add(verify)
	var agents []api.Agent
	for _, t := range templates {
		if t.Threads.Backend == "api" {
			agents = append(agents, api.Agent{ID: t.ID, UserID: t.Threads.UserID, Username: t.Threads.Username})
		}
	}
	srv, err := api.New(api.Options{Agents: agents, AppID: getenv("THREADS_APP_ID"), AppSecret: secret, VerifyToken: verify, PublicURL: publicURL, Store: st, Tokens: tokens, Now: now, Rand: rand.Reader, Logger: logger})
	if err != nil {
		return nil, errors.New("configuring api server failed; set THREADS_APP_ID, THREADS_APP_SECRET and THREADS_WEBHOOK_VERIFY_TOKEN")
	}
	return srv, nil
}

// serveAPI listens on a numeric loopback address and serves until ctx ends.
func serveAPI(ctx context.Context, wg *sync.WaitGroup, handler http.Handler, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		return errors.New("api server requires a numeric loopback listen address")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return errors.New("api server cannot listen on the given address")
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	wg.Add(1)
	go func() {
		defer wg.Done()
		go func() { _ = srv.Serve(ln) }()
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	return nil
}

// tokenMissingNotice tells the owner, once a day, that an agent has no Threads
// token, with a fresh authorization link when the api server runs.
func tokenMissingNotice(st *store.Store, oauth *api.Server, now func() time.Time, logger *slog.Logger) func(context.Context, string) {
	return func(ctx context.Context, agentID string) {
		text := "Threads token missing; the agent skips its wakes. Set THREADS_ACCESS_TOKEN or start the service with -api-listen to authorize."
		if oauth != nil {
			if link, err := oauth.NewLink(agentID); err == nil {
				text = "Threads token missing; the agent skips its wakes. Authorize Threads (one use, 15 minutes): " + link
			}
		}
		day := now().UTC().Format(time.DateOnly)
		if _, err := st.QueueOwner(ctx, agentID, "notice", text, "notice:threads_token_missing:"+day, now()); err != nil {
			logger.Error("queueing missing token notice failed", "agent", agentID)
		}
	}
}
