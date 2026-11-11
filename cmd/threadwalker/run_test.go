package main

import (
	"bytes"
	"context"
	"github.com/canhta/threadwalker/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunOnceOfflineUsesNoCredentialsOrExternalServices(t *testing.T) {
	ctx := context.Background()
	var out, stderr bytes.Buffer
	db := filepath.Join(t.TempDir(), "offline.db")
	code := run(ctx, []string{"run", "-once", "-offline", "-db", db}, func(string) string { t.Fatal("offline read environment"); return "" }, &out, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s logs=%s", code, &stderr, &out)
	}
	st, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var wakes, actions int
	st.DB().QueryRow(`SELECT COUNT(*) FROM wakes WHERE end_reason='completed'`).Scan(&wakes)
	st.DB().QueryRow(`SELECT COUNT(*) FROM actions WHERE dry_run=1`).Scan(&actions)
	if wakes != 1 || actions != 1 {
		t.Fatalf("wakes=%d actions=%d", wakes, actions)
	}
}

func TestOfflineExplicitEnabledTemplateStillDisablesTelegram(t *testing.T) {
	raw, err := os.ReadFile("../../agents/solo-builder.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	text := strings.Replace(string(raw), "enabled: false", "enabled: true", 1)
	if err = os.WriteFile(filepath.Join(dir, "one.yaml"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"run", "-once", "-offline", "-agents", dir, "-db", filepath.Join(t.TempDir(), "offline.db")}, func(string) string { t.Fatal("offline read environment credentials"); return "" }, &out, &stderr)
	if code != 0 {
		t.Fatalf("offline enabled template failed %d: %s", code, &stderr)
	}
	if strings.Contains(out.String(), "telegram.operation.finished") {
		t.Fatal("offline started Telegram service")
	}
}
