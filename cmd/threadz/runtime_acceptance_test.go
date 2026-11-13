package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
)

// These tests are the CLI acceptance contract for the local-first runtime. They
// drive the real run() entry point with injected getenv and captured writers,
// an isolated temporary SQLite database and no network, VPS or real credentials.
const (
	runtimeCLINoteText      = "Synthetic note for the next wake."
	runtimeCLIActionText    = "Synthetic local dry-run action."
	runtimeCLITrendText     = "Synthetic offline trend"
	runtimeCLIDummyKey      = "offline-dummy-key"
	runtimeCLIUnexpectedEnv = "runtime-cli-unexpected-environment-value"
	runtimeCLICatalogModel  = "arbitrary-runtime-cli-model"
)

// runtimeCLIEnvRecorder proves offline operation never resolves credentials
// from the operator's real environment.
type runtimeCLIEnvRecorder struct {
	keys []string
}

func (e *runtimeCLIEnvRecorder) getenv(key string) string {
	if strings.HasPrefix(key, "THREADZ_") {
		return ""
	}
	e.keys = append(e.keys, key)
	return runtimeCLIUnexpectedEnv
}

func runtimeCLIRun(t *testing.T, ctx context.Context, args []string, env *runtimeCLIEnvRecorder) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(ctx, args, env.getenv, &out, &errOut)
	return code, out.String(), errOut.String()
}

func runtimeCLIOpen(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("opening acceptance database: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing acceptance database: %v", err)
		}
	})
	return st
}

func runtimeCLICount(t *testing.T, st *store.Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("counting with %q: %v", query, err)
	}
	return n
}

func runtimeCLIDecodeLogs(t *testing.T, stdout string) []map[string]any {
	t.Helper()
	var records []map[string]any
	dec := json.NewDecoder(strings.NewReader(stdout))
	for {
		var record map[string]any
		err := dec.Decode(&record)
		if errors.Is(err, io.EOF) {
			return records
		}
		if err != nil {
			t.Fatalf("stdout is not newline-delimited JSON logs: %v; stdout %q", err, stdout)
		}
		records = append(records, record)
	}
}

func runtimeCLIEvents(records []map[string]any, event string) []map[string]any {
	var found []map[string]any
	for _, record := range records {
		if record["event"] == event {
			found = append(found, record)
		}
	}
	return found
}

func runtimeCLIWriteText(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating directory %s: %v", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return path
}

// runtimeCLITemplate builds a temporary template from the canonical example
// instead of copying the whole file. Only the id, the optional llm block and
// an optional mutation differ from the documented shape.
func runtimeCLITemplate(t *testing.T, dir, filename, id, llmBlock string, mutate func(string) string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fixture.agent.yaml"))
	if err != nil {
		t.Fatalf("reading canonical example template: %v", err)
	}
	body := string(raw)

	if llmBlock != "" {
		const llmPrefix = "\nllm:\n"
		start := strings.Index(body, llmPrefix)
		if start < 0 {
			t.Fatalf("canonical example has no llm block")
		}
		rest := body[start+len(llmPrefix):]
		relEnd := strings.Index(rest, "\nlimits:")
		if relEnd < 0 {
			t.Fatalf("canonical example has no limits block after llm")
		}
		end := start + len(llmPrefix) + relEnd
		body = body[:start] + "\n" + strings.TrimRight(llmBlock, "\n") + "\n" + body[end:]
	}

	const oldID = "id: fixture"
	if !strings.Contains(body, oldID) {
		t.Fatalf("canonical example does not declare %q", oldID)
	}
	body = strings.Replace(body, oldID, "id: "+id, 1)

	if mutate != nil {
		body = mutate(body)
	}
	return runtimeCLIWriteText(t, dir, filename, body)
}

func runtimeCLIAssertNoRuntimeRowsIfDBExists(t *testing.T, dbPath string) {
	t.Helper()
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		t.Fatalf("stat database %s: %v", dbPath, err)
	}
	st := runtimeCLIOpen(t, dbPath)
	for _, table := range []string{"wakes", "actions", "tool_calls", "notes"} {
		if got := runtimeCLICount(t, st, "SELECT COUNT(*) FROM "+table); got != 0 {
			t.Errorf("%s rows = %d, want 0 before startup is accepted", table, got)
		}
	}
}

// TestRuntimeCLIOnceOfflineWakePersistsCompletedAudit proves one bounded offline
// wake runs to completion and leaves the audit, action, note and schedule rows.
func TestRuntimeCLIOnceOfflineWakePersistsCompletedAudit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dbPath := filepath.Join(t.TempDir(), "offline.db")
	env := &runtimeCLIEnvRecorder{}
	code, stdout, stderr := runtimeCLIRun(t, ctx, []string{"run", "-once", "-offline", "-db", dbPath}, env)
	if code != 0 {
		t.Fatalf("run(-once -offline) = %d, want 0; stderr=%s stdout=%s", code, stderr, stdout)
	}
	if len(env.keys) != 0 {
		t.Errorf("offline run resolved real environment variables %v; it must use dummy credentials", env.keys)
	}

	st := runtimeCLIOpen(t, dbPath)
	if got := runtimeCLICount(t, st, `SELECT COUNT(*) FROM wakes`); got != 1 {
		t.Fatalf("wakes = %d, want exactly one bounded wake", got)
	}
	if got := runtimeCLICount(t, st, `SELECT COUNT(*) FROM wakes WHERE end_reason='completed' AND ended_at IS NOT NULL`); got != 1 {
		t.Fatalf("completed wakes = %d, want 1", got)
	}

	var startedRaw string
	var steps int
	if err := st.DB().QueryRow(`SELECT started_at, steps FROM wakes`).Scan(&startedRaw, &steps); err != nil {
		t.Fatalf("reading wake row: %v", err)
	}
	startedAt, err := time.Parse(time.RFC3339Nano, startedRaw)
	if err != nil {
		t.Fatalf("wake started_at %q is not RFC3339: %v", startedRaw, err)
	}
	if steps < 5 {
		t.Errorf("wake steps = %d, want the five scripted tool calls to be counted", steps)
	}

	rows, err := st.DB().Query(`SELECT tool, args_json, result_json FROM tool_calls ORDER BY id`)
	if err != nil {
		t.Fatalf("reading tool call audit rows: %v", err)
	}
	defer rows.Close()
	seenTools := map[string]bool{}
	auditRows := 0
	for rows.Next() {
		var tool, args, result string
		if err := rows.Scan(&tool, &args, &result); err != nil {
			t.Fatalf("scanning tool call row: %v", err)
		}
		auditRows++
		seenTools[tool] = true
		if !json.Valid([]byte(args)) {
			t.Errorf("tool call %q args_json is not valid JSON: %q", tool, args)
		}
		if result == "" || !json.Valid([]byte(result)) {
			t.Errorf("tool call %q result_json is empty or invalid JSON: %q", tool, result)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading tool call audit rows: %v", err)
	}
	if auditRows < 4 {
		t.Errorf("tool call audit rows = %d, want at least 4", auditRows)
	}
	for _, want := range []string{"read_feed", "fetch_trends", "memory_add", "post"} {
		if !seenTools[want] {
			t.Errorf("tool call audit rows do not include %q; got %v", want, seenTools)
		}
	}

	if got := runtimeCLICount(t, st, `SELECT COUNT(*) FROM actions WHERE dry_run=1`); got < 1 {
		t.Errorf("dry_run actions = %d, want at least 1", got)
	}
	if got := runtimeCLICount(t, st, `SELECT COUNT(*) FROM actions WHERE dry_run=0`); got != 0 {
		t.Errorf("live actions = %d, want 0 in an offline dry run", got)
	}
	if got := runtimeCLICount(t, st, `SELECT COUNT(*) FROM actions WHERE kind='post'`); got < 1 {
		t.Errorf("post actions = %d, want at least 1", got)
	}
	var actionText string
	if err := st.DB().QueryRow(`SELECT text FROM actions LIMIT 1`).Scan(&actionText); err != nil {
		t.Fatalf("reading dry-run action text: %v", err)
	}
	if strings.TrimSpace(actionText) == "" {
		t.Error("dry-run action text is empty")
	}

	var agentID string
	if err := st.DB().QueryRow(`SELECT id FROM agents LIMIT 1`).Scan(&agentID); err != nil {
		t.Fatalf("reading agent row: %v", err)
	}
	var nextWakeRaw string
	if err := st.DB().QueryRow(`SELECT next_wake_at FROM agents WHERE id=?`, agentID).Scan(&nextWakeRaw); err != nil {
		t.Fatalf("reading agent next_wake_at: %v", err)
	}
	if index, err := st.MemoryIndex(context.Background(), agentID); err != nil || len(index) != 1 || index[0].Text != runtimeCLINoteText {
		t.Errorf("memory index = %v, %v; want the entry written during the wake %q", index, err, runtimeCLINoteText)
	}
	nextWake, err := time.Parse(time.RFC3339Nano, nextWakeRaw)
	if err != nil {
		t.Fatalf("next_wake_at %q is not parseable RFC3339: %v", nextWakeRaw, err)
	}
	if !nextWake.After(startedAt) {
		t.Errorf("next_wake_at %s is not after the wake start %s", nextWake, startedAt)
	}
	if nextWake.After(startedAt.Add(24*time.Hour + time.Second)) {
		t.Errorf("next_wake_at %s exceeds the 24-hour scheduling ceiling from %s", nextWake, startedAt)
	}

	if got := runtimeCLICount(t, st, `SELECT COUNT(*) FROM wakes WHERE tokens_in > 0 AND tokens_out > 0`); got != 1 {
		t.Errorf("wakes with recorded token usage = %d, want 1", got)
	}

	records := runtimeCLIDecodeLogs(t, stdout)
	if len(records) == 0 {
		t.Fatal("offline run produced no operational log records")
	}
	if got := len(runtimeCLIEvents(records, "wake.started")); got != 1 {
		t.Errorf("wake.started events = %d, want 1", got)
	}
	if got := len(runtimeCLIEvents(records, "wake.finished")); got != 1 {
		t.Errorf("wake.finished events = %d, want 1", got)
	}
	if got := len(runtimeCLIEvents(records, "wake.stopped")); got != 0 {
		t.Errorf("wake.stopped events = %d, want 0 for a completed wake", got)
	}
}

// TestRuntimeCLIOfflineInlineLLMUsesArbitraryNamesWithoutEnv proves the
// offline path honors an inline llm block with arbitrary operator-chosen
// identifiers and never asks the real getenv for its key.
func TestRuntimeCLIOfflineInlineLLMUsesArbitraryNamesWithoutEnv(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	agentsDir := t.TempDir()
	runtimeCLITemplate(t, agentsDir, "agent.yaml", "runtime-cli-catalog-agent", "llm:\n  base_url: https://runtime-cli-provider.invalid/v1\n  model: "+runtimeCLICatalogModel+"\n  api_key_env: RUNTIME_CLI_ARBITRARY_KEY_NAME\n  price_per_mtok_in_usd: 0.4\n  price_per_mtok_out_usd: 0.8\n", nil)

	dbPath := filepath.Join(t.TempDir(), "catalog.db")
	env := &runtimeCLIEnvRecorder{}
	code, stdout, stderr := runtimeCLIRun(t, ctx, []string{
		"run", "-once", "-offline", "-agents", agentsDir, "-db", dbPath,
	}, env)
	if code != 0 {
		t.Fatalf("run(-once -offline -agents) = %d, want 0; stderr=%s stdout=%s", code, stderr, stdout)
	}
	if len(env.keys) != 0 {
		t.Errorf("offline catalog run resolved real environment variables %v; credentials must be dummy offline", env.keys)
	}
	if strings.Contains(stdout+stderr, runtimeCLIUnexpectedEnv) {
		t.Errorf("offline catalog run used the unexpectedly resolved environment value: %s%s", stdout, stderr)
	}

	st := runtimeCLIOpen(t, dbPath)
	if got := runtimeCLICount(t, st, `SELECT COUNT(*) FROM wakes WHERE end_reason='completed'`); got != 1 {
		t.Errorf("completed wakes = %d, want 1", got)
	}
	if got := runtimeCLICount(t, st, `SELECT COUNT(*) FROM actions WHERE dry_run=1`); got < 1 {
		t.Errorf("dry_run actions = %d, want at least 1", got)
	}

	records := runtimeCLIDecodeLogs(t, stdout)
	modelSeen := false
	for _, record := range records {
		if record["model"] == runtimeCLICatalogModel {
			modelSeen = true
			break
		}
	}
	if !modelSeen {
		t.Errorf("operational logs never report the selected arbitrary model %q", runtimeCLICatalogModel)
	}
}

// TestRuntimeCLIOfflineLogsOmitTemplateAndNoteValues proves payloads from the
// template and the agent's own note stay out of captured operational logs.
func TestRuntimeCLIOfflineLogsOmitTemplateAndNoteValues(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const templateCanary = "runtime-cli-template-secret-6f4d2a"
	agentsDir := t.TempDir()
	runtimeCLITemplate(t, agentsDir, "agent.yaml", "runtime-cli-redaction-agent", "", func(body string) string {
		const old = "  name: Kaito"
		if !strings.Contains(body, old) {
			t.Fatalf("canonical example has no %q persona name line", strings.TrimSpace(old))
		}
		return strings.Replace(body, old, "  name: "+templateCanary, 1)
	})

	dbPath := filepath.Join(t.TempDir(), "redaction.db")
	env := &runtimeCLIEnvRecorder{}
	code, stdout, stderr := runtimeCLIRun(t, ctx, []string{"run", "-once", "-offline", "-agents", agentsDir, "-db", dbPath}, env)
	if code != 0 {
		t.Fatalf("run(-once -offline -agents) = %d, want 0; stderr=%s stdout=%s", code, stderr, stdout)
	}
	if len(env.keys) != 0 {
		t.Errorf("offline explicit-template run resolved real environment variables %v", env.keys)
	}

	st := runtimeCLIOpen(t, dbPath)
	if got := runtimeCLICount(t, st, `SELECT COUNT(*) FROM memory_index WHERE text=? AND archived_at IS NULL`, runtimeCLINoteText); got != 1 {
		t.Fatalf("live memory entries matching the scripted note = %d, want 1", got)
	}

	records := runtimeCLIDecodeLogs(t, stdout)
	if len(runtimeCLIEvents(records, "wake.started")) != 1 {
		t.Fatalf("expected one wake.started log record, got %d", len(runtimeCLIEvents(records, "wake.started")))
	}

	combined := stdout + "\n" + stderr
	for _, payload := range []string{
		templateCanary,
		runtimeCLINoteText,
		runtimeCLIActionText,
		runtimeCLITrendText,
		runtimeCLIDummyKey,
	} {
		if strings.Contains(combined, payload) {
			t.Errorf("operational output leaked payload %q: %s", payload, combined)
		}
	}
}

// runtimeCLICancelWriter cancels the run context as soon as the first wake
// lifecycle event is written, so the wake is interrupted deterministically
// without a wall-clock delay.
type runtimeCLICancelWriter struct {
	buf    bytes.Buffer
	needle string
	cancel context.CancelFunc
	fired  bool
}

func (w *runtimeCLICancelWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	if !w.fired && strings.Contains(string(p), w.needle) {
		w.fired = true
		w.cancel()
	}
	return len(p), nil
}

// TestRuntimeCLICancelledContextStopsWithoutSideEffects proves cancellation
// ends the bounded run without publishing actions, tool calls or notes.
func TestRuntimeCLICancelledContextStopsWithoutSideEffects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dbPath := filepath.Join(t.TempDir(), "cancelled.db")
	env := &runtimeCLIEnvRecorder{}
	writer := &runtimeCLICancelWriter{needle: `"event":"wake.started"`, cancel: cancel}
	var stderr bytes.Buffer
	code := run(ctx, []string{"run", "-once", "-offline", "-db", dbPath}, env.getenv, writer, &stderr)
	if !writer.fired {
		t.Fatalf("run returned %d without starting a wake; stderr=%s stdout=%s", code, stderr.String(), writer.buf.String())
	}
	if code != 0 {
		t.Fatalf("cancelled run = %d, want a bounded graceful exit 0; stderr=%s", code, stderr.String())
	}

	st := runtimeCLIOpen(t, dbPath)
	if got := runtimeCLICount(t, st, `SELECT COUNT(*) FROM wakes WHERE end_reason='completed'`); got != 0 {
		t.Errorf("completed wakes after cancellation = %d, want 0", got)
	}
	if got := runtimeCLICount(t, st, `SELECT COUNT(*) FROM wakes WHERE ended_at IS NULL`); got != 0 {
		t.Errorf("open wakes after cancellation = %d, want 0", got)
	}
	for _, table := range []string{"actions", "tool_calls", "notes"} {
		if got := runtimeCLICount(t, st, "SELECT COUNT(*) FROM "+table); got != 0 {
			t.Errorf("%s rows after cancellation = %d, want 0", table, got)
		}
	}
}

// TestRuntimeCLIUnknownFlagAndCommandExitNonzero covers the command surface
// outside the bounded offline contract.
func TestRuntimeCLIUnknownFlagAndCommandExitNonzero(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"unknown flag", []string{"run", "-once", "-offline", "-db", filepath.Join(t.TempDir(), "flag.db"), "-not-a-real-flag"}},
		{"unknown command", []string{"run-acceptance-unknown"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runtimeCLIRun(t, context.Background(), tc.args, &runtimeCLIEnvRecorder{})
			if code == 0 {
				t.Fatalf("%s = 0, want nonzero; stdout=%s stderr=%s", tc.name, stdout, stderr)
			}
			if strings.TrimSpace(stderr) == "" {
				t.Errorf("%s produced no diagnostic on stderr", tc.name)
			}
		})
	}
}

// TestRuntimeCLIMissingTemplateExitsNonzero proves explicit offline
// configuration fails closed instead of silently falling back to the built-in
// synthetic template.
func TestRuntimeCLIMissingTemplateExitsNonzero(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing template directory", []string{"run", "-once", "-offline", "-agents", filepath.Join(t.TempDir(), "missing-agents"), "-db", filepath.Join(t.TempDir(), "template.db")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runtimeCLIRun(t, context.Background(), tc.args, &runtimeCLIEnvRecorder{})
			if code == 0 {
				t.Fatalf("%s = 0, want nonzero; stdout=%s stderr=%s", tc.name, stdout, stderr)
			}
			if strings.TrimSpace(stderr) == "" {
				t.Errorf("%s produced no diagnostic on stderr", tc.name)
			}
		})
	}
}

// TestRuntimeCLIInvalidDatabasePathExitsNonzero proves an unopenable database
// path is rejected before any wake.
func TestRuntimeCLIInvalidDatabasePathExitsNonzero(t *testing.T) {
	code, stdout, stderr := runtimeCLIRun(t, context.Background(), []string{
		"run", "-once", "-offline", "-db", t.TempDir(),
	}, &runtimeCLIEnvRecorder{})
	if code == 0 {
		t.Fatalf("run with a directory as the database path = 0, want nonzero; stdout=%s stderr=%s", stdout, stderr)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("invalid database path produced no diagnostic on stderr")
	}
}

// TestRuntimeCLINonDryRunTemplateRefusesStartup proves the stub backend fails
// closed until a real publishing backend is explicitly configured.
func TestRuntimeCLINonDryRunTemplateRefusesStartup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	agentsDir := t.TempDir()
	runtimeCLITemplate(t, agentsDir, "agent.yaml", "runtime-cli-live-agent", "", func(body string) string {
		const old = "\ndry_run: true"
		if !strings.Contains(body, old) {
			t.Fatalf("canonical example has no dry_run true line")
		}
		return strings.Replace(body, old, "\ndry_run: false", 1)
	})
	dbPath := filepath.Join(t.TempDir(), "live.db")

	code, stdout, stderr := runtimeCLIRun(t, ctx, []string{"run", "-once", "-offline", "-agents", agentsDir, "-db", dbPath}, &runtimeCLIEnvRecorder{})
	if code == 0 {
		t.Fatalf("run with dry_run false = 0, want nonzero; stdout=%s stderr=%s", stdout, stderr)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("non-dry-run refusal produced no diagnostic on stderr")
	}
	runtimeCLIAssertNoRuntimeRowsIfDBExists(t, dbPath)
}
