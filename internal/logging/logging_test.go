package logging_test

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/logging"
)

// decodeRecords decodes newline-delimited JSON log records and fails on any
// line that is not a JSON object.
func decodeRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	for {
		var record map[string]any
		err := dec.Decode(&record)
		if err == io.EOF {
			return records
		}
		if err != nil {
			t.Fatalf("decoding log record: %v; output %q", err, buf.String())
		}
		records = append(records, record)
	}
}

func TestNewDefaultsToJSONInfo(t *testing.T) {
	var buf bytes.Buffer
	logger, err := logging.New(&buf, "", "")
	if err != nil {
		t.Fatalf("New with defaults: %v", err)
	}

	logger.Debug("debug noise", "event", "debug.noise")
	logger.Info("check passed", "event", "check.finished", "outcome", "success")

	records := decodeRecords(t, &buf)
	if len(records) != 1 {
		t.Fatalf("default level must be info, got %d records: %s", len(records), buf.String())
	}
	record := records[0]
	if got := record["level"]; got != "INFO" {
		t.Errorf("level = %v, want INFO", got)
	}
	if got := record["event"]; got != "check.finished" {
		t.Errorf("event = %v, want check.finished", got)
	}
	if got := record["msg"]; got != "check passed" {
		t.Errorf("msg = %v, want check passed", got)
	}
}

func TestNewLevelFiltering(t *testing.T) {
	for _, tc := range []struct {
		level string
		want  []string
	}{
		{level: "debug", want: []string{"debug record", "info record", "warn record", "error record"}},
		{level: "info", want: []string{"info record", "warn record", "error record"}},
		{level: "warn", want: []string{"warn record", "error record"}},
		{level: "error", want: []string{"error record"}},
	} {
		t.Run(tc.level, func(t *testing.T) {
			var buf bytes.Buffer
			logger, err := logging.New(&buf, tc.level, "json")
			if err != nil {
				t.Fatalf("New(%q): %v", tc.level, err)
			}
			logger.Debug("debug record", "event", "test.debug")
			logger.Info("info record", "event", "test.info")
			logger.Warn("warn record", "event", "test.warn")
			logger.Error("error record", "event", "test.error")

			records := decodeRecords(t, &buf)
			if len(records) != len(tc.want) {
				t.Fatalf("%s: got %d records, want %d: %s", tc.level, len(records), len(tc.want), buf.String())
			}
			for i, want := range tc.want {
				if got := records[i]["msg"]; got != want {
					t.Errorf("%s record %d msg = %v, want %q", tc.level, i, got, want)
				}
			}
		})
	}
}

func TestNewTextFormat(t *testing.T) {
	var buf bytes.Buffer
	logger, err := logging.New(&buf, "info", "text")
	if err != nil {
		t.Fatalf("New(text): %v", err)
	}
	logger.Debug("debug noise", "event", "test.debug")
	logger.Info("started", "event", "check.started", "agent_id", "agent-1")

	out := buf.String()
	if strings.Contains(out, "debug noise") {
		t.Errorf("text info level leaked debug record: %q", out)
	}
	if !strings.Contains(out, "started") || !strings.Contains(out, "event=check.started") || !strings.Contains(out, "agent_id=agent-1") {
		t.Errorf("text output missing message, event or metadata: %q", out)
	}
	if json.Valid([]byte(strings.TrimSpace(out))) {
		t.Errorf("text format unexpectedly produced JSON: %q", out)
	}
}

func TestNewRejectsInvalidOptions(t *testing.T) {
	for _, tc := range []struct {
		name, level, format, want string
	}{
		{name: "unknown level", level: "trace", format: "json", want: "level"},
		{name: "unknown format", level: "info", format: "logfmt", want: "format"},
		{name: "unknown level and format", level: "verbose", format: "yaml", want: "level"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger, err := logging.New(&buf, tc.level, tc.format)
			if err == nil {
				t.Fatalf("New(%q, %q) accepted invalid options", tc.level, tc.format)
			}
			if logger != nil {
				t.Errorf("New(%q, %q) returned a logger with its error", tc.level, tc.format)
			}
			if buf.Len() != 0 {
				t.Errorf("New(%q, %q) wrote output: %q", tc.level, tc.format, buf.String())
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Errorf("error %q does not name the invalid %s option", err, tc.want)
			}
			if strings.ContainsAny(err.Error(), "\r\n") {
				t.Errorf("error must be a single secret-free line: %q", err)
			}
		})
	}
}
