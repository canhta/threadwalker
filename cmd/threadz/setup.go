package main

import (
	"flag"
	"io"
	"log/slog"
	"strings"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/logging"
)

// envOr returns the trimmed environment value, else the fallback. A flag
// parsed later overrides both.
func envOr(getenv func(string) string, name, fallback string) string {
	if v := strings.TrimSpace(getenv(name)); v != "" {
		return v
	}
	return fallback
}

// commonFlags are the flags every command that reads configuration shares.
type commonFlags struct{ agents, db *string }

// addCommonFlags registers --agents and --db (with shorthands). The
// agents directory defaults to templates/; THREADZ_AGENTS wins over it.
func addCommonFlags(fs *flag.FlagSet, getenv func(string) string, dbFallback string) commonFlags {
	c := commonFlags{
		agents: fs.String("agents", envOr(getenv, "THREADZ_AGENTS", "templates"), "agent template directory"),
		db:     fs.String("db", envOr(getenv, "THREADZ_DB", dbFallback), "SQLite database path"),
	}
	fs.StringVar(c.agents, "a", *c.agents, "shorthand for --agents")
	fs.StringVar(c.db, "d", *c.db, "shorthand for --db")
	return c
}

// load reads the templates under --agents.
func (c commonFlags) load(opts config.Options) ([]config.Template, error) {
	return config.Load(*c.agents, opts)
}

// soleAgentID names the only agent template under --agents, so a host that runs
// one agent can leave --agent out. None or several stay an error: the command
// would otherwise act on an agent the caller never named.
func (c commonFlags) soleAgentID() (string, bool) {
	templates, err := c.load(config.Options{Public: true})
	if err != nil || len(templates) != 1 {
		return "", false
	}
	return templates[0].ID, true
}

// logFlags are --log-level and --log-format.
type logFlags struct{ level, format *string }

func addLogFlags(fs *flag.FlagSet) logFlags {
	return logFlags{
		level:  fs.String("log-level", "info", "operational log level: debug, info, warn or error"),
		format: fs.String("log-format", "json", "operational log format: json or text"),
	}
}

func (l logFlags) logger(w io.Writer) (*slog.Logger, error) {
	return logging.New(w, *l.level, *l.format)
}
