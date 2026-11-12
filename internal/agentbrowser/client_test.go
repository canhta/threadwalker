package agentbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

const fakeProcessEnv = "THREADWALKER_FAKE_AGENT_BROWSER"

// TestAgentBrowserFakeProcess is the fake executable body. It is inert during a
// normal test run and exits before the testing framework prints anything.
func TestAgentBrowserFakeProcess(t *testing.T) {
	if os.Getenv(fakeProcessEnv) != "1" {
		return
	}
	runFakeProcess()
	os.Exit(0)
}

type fakeProgram struct {
	Version  string                 `json:"version"`
	Commands map[string]fakeCommand `json:"commands"`
}

type fakeCommand struct {
	Stdout  string `json:"stdout"`
	Stderr  string `json:"stderr"`
	Exit    int    `json:"exit"`
	SleepMS int    `json:"sleep_ms"`
	Huge    int    `json:"huge"`
}

type fakeRecord struct {
	Argv []string          `json:"argv"`
	Env  map[string]string `json:"env"`
	PID  int               `json:"pid"`
}

func runFakeProcess() {
	programPath := os.Getenv("THREADWALKER_FAKE_PROGRAM")
	recordPath := os.Getenv("THREADWALKER_FAKE_RECORD")
	raw, err := os.ReadFile(programPath)
	if err != nil {
		os.Exit(90)
	}
	var program fakeProgram
	if err = json.Unmarshal(raw, &program); err != nil {
		os.Exit(91)
	}
	record := fakeRecord{Argv: os.Args[1:], Env: map[string]string{}, PID: os.Getpid()}
	for _, item := range os.Environ() {
		if key, value, ok := strings.Cut(item, "="); ok {
			record.Env[key] = value
		}
	}
	if recordPath != "" {
		if file, e := os.OpenFile(recordPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600); e == nil {
			encoded, _ := json.Marshal(record)
			_, _ = file.Write(append(encoded, '\n'))
			_ = file.Close()
		}
	}
	command := fakeCommandName(record.Argv)
	if command == "version" {
		fmt.Println(program.Version)
		return
	}
	entry, ok := program.Commands[command]
	if !ok {
		os.Exit(92)
	}
	if entry.SleepMS > 0 {
		time.Sleep(time.Duration(entry.SleepMS) * time.Millisecond)
	}
	if entry.Stderr != "" {
		_, _ = os.Stderr.WriteString(entry.Stderr)
	}
	if entry.Huge > 0 {
		chunk := strings.Repeat("x", 4096)
		for written := 0; written < entry.Huge; written += len(chunk) {
			if _, err = os.Stdout.WriteString(chunk); err != nil {
				break
			}
		}
	}
	if entry.Stdout != "" {
		_, _ = os.Stdout.WriteString(entry.Stdout)
	}
	os.Exit(entry.Exit)
}

func fakeCommandName(argv []string) string {
	valueFlags := map[string]bool{
		"--session": true, "--namespace": true, "--profile": true,
		"--executable-path": true, "--config": true, "--idle-timeout": true,
		"--args": true, "--user-agent": true, "--proxy": true,
	}
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--version" || arg == "-V" {
			return "version"
		}
		if valueFlags[arg] {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if arg == "session" && i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
			return "session " + argv[i+1]
		}
		return arg
	}
	return ""
}

func fakeExecutable(t *testing.T, program fakeProgram) (binary, record string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake executable wrapper is a POSIX shell script")
	}
	dir := t.TempDir()
	programPath := filepath.Join(dir, "program.json")
	raw, err := json.Marshal(program)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(programPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	record = filepath.Join(dir, "record.jsonl")
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nTHREADWALKER_FAKE_AGENT_BROWSER=1 THREADWALKER_FAKE_PROGRAM=%q THREADWALKER_FAKE_RECORD=%q exec %q -test.run=TestAgentBrowserFakeProcess -- \"$@\"\n", programPath, record, testBinary)
	binary = filepath.Join(dir, "agent-browser")
	if err = os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return binary, record
}

func testClient(t *testing.T, program fakeProgram, mutate func(*Options)) (*Client, string) {
	t.Helper()
	binary, record := fakeExecutable(t, program)
	chrome := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(chrome, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	opts := Options{BinaryPath: binary, ChromePath: chrome, ProfileDir: t.TempDir(), Session: "tw-agent", Namespace: "tw-ns", Timeout: 5 * time.Second, StartupTimeout: 5 * time.Second}
	if mutate != nil {
		mutate(&opts)
	}
	client, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	return client, record
}

func success(data string) string {
	return `{"success":true,"data":` + data + `,"error":null}`
}

func fakeProgramFor(commands map[string]fakeCommand) fakeProgram {
	if _, ok := commands["open"]; !ok {
		commands["open"] = fakeCommand{Stdout: success(`{"launched":true}`)}
	}
	if _, ok := commands["session info"]; !ok {
		commands["session info"] = fakeCommand{Stdout: success(`{"pid":1,"active":true}`)}
	}
	if _, ok := commands["session list"]; !ok {
		commands["session list"] = fakeCommand{Stdout: success(`{"sessions":[]}`)}
	}
	return fakeProgram{Version: "agent-browser " + Version, Commands: commands}
}

func readRecords(t *testing.T, path string) []fakeRecord {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var records []fakeRecord
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var record fakeRecord
		if err = json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

func TestClientFixedCommandsAndEnvironment(t *testing.T) {
	t.Setenv("PARENT_SECRET_TOKEN", "do-not-leak")
	t.Setenv("AGENT_BROWSER_PROFILE", "/parent/profile")
	client, record := testClient(t, fakeProgramFor(map[string]fakeCommand{
		"open":         {Stdout: success(`{"url":"https://fixture.test/feed","title":"Fixture"}`)},
		"read":         {Stdout: success(`{"content":"visible body","url":"https://fixture.test/feed","finalUrl":"https://fixture.test/feed","source":"active-tab-html","truncated":false}`)},
		"snapshot":     {Stdout: success(`{"origin":"https://fixture.test/feed","snapshot":"- main","refs":{"e1":{"name":"","role":"textbox"},"e2":{"name":"Post","role":"button"}}}`)},
		"fill":         {Stdout: success(`{"filled":"@e1"}`)},
		"focus":        {Stdout: success(`{"focused":"@e1"}`)},
		"keyboard":     {Stdout: success(`{"typed":true}`)},
		"press":        {Stdout: success(`{"pressed":"Shift+Enter"}`)},
		"click":        {Stdout: success(`{"clicked":"@e2"}`)},
		"close":        {Stdout: success(`{"closed":true}`)},
		"session list": {Stdout: success(`{"sessions":[]}`)},
	}), nil)
	ctx := context.Background()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if page, err := client.Open(ctx, "https://fixture.test/feed"); err != nil || page.URL != "https://fixture.test/feed" || page.Title != "Fixture" {
		t.Fatalf("open=%+v err=%v", page, err)
	}
	if document, err := client.Read(ctx); err != nil || document.Content != "visible body" || document.Truncated {
		t.Fatalf("read=%+v err=%v", document, err)
	}
	if snapshot, err := client.Snapshot(ctx); err != nil || len(snapshot.Refs) != 2 || snapshot.Text != "- main" {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if err := client.Fill(ctx, "e1", "--leading text"); err != nil {
		t.Fatal(err)
	}
	if err := client.Focus(ctx, "e1"); err != nil {
		t.Fatal(err)
	}
	if err := client.Type(ctx, "line one"); err != nil {
		t.Fatal(err)
	}
	if err := client.PressShiftEnter(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.Type(ctx, "line\nbreak"); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("newline accepted by Type: %v", err)
	}
	if err := client.Type(ctx, "--help"); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("option-shaped text accepted by Type: %v", err)
	}
	if err := client.Click(ctx, "e2"); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(ctx); err != nil {
		t.Fatal(err)
	}
	records := readRecords(t, record)
	var fill, open, snapshot, focus, typed, pressed []string
	for _, item := range records {
		env := item.Env
		if env["PARENT_SECRET_TOKEN"] != "" || env["AGENT_BROWSER_PROFILE"] != "" {
			t.Fatal("parent environment leaked into the CLI")
		}
		if env["AGENT_BROWSER_DEFAULT_TIMEOUT"] == "" {
			t.Fatal("action timeout not bounded")
		}
		switch fakeCommandName(item.Argv) {
		case "open":
			open = item.Argv
		case "fill":
			fill = item.Argv
		case "snapshot":
			snapshot = item.Argv
		case "focus":
			focus = item.Argv
		case "keyboard":
			typed = item.Argv
		case "press":
			pressed = item.Argv
		}
	}
	if !containsSequence(open, "--json") || !containsSequence(open, "--session", "tw-agent") || !containsSequence(open, "--namespace", "tw-ns") || !containsSequence(open, "--no-auto-connect") || !containsSequence(open, "--no-webmcp") {
		t.Fatalf("open argv missing fixed flags: %v", open)
	}
	separator := -1
	for i, arg := range fill {
		if arg == "--" {
			separator = i
		}
	}
	if separator < 0 || separator != len(fill)-2 || fill[len(fill)-1] != "--leading text" {
		t.Fatalf("fill argv does not isolate text: %v", fill)
	}
	if !containsSequence(snapshot, "snapshot", "-c", "-u") {
		t.Fatalf("snapshot argv missing compact URL flags: %v", snapshot)
	}
	if !containsSequence(focus, "focus", "e1") {
		t.Fatalf("focus argv: %v", focus)
	}
	if !containsSequence(typed, "keyboard", "type", "line one") || typed[len(typed)-1] != "line one" {
		t.Fatalf("keyboard type argv: %v", typed)
	}
	if !containsSequence(pressed, "press", "Shift+Enter") {
		t.Fatalf("press argv: %v", pressed)
	}
}

func containsSequence(values []string, sequence ...string) bool {
	for i := 0; i+len(sequence) <= len(values); i++ {
		match := true
		for j := range sequence {
			if values[i+j] != sequence[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestClientVersionMismatch(t *testing.T) {
	client, _ := testClient(t, fakeProgram{Version: "agent-browser 0.41.0"}, nil)
	if err := client.Start(context.Background()); !errors.Is(err, ErrVersion) {
		t.Fatalf("version mismatch accepted: %v", err)
	}
}

func TestClientOutputLimit(t *testing.T) {
	client, _ := testClient(t, fakeProgramFor(map[string]fakeCommand{"read": {Huge: 64 << 10}}), func(opts *Options) { opts.MaxOutputBytes = 4096 })
	if err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Read(context.Background()); !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("unbounded output accepted: %v", err)
	}
}

func TestClientProtocolAndCommandErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		stdout string
		want   error
	}{
		"bad_json":        {"not json", ErrProtocol},
		"unknown_field":   {`{"success":true,"data":{},"error":null,"extra":1}`, ErrProtocol},
		"null_data":       {`{"success":true,"data":null,"error":null}`, ErrProtocol},
		"failed_command":  {`{"success":false,"data":null,"error":"Unknown ref: e1"}`, ErrCommand},
		"error_with_data": {`{"success":true,"data":{},"error":"boom"}`, ErrCommand},
	} {
		t.Run(name, func(t *testing.T) {
			client, _ := testClient(t, fakeProgramFor(map[string]fakeCommand{"open": {Stdout: tc.stdout}}), nil)
			_, err := client.Open(context.Background(), "https://fixture.test/feed")
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "Unknown ref") {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
		})
	}
}

func TestClientTimeoutKillsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group assertion is POSIX-specific")
	}
	client, record := testClient(t, fakeProgramFor(map[string]fakeCommand{"open": {SleepMS: 30000}}), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := client.Open(ctx, "https://fixture.test/feed")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline not preserved: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("cancel took %s", elapsed)
	}
	records := readRecords(t, record)
	pid := 0
	for _, item := range records {
		if fakeCommandName(item.Argv) == "open" {
			pid = item.PID
		}
	}
	if pid == 0 {
		t.Fatal("missing helper pid")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d survived cancellation", pid)
}

func TestClientCloseIsIdempotentAndVerifiesSession(t *testing.T) {
	client, record := testClient(t, fakeProgramFor(map[string]fakeCommand{
		"close":        {Stdout: success(`{"closed":true}`)},
		"session list": {Stdout: success(`{"sessions":[]}`)},
	}), nil)
	if err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Read(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed client accepted a command: %v", err)
	}
	records := readRecords(t, record)
	closes := 0
	for _, item := range records {
		if fakeCommandName(item.Argv) == "close" {
			closes++
		}
	}
	if closes != 1 {
		t.Fatalf("close ran %d times", closes)
	}
}

func TestClientKillsOwnedDaemonWhenCloseIsIgnored(t *testing.T) {
	client, _ := testClient(t, fakeProgramFor(map[string]fakeCommand{
		"close":        {Stdout: `{"success":false,"data":null,"error":"busy"}`, Exit: 1},
		"session list": {Stdout: success(`{"sessions":[]}`)},
	}), nil)
	if err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	client.daemonPID = 999999
	client.mu.Unlock()
	killed := 0
	client.kill = func(pid int) error { killed = pid; return nil }
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if killed != 999999 {
		t.Fatalf("owned daemon not killed: %d", killed)
	}
}

func TestClientStartRequiresDaemonIdentity(t *testing.T) {
	client, _ := testClient(t, fakeProgramFor(map[string]fakeCommand{
		"session info": {Stdout: success(`{"active":false}`)},
	}), nil)
	if err := client.Start(context.Background()); !errors.Is(err, ErrProtocol) {
		t.Fatalf("missing daemon pid accepted: %v", err)
	}
}

func TestClientCloseFailsWhenSessionSurvives(t *testing.T) {
	client, _ := testClient(t, fakeProgramFor(map[string]fakeCommand{
		"close":        {Stdout: success(`{"closed":true}`)},
		"session list": {Stdout: success(`{"sessions":["tw-agent"]}`)},
	}), nil)
	if err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(context.Background()); !errors.Is(err, ErrCommand) {
		t.Fatalf("surviving session accepted: %v", err)
	}
}

func TestClientRejectsUnsafeOptions(t *testing.T) {
	binary, _ := fakeExecutable(t, fakeProgram{Version: "agent-browser " + Version})
	chrome := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(chrome, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	profile := t.TempDir()
	for name, mutate := range map[string]func(*Options){
		"relative_binary": func(o *Options) { o.BinaryPath = "agent-browser" },
		"missing_binary":  func(o *Options) { o.BinaryPath = filepath.Join(t.TempDir(), "missing") },
		"bad_session":     func(o *Options) { o.Session = "tw agent; rm -rf /" },
		"bad_namespace":   func(o *Options) { o.Namespace = "ns/../../x" },
		"relative_profile": func(o *Options) {
			o.ProfileDir = "relative"
		},
	} {
		t.Run(name, func(t *testing.T) {
			opts := Options{BinaryPath: binary, ChromePath: chrome, ProfileDir: profile, Session: "tw-agent", Namespace: "tw-ns", Timeout: time.Second}
			mutate(&opts)
			if _, err := New(opts); !errors.Is(err, ErrUnsafe) {
				t.Fatalf("unsafe options accepted: %v", err)
			}
		})
	}
}

func TestClientRejectsUnsafeTargetsAndRefs(t *testing.T) {
	client, _ := testClient(t, fakeProgramFor(map[string]fakeCommand{"open": {Stdout: success(`{"url":"https://fixture.test"}`)}}), nil)
	if err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Open(context.Background(), "https://fixture.test/\r\nInjected: 1"); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("newline target accepted: %v", err)
	}
	for _, ref := range []string{"", "e", "e-1", "e1 --json", "@e1", "e1234567890123"} {
		if err := client.Click(context.Background(), ref); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("unsafe ref %q accepted: %v", ref, err)
		}
	}
}

func TestFakeProgramNameParsing(t *testing.T) {
	argv := []string{"--json", "--session", "open", "--profile", "/p", "open", "https://fixture.test"}
	if got := fakeCommandName(argv); got != "open" {
		t.Fatalf("command=%q", got)
	}
}
