package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/store"
)

func TestDashboardCLIRejectsNonLoopbackAndNeverResolvesSecrets(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "existing.db")
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	for _, args := range [][]string{{"-listen", "0.0.0.0:8080"}, {"-listen", "example.invalid:8080"}, {"-listen", "127.0.0.1:0", "-unknown"}, {"unexpected"}, {"-listen", "[::]:8080"}, {"-listen", "127.0.0.1:http"}, {"-listen", "127.0.0.1:-1"}} {
		var out, errs bytes.Buffer
		if code := run(context.Background(), append([]string{"dashboard"}, args...), noSecretEnv(t, "dashboard resolved environment"), &out, &errs); code != exitUsage {
			t.Fatalf("args %v: code %d, want %d", args, code, exitUsage)
		}
		if strings.Contains(out.String()+errs.String(), "example.invalid") {
			t.Fatal("reflected listen address")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errs bytes.Buffer
	if code := run(ctx, []string{"dashboard", "-db", dbPath, "-agents", filepath.Join("..", "..", "templates"), "-listen", "127.0.0.1:0"}, noSecretEnv(t, "dashboard resolved environment"), &out, &errs); code != 0 {
		t.Fatalf("cancelled CLI: %d %s", code, errs.String())
	}
}

type dashboardCancelWriter struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (w *dashboardCancelWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	if bytes.Contains(p, []byte(`"event":"dashboard.server.started"`)) {
		w.cancel()
	}
	return n, err
}

func TestDashboardCLIStartsFromPublicConfigWithoutCredentials(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "existing.db")
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := dashboardCancelWriter{cancel: cancel}
	var errs bytes.Buffer
	code := run(ctx, []string{"dashboard", "-agents", enabledTelegramAgentDir(t), "-db", dbPath, "-listen", "127.0.0.1:0"}, noSecretEnv(t, "dashboard resolved secret environment"), &out, &errs)
	if code != 0 || !strings.Contains(out.String(), `"event":"dashboard.server.finished"`) {
		t.Fatalf("startup/stop %d stderr %s logs %s", code, errs.String(), out.String())
	}
}

func TestDashboardCLIFailuresDoNotExposeConfigurationOrPaths(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want int
	}{
		{[]string{"-llms", "SYSTEM-PROMPT-private-path"}, exitUsage},
		{[]string{"-agents", "SYSTEM-PROMPT-private-path"}, exitFailure},
		{[]string{"-agents", filepath.Join("..", "..", "templates"), "-db", "SYSTEM-PROMPT-private-path"}, exitFailure},
		{[]string{"-log-level", "SYSTEM-PROMPT-private-path"}, exitUsage},
		{[]string{"-log-format", "SYSTEM-PROMPT-private-path"}, exitUsage},
	} {
		var out, errs bytes.Buffer
		if code := run(context.Background(), append([]string{"dashboard"}, tc.args...), noSecretEnv(t, "resolved environment"), &out, &errs); code != tc.want {
			t.Fatalf("args %v: code %d, want %d", tc.args, code, tc.want)
		}
		if strings.Contains(out.String()+errs.String(), "SYSTEM-PROMPT-private-path") {
			t.Fatal("reflected private path/config")
		}
	}
}

func TestDashboardCLIContentModeNeedsAToken(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "existing.db")
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	var out, errs bytes.Buffer
	code := run(context.Background(), []string{"dashboard", "-agents", filepath.Join("..", "..", "templates"), "-db", dbPath, "-content"}, func(string) string { return "" }, &out, &errs)
	if code != exitFailure || !strings.Contains(errs.String(), "THREADZ_DASHBOARD_TOKEN") {
		t.Fatalf("content without token: code %d stderr %q", code, errs.String())
	}
	if strings.Contains(out.String()+errs.String(), "synthetic-token") {
		t.Fatal("token value reflected")
	}
}
