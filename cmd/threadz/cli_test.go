package main

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errs bytes.Buffer
	code = run(context.Background(), args, func(string) string { return "" }, &out, &errs)
	return code, out.String(), errs.String()
}

func TestHelpPrintsUsageOnStdout(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		code, stdout, stderr := runCLI(t, args...)
		if code != exitOK {
			t.Errorf("%v exit = %d, want %d; stderr=%s", args, code, exitOK, stderr)
		}
		if !strings.Contains(stdout, "Usage:") || !strings.Contains(stdout, "threadz <command>") {
			t.Errorf("%v stdout does not contain the usage text: %q", args, stdout)
		}
		if stderr != "" {
			t.Errorf("%v wrote help to stderr: %q", args, stderr)
		}
	}
}

func TestCommandHelp(t *testing.T) {
	for _, command := range []string{"check", "run", "dashboard", "eval"} {
		code, stdout, _ := runCLI(t, command, "--help")
		if code != exitOK {
			t.Errorf("%s --help exit = %d, want %d", command, code, exitOK)
		}
		if !strings.Contains(stdout, "usage: threadz "+command) {
			t.Errorf("%s --help stdout = %q, want its usage", command, stdout)
		}
	}
	code, stdout, _ := runCLI(t, "help", "run")
	if code != exitOK || !strings.Contains(stdout, "usage: threadz run") {
		t.Errorf("help run = %d, %q", code, stdout)
	}
}

func TestVersion(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}} {
		code, stdout, _ := runCLI(t, args...)
		if code != exitOK {
			t.Errorf("%v exit = %d, want %d", args, code, exitOK)
		}
		if !strings.HasPrefix(stdout, "threadz ") || !strings.HasSuffix(stdout, "\n") {
			t.Errorf("%v stdout = %q", args, stdout)
		}
	}
}

func TestUsageErrorsExitTwoWithUsageOnStderr(t *testing.T) {
	cases := [][]string{
		{},
		{"unknown-command"},
		{"check", "--not-a-flag"},
		{"run", "--not-a-flag"},
		{"dashboard", "--not-a-flag"},
		{"eval", "--not-a-flag"},
		{"help", "unknown-command"},
	}
	for _, args := range cases {
		code, stdout, stderr := runCLI(t, args...)
		if code != exitUsage {
			t.Errorf("%v exit = %d, want %d", args, code, exitUsage)
		}
		if !strings.Contains(stderr, "usage") && !strings.Contains(stderr, "Usage") {
			t.Errorf("%v stderr has no usage text: %q", args, stderr)
		}
		if len(args) > 0 && args[0] != "check" && args[0] != "run" && args[0] != "dashboard" && args[0] != "eval" {
			if strings.Contains(stdout, "Usage:") {
				t.Errorf("%v wrote usage to stdout on error", args)
			}
		}
	}
}

func parseCommon(t *testing.T, env map[string]string, args ...string) commonFlags {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	c := addCommonFlags(fs, func(k string) string { return env[k] }, "data/threadz.db")
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDefaultAgentsDir(t *testing.T) {
	if got := *parseCommon(t, nil).agents; got != "templates" {
		t.Errorf("agents = %q, want templates", got)
	}
	if got := *parseCommon(t, map[string]string{"THREADZ_AGENTS": " /x "}).agents; got != "/x" {
		t.Errorf("agents = %q, want the environment value", got)
	}
}

func TestFlagBeatsEnvironmentBeatsDefault(t *testing.T) {
	env := map[string]string{"THREADZ_DB": "/env.db"}
	if c := parseCommon(t, nil); *c.db != "data/threadz.db" {
		t.Errorf("default = %q", *c.db)
	}
	if c := parseCommon(t, env); *c.db != "/env.db" {
		t.Errorf("environment = %q", *c.db)
	}
	if c := parseCommon(t, env, "-d", "/flag.db"); *c.db != "/flag.db" {
		t.Errorf("flag = %q", *c.db)
	}
}

func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := strings.Join([]string{
		"# comment",
		"",
		"THREADZ_TEST_A=one",
		"export THREADZ_TEST_B='two words'",
		`THREADZ_TEST_C="quoted value"`,
		"THREADZ_TEST_A=ignored-because-set",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	existing := map[string]string{"THREADZ_TEST_A": "already-set"}
	set := map[string]string{}
	loaded, err := loadEnvFile(path, func(k string) string { return existing[k] }, func(k, v string) error {
		set[k] = v
		return nil
	})
	if err != nil {
		t.Fatalf("loadEnvFile: %v", err)
	}
	if loaded != 2 {
		t.Errorf("loaded = %d, want the two unset variables", loaded)
	}
	if set["THREADZ_TEST_A"] != "" {
		t.Errorf("loadEnvFile overrode an already-set variable: %q", set["THREADZ_TEST_A"])
	}
	if set["THREADZ_TEST_B"] != "two words" || set["THREADZ_TEST_C"] != "quoted value" {
		t.Errorf("loadEnvFile set %v, want unquoted values", set)
	}
}

func TestLoadEnvFileMissingIsNotAnError(t *testing.T) {
	loaded, err := loadEnvFile(filepath.Join(t.TempDir(), "missing.env"), func(string) string { return "" }, func(string, string) error { return nil })
	if err != nil || loaded != 0 {
		t.Fatalf("loadEnvFile(missing) = %d, %v", loaded, err)
	}
}

func TestLoadEnvFileRejectsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("not a variable\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadEnvFile(path, func(string) string { return "" }, func(string, string) error { return nil }); err == nil {
		t.Fatal("loadEnvFile accepted a malformed line")
	}
	if err := os.WriteFile(path, []byte("BAD-NAME=value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadEnvFile(path, func(string) string { return "" }, func(string, string) error { return nil }); err == nil {
		t.Fatal("loadEnvFile accepted an invalid variable name")
	}
}

// noSecretEnv answers operator settings (THREADZ_*) and fails the test on
// any other lookup, such as a credential.
func noSecretEnv(t *testing.T, msg string) func(string) string {
	return func(k string) string {
		if !strings.HasPrefix(k, "THREADZ_") {
			t.Fatal(msg)
		}
		return ""
	}
}
