package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
)

// Exit codes follow the CLI convention: 0 success, 1 runtime failure, 2 usage.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

const usageText = `threadwalker — one autonomous Threads agent, no approvals

Usage:
  threadwalker <command> [flags]

Commands:
  check      validate agent templates, optional catalog and the database
  run        run the scheduler (add --once for a single wake)
  dashboard  serve the read-only local activity page
  metrics    reproduce the offline metric-collection simulation
  eval       run the offline evaluation suite
  help       print this help, or help for one command
  version    print the build version

Common flags (same names on every command):
  --agents <dir>   agent template directory (default: templates/, else agents/)
  --db <path>      SQLite database path (default: data/threadwalker.db)
  --llms <path>    optional LLM provider/model catalog

Environment:
  THREADWALKER_AGENTS, THREADWALKER_DB, THREADWALKER_LLMS set defaults.
  A flag beats the environment variable; the environment beats the default.
  THREADWALKER_ENV_FILE names the env file (default: .env when present).
  The env file never overrides a variable that is already set.

Examples:
  threadwalker check
  threadwalker run --once --offline
  threadwalker dashboard
  threadwalker eval --suite internal/eval/testdata/default.json
`

// versionText returns the build version, falling back to "dev" for a local build.
func versionText() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

// envOr returns the operator environment value, else the built-in default.
// A flag parsed later overrides both.
func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

// defaultAgentsDir prefers the live template directory and keeps the shipped
// examples usable in a fresh checkout. THREADWALKER_AGENTS wins over both.
func defaultAgentsDir() string {
	if v := strings.TrimSpace(os.Getenv("THREADWALKER_AGENTS")); v != "" {
		return v
	}
	if info, err := os.Stat("templates"); err == nil && info.IsDir() {
		return "templates"
	}
	return "agents"
}

func defaultDBPath() string  { return envOr("THREADWALKER_DB", "data/threadwalker.db") }
func defaultLLMsPath() string { return strings.TrimSpace(os.Getenv("THREADWALKER_LLMS")) }

// defaultEnvFile names the optional env file; THREADWALKER_ENV_FILE overrides.
func defaultEnvFile() string { return envOr("THREADWALKER_ENV_FILE", ".env") }

// loadEnvFile reads KEY=VALUE lines and sets only variables that are unset.
// Values are never printed or logged; a malformed line reports its number only.
func loadEnvFile(path string, lookup func(string) string, set func(string, string) error) (int, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("opening env file %s: %w", path, err)
	}
	defer f.Close()

	loaded := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			return loaded, fmt.Errorf("env file %s line %d: expected KEY=VALUE", path, lineNo)
		}
		key := strings.TrimSpace(line[:eq])
		if !validEnvName(key) {
			return loaded, fmt.Errorf("env file %s line %d: invalid variable name", path, lineNo)
		}
		value := strings.TrimSpace(line[eq+1:])
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		if lookup(key) != "" {
			continue
		}
		if err = set(key, value); err != nil {
			return loaded, fmt.Errorf("env file %s line %d: setting a variable failed", path, lineNo)
		}
		loaded++
	}
	if err = scanner.Err(); err != nil {
		return loaded, fmt.Errorf("reading env file %s: %w", path, err)
	}
	return loaded, nil
}

func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '_':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// commandUsage is the per-command help text printed by help, --help and -h.
var commandUsage = map[string]string{
	"check": `usage: threadwalker check [--agents DIR] [--llms FILE] [--db PATH]

Validate every agent template (and optional catalog) without calling out.

Examples:
  threadwalker check
  threadwalker check --agents agents --llms llms.yaml
`,
	"run": `usage: threadwalker run [--agents DIR] [--llms FILE] [--db PATH] [--once] [--offline]

Run the serialized scheduler. --once executes at most one due wake. --offline
uses synthetic services and no credentials; its default template is built in.

Examples:
  threadwalker run --once --offline
  threadwalker run --agents templates --db data/threadwalker.db
  threadwalker run --offline --soak-hours 48 --soak-agents 2 --evidence /tmp/iso
`,
	"dashboard": `usage: threadwalker dashboard [--agents DIR] [--llms FILE] [--db PATH] [--listen ADDR]

Serve the read-only activity page on a numeric loopback address.

Example:
  threadwalker dashboard --listen 127.0.0.1:8080
`,
	"metrics": `usage: threadwalker metrics --offline [--soak-hours 48..720] [--evidence DIR]

Reproduce the offline metric-collection simulation (no wall-clock soak claim).
`,
	"eval": `usage: threadwalker eval [--suite FILE] [--out FILE] [--compare FILE] [--llms FILE]
                        [--profiles IDS] [--cases IDS] [--seed N] [--repeat N] [--live]

Run the offline evaluation suite. --live requires explicit profiles, cases and caps.

Example:
  threadwalker eval --suite internal/eval/testdata/default.json
`,
}

func commandHelp(name string) string {
	if text, ok := commandUsage[name]; ok {
		return text
	}
	return usageText
}

// writeUsage prints command help to w (stdout for help, stderr for an error).
func writeUsage(w io.Writer, command string) {
	if command == "" {
		fmt.Fprint(w, usageText)
		return
	}
	fmt.Fprint(w, commandHelp(command))
}
