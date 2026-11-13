package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/telegram"
)

// minFreeDiskBytes is the least free space beside the database before doctor
// fails: a wake plus a prune's working set must fit.
const minFreeDiskBytes = 512 << 20

const checkTimeout = 10 * time.Second

// healthResult is one row of the doctor report and of the status health list.
type healthResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// healthCheck is one row of the single health table. Doctor runs every row;
// status runs the rows marked Live for each agent.
type healthCheck struct {
	Name string
	Live bool
	Run  func(ctx context.Context) (string, error)
}

// ctlDeps are the outside effects the operator commands use, injected so the
// commands run against fake transports.
type ctlDeps struct {
	httpClient *http.Client
	freeBytes  func(path string) (uint64, error)
	now        func() time.Time
	journal    func(ctx context.Context, args []string) (io.ReadCloser, error)
	// stdin feeds `ctl threads cookies`; nil means os.Stdin.
	stdin io.Reader
}

func runChecks(ctx context.Context, checks []healthCheck, liveOnly bool) []healthResult {
	var results []healthResult
	for _, c := range checks {
		if liveOnly && !c.Live {
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
		detail, err := c.Run(checkCtx)
		cancel()
		if err != nil {
			results = append(results, healthResult{Name: c.Name, Detail: err.Error()})
			continue
		}
		results = append(results, healthResult{Name: c.Name, OK: true, Detail: detail})
	}
	return results
}

// agentChecks are the live rows for one agent.
func agentChecks(deps ctlDeps, t config.Template) []healthCheck {
	return []healthCheck{
		{Name: "browser:" + t.ID, Live: true, Run: func(context.Context) (string, error) {
			switch t.Threads.Backend {
			case "", "stub":
				return "stub backend", nil
			}
			if info, err := os.Stat(t.Threads.ExecutablePath); err != nil || info.IsDir() {
				return "", errors.New("browser executable not found")
			}
			if info, err := os.Stat(t.Threads.ProfileDir); err != nil || !info.IsDir() {
				return "", errors.New("browser profile directory not found")
			}
			return "feed browser profile present", nil
		}},
		{Name: "telegram:" + t.ID, Live: true, Run: func(ctx context.Context) (string, error) {
			if !t.Telegram.Enabled {
				return "disabled", nil
			}
			client, err := telegram.NewClient(telegram.ClientOptions{Token: t.Telegram.BotToken, HTTPClient: deps.httpClient})
			if err != nil {
				return "", errors.New("bot token is not valid")
			}
			if err = client.GetMe(ctx); err != nil {
				return "", err
			}
			return "getMe accepted", nil
		}},
	}
}

// deploymentChecks are the rows that cover the whole deployment.
func deploymentChecks(deps ctlDeps, getenv func(string) string, templates []config.Template, dbPath string) []healthCheck {
	checks := []healthCheck{
		{Name: "env_keys", Run: func(context.Context) (string, error) {
			var missing, present []string
			for _, name := range requiredEnvNames(templates) {
				if strings.TrimSpace(getenv(name)) == "" {
					missing = append(missing, name)
				} else {
					present = append(present, name)
				}
			}
			if len(missing) > 0 {
				return "", fmt.Errorf("not set: %s", strings.Join(missing, ", "))
			}
			return "set: " + strings.Join(present, ", "), nil
		}},
		{Name: "db_schema", Run: func(ctx context.Context) (string, error) {
			st, err := store.OpenReadOnly(ctx, dbPath)
			if err != nil {
				return "", errors.New("database is unavailable")
			}
			defer st.Close()
			stored, expected, err := st.SchemaVersion(ctx)
			if err != nil {
				return "", err
			}
			if stored != expected {
				return "", fmt.Errorf("schema version %d, this build needs %d", stored, expected)
			}
			return fmt.Sprintf("schema version %d", stored), nil
		}},
		{Name: "disk", Run: func(context.Context) (string, error) {
			free, err := deps.freeBytes(dbPath)
			if err != nil {
				return "", fmt.Errorf("reading free disk space: %w", err)
			}
			size := int64(0)
			if info, statErr := os.Stat(dbPath); statErr == nil {
				size = info.Size()
			}
			detail := fmt.Sprintf("%d MiB free, database %d MiB", free>>20, size>>20)
			if free < minFreeDiskBytes {
				return "", errors.New(detail + ", below the minimum")
			}
			return detail, nil
		}},
		{Name: "retention", Run: func(ctx context.Context) (string, error) {
			st, err := store.OpenReadOnly(ctx, dbPath)
			if err != nil {
				return "", errors.New("database is unavailable")
			}
			defer st.Close()
			overdue, err := st.OverdueRetention(ctx, deps.now())
			if err != nil {
				return "", err
			}
			var late []string
			for _, o := range overdue {
				if o.Deleted > 0 {
					late = append(late, fmt.Sprintf("%s %d", o.Table, o.Deleted))
				}
			}
			if len(late) > 0 {
				return "", fmt.Errorf("rows past their retention: %s", strings.Join(late, ", "))
			}
			return "no rows past their retention", nil
		}},
	}
	for _, t := range templates {
		checks = append(checks, agentChecks(deps, t)...)
	}
	return checks
}

// requiredEnvNames lists the environment variables the templates need, names only.
func requiredEnvNames(templates []config.Template) []string {
	seen := map[string]bool{}
	for _, t := range templates {
		names := []string{t.LLM.APIKeyEnv, config.EmbeddingKeyEnv}
		if t.Telegram.Enabled {
			names = append(names, t.Telegram.BotTokenEnv, t.Telegram.OwnerChatIDEnv)
		}
		for _, n := range names {
			if n != "" {
				seen[n] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
