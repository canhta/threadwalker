package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	cmdLoggingFakeKey             = "cmd-logging-fake-llm-key-111"
	cmdLoggingFakeTelegramToken   = "cmd-logging-fake-telegram-token-222"
	cmdLoggingFakeOwnerChatID     = "cmd-logging-fake-owner-chat-333"
	cmdLoggingFakeStrangerContent = "cmd-logging-stranger-content-444"
)

type cmdLogRecord map[string]any

var cmdLogSafeCode = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)

var cmdLogAllowedFields = map[string]bool{
	"time": true, "level": true, "msg": true, "event": true,
	"agent_id": true, "wake_id": true, "provider_id": true, "profile_id": true,
	"model": true, "protocol": true, "operation": true, "request": true,
	"attempt": true, "http_status": true, "duration_ms": true,
	"tokens_in": true, "tokens_out": true, "cost_usd": true,
	"outcome": true, "reason": true, "steps": true, "tool": true, "tool_call_id": true,
}

func cmdLoggingEnv() map[string]string {
	return map[string]string{
		"LLM_API_KEY":            cmdLoggingFakeKey,
		"OPENAI_API_KEY":         cmdLoggingFakeKey,
		"TELEGRAM_BOT_TOKEN":     cmdLoggingFakeTelegramToken,
		"TELEGRAM_OWNER_CHAT_ID": cmdLoggingFakeOwnerChatID,
	}
}

func cmdLoggingRun(t *testing.T, args []string, env map[string]string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(context.Background(), args, func(key string) string { return env[key] }, &out, &errOut)
	return code, out.String(), errOut.String()
}

func cmdLoggingDecode(t *testing.T, stdout string) []cmdLogRecord {
	t.Helper()
	var records []cmdLogRecord
	dec := json.NewDecoder(strings.NewReader(stdout))
	for {
		var record cmdLogRecord
		err := dec.Decode(&record)
		if errors.Is(err, io.EOF) {
			return records
		}
		if err != nil {
			t.Fatalf("stdout is not newline-delimited JSON: %v; stdout %q", err, stdout)
		}
		records = append(records, record)
	}
}

func cmdLoggingEvents(records []cmdLogRecord, event string) []cmdLogRecord {
	var found []cmdLogRecord
	for _, record := range records {
		if record["event"] == event {
			found = append(found, record)
		}
	}
	return found
}

func cmdLoggingOne(t *testing.T, records []cmdLogRecord, event string) cmdLogRecord {
	t.Helper()
	found := cmdLoggingEvents(records, event)
	if len(found) != 1 {
		t.Fatalf("event %q appeared %d times, want once; records: %v", event, len(found), records)
	}
	return found[0]
}

func cmdLoggingAssertSafe(t *testing.T, stdout, stderr string, records []cmdLogRecord) {
	t.Helper()
	for _, record := range records {
		for field := range record {
			if !cmdLogAllowedFields[field] {
				t.Errorf("event %v logged unexpected field %q", record["event"], field)
			}
		}
		for _, field := range []string{"reason", "outcome"} {
			value, ok := record[field]
			if !ok {
				continue
			}
			text, ok := value.(string)
			if !ok {
				t.Errorf("event %v field %q = %v is not a string", record["event"], field, value)
				continue
			}
			if text != "" && !cmdLogSafeCode.MatchString(text) {
				t.Errorf("event %v field %q = %v is not a safe stable code", record["event"], field, value)
			}
		}
	}
	combined := stdout + "\n" + stderr
	for _, secret := range []string{cmdLoggingFakeKey, cmdLoggingFakeTelegramToken, cmdLoggingFakeOwnerChatID, cmdLoggingFakeStrangerContent} {
		if strings.Contains(combined, secret) {
			t.Errorf("CLI output leaked %q: %s", secret, combined)
		}
	}
}

func TestCLILoggingDefaultsToJSONInfo(t *testing.T) {
	agentsDir := "testdata"
	dbPath := filepath.Join(t.TempDir(), "threadz.db")
	code, stdout, stderr := cmdLoggingRun(t, []string{"check", "-agents", agentsDir, "-db", dbPath}, cmdLoggingEnv())
	if code != 0 {
		t.Fatalf("run(check) = %d; stderr=%s stdout=%s", code, stderr, stdout)
	}

	records := cmdLoggingDecode(t, stdout)
	if len(records) == 0 {
		t.Fatalf("default JSON logging produced no records: %q", stdout)
	}
	started := cmdLoggingOne(t, records, "check.started")
	if got := started["level"]; got != "INFO" {
		t.Errorf("check.started level = %v, want INFO", got)
	}
	finished := cmdLoggingOne(t, records, "check.finished")
	if got := finished["outcome"]; got != "success" {
		t.Errorf("check.finished outcome = %v, want success", got)
	}
	if got := finished["reason"]; got != "validated" {
		t.Errorf("check.finished reason = %v, want validated", got)
	}
	configured := cmdLoggingEvents(records, "check.configured")
	if len(configured) != 1 {
		t.Fatalf("configuration events=%d, want the one fixture template", len(configured))
	}
	seen := map[string]bool{}
	for _, event := range configured {
		id, _ := event["agent_id"].(string)
		if seen[id] || id != "fixture" {
			t.Fatalf("invalid agent correlation %q", id)
		}
		seen[id] = true
	}
	cmdLoggingAssertSafe(t, stdout, stderr, records)
}

func TestCLILoggingExplicitFlagsOnSuccessAndFailure(t *testing.T) {
	agentsDir := "testdata"

	t.Run("success debug JSON", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "threadz.db")
		code, stdout, stderr := cmdLoggingRun(t, []string{
			"check", "-agents", agentsDir, "-db", dbPath,
			"-log-level", "debug", "-log-format", "json",
		}, cmdLoggingEnv())
		if code != 0 {
			t.Fatalf("run(check -log-level debug -log-format json) = %d; stderr=%s stdout=%s", code, stderr, stdout)
		}
		records := cmdLoggingDecode(t, stdout)
		started := cmdLoggingOne(t, records, "check.started")
		if got := started["level"]; got != "INFO" {
			t.Errorf("check.started level = %v, want INFO", got)
		}
		finished := cmdLoggingOne(t, records, "check.finished")
		if got := finished["outcome"]; got != "success" {
			t.Errorf("check.finished outcome = %v, want success", got)
		}
		cmdLoggingAssertSafe(t, stdout, stderr, records)
	})

	t.Run("failure debug JSON", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "threadz.db")
		// Explicitly enabled Telegram requires credentials and must fail.
		agentsDir := enabledTelegramAgentDir(t)
		env := map[string]string{"LLM_API_KEY": cmdLoggingFakeKey, "OPENAI_API_KEY": cmdLoggingFakeKey}
		code, stdout, stderr := cmdLoggingRun(t, []string{
			"check", "-agents", agentsDir, "-db", dbPath,
			"-log-level", "debug", "-log-format", "json",
		}, env)
		if code != 1 {
			t.Fatalf("run(check) with missing Telegram values = %d, want 1; stderr=%s stdout=%s", code, stderr, stdout)
		}
		if !strings.Contains(stderr, "telegram.bot_token_env") || !strings.Contains(stderr, "telegram.owner_chat_id_env") {
			t.Errorf("stderr does not name missing variables: %q", stderr)
		}
		records := cmdLoggingDecode(t, stdout)
		cmdLoggingOne(t, records, "check.started")
		finished := cmdLoggingOne(t, records, "check.finished")
		if got := finished["outcome"]; got != "error" {
			t.Errorf("check.finished outcome = %v, want error", got)
		}
		if got := finished["reason"]; got != "configuration_invalid" {
			t.Errorf("check.finished reason = %v, want configuration_invalid", got)
		}
		if got := len(cmdLoggingEvents(records, "check.configured")); got != 0 {
			t.Errorf("check.configured on failure = %d, want 0", got)
		}
		cmdLoggingAssertSafe(t, stdout, stderr, records)
	})

	t.Run("success text format", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "threadz.db")
		code, stdout, stderr := cmdLoggingRun(t, []string{
			"check", "-agents", agentsDir, "-db", dbPath,
			"-log-level", "info", "-log-format", "text",
		}, cmdLoggingEnv())
		if code != 0 {
			t.Fatalf("run(check -log-format text) = %d; stderr=%s stdout=%s", code, stderr, stdout)
		}
		if !strings.Contains(stdout, "event=check.started") || !strings.Contains(stdout, "event=check.finished") {
			t.Errorf("text log output missing lifecycle events: %q", stdout)
		}
		if json.Valid([]byte(strings.TrimSpace(stdout))) {
			t.Errorf("text format produced a JSON document: %q", stdout)
		}
		if strings.Contains(stdout, cmdLoggingFakeKey) || strings.Contains(stderr, cmdLoggingFakeKey) {
			t.Errorf("text output leaked fake key")
		}
	})
}

func TestCLILoggingRejectsInvalidOptions(t *testing.T) {
	agentsDir := "testdata"
	for _, tc := range []struct {
		name, level, format, want string
	}{
		{name: "bad level", level: "trace", format: "json", want: "log level"},
		{name: "bad format", level: "info", format: "logfmt", want: "log format"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "threadz.db")
			code, stdout, stderr := cmdLoggingRun(t, []string{
				"check", "-agents", agentsDir, "-db", dbPath,
				"-log-level", tc.level, "-log-format", tc.format,
			}, cmdLoggingEnv())
			if code != exitUsage {
				t.Fatalf("invalid logging options = %d, want %d; stderr=%s stdout=%s", code, exitUsage, stderr, stdout)
			}
			if !strings.Contains(strings.ToLower(stderr), tc.want) {
				t.Errorf("stderr %q does not name the invalid %s option", stderr, tc.want)
			}
			records := cmdLoggingDecode(t, stdout)
			if got := len(cmdLoggingEvents(records, "check.started")); got != 0 {
				t.Errorf("check.started emitted before logging validation = %d, want 0", got)
			}
			finished := cmdLoggingOne(t, records, "check.finished")
			if got := finished["outcome"]; got != "error" {
				t.Errorf("check.finished outcome = %v, want error", got)
			}
			if got := finished["reason"]; got != "invalid_logging_options" {
				t.Errorf("check.finished reason = %v, want invalid_logging_options", got)
			}
			cmdLoggingAssertSafe(t, stdout, stderr, records)
		})
	}
}
