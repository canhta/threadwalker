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

const usageText = `threadz — one autonomous Threads agent, no approvals

Usage:
  threadz <command> [flags]

Commands:
  check      validate agent templates and the database
  run        run the scheduler (add --once for a single wake)
  dashboard  serve the read-only local dashboard
  ctl        operate the running service: status, wake, pause, context, wakes, logs, doctor
  update     install the latest signed release, with rollback (root)
  eval       run the offline evaluation suite, or the offline metrics and soak simulations
  help       print this help, or help for one command
  version    print the build version

Common flags (same names on every command):
  --agents <dir>   agent template directory (default: templates/)
  --db <path>      SQLite database path (default: data/threadz.db)

Environment:
  THREADZ_AGENTS and THREADZ_DB set defaults.
  A flag beats the environment variable; the environment beats the default.
  THREADZ_ENV_FILE names the env file (default: .env when present).
  The env file never overrides a variable that is already set.

Examples:
  threadz check
  threadz run --once --offline
  threadz dashboard
  threadz ctl status
  threadz eval --suite internal/eval/testdata/default.json
`

// buildVersion is stamped by the release build; local builds keep "dev".
var buildVersion = "dev"

// versionText returns the build version, falling back to "dev" for a local build.
func versionText() string {
	if buildVersion != "" && buildVersion != "dev" {
		return buildVersion
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

// defaultEnvFile names the optional env file; THREADZ_ENV_FILE overrides.
func defaultEnvFile(getenv func(string) string) string {
	return envOr(getenv, "THREADZ_ENV_FILE", ".env")
}

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
	"check": `usage: threadz check [--agents DIR] [--db PATH]

Validate every agent template without calling out.

Examples:
  threadz check
  threadz check --agents agents
`,
	"run": `usage: threadz run [--agents DIR] [--db PATH] [--socket PATH] [--once] [--offline]

Run the serialized scheduler. --once executes at most one due wake. --offline
uses synthetic services and no credentials; its default template is built in.

Examples:
  threadz run --once --offline
  threadz run --agents templates --db data/threadz.db --socket data/admin.sock
  
`,
	"ctl":    ctlUsage,
	"update": updateUsage,
	"dashboard": `usage: threadz dashboard [--agents DIR] [--db PATH] [--listen ADDR] [--content]
                         [--rate N] [--burst N] [--max-concurrent N]

Serve the read-only dashboard on a numeric loopback address.

Example:
  threadz dashboard --listen 127.0.0.1:8080
`,
	"eval": `usage: threadz eval [--suite FILE] [--out FILE] [--compare FILE] [--llms FILE]
                        [--profiles IDS] [--cases IDS] [--seed N] [--repeat N] [--live]

Run the offline evaluation suite. --live requires explicit profiles, cases and caps.

       threadz eval metrics [--soak-hours 48..720] [--evidence DIR]
       threadz eval soak [--soak-hours 48..720] [--soak-agents 1|2] [--evidence DIR]

metrics reproduces the offline metric-collection simulation; soak runs the
offline runtime (one agent) or isolation (two agents) soak. Both use synthetic
services and logical time, and their suites are built into the binary.

Example:
  threadz eval --suite internal/eval/testdata/default.json
  threadz eval soak --soak-hours 48 --soak-agents 2
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
