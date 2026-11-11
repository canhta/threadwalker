package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canhta/threadwalker/internal/store"
)

func TestDashboardCLIRejectsNonLoopbackAndNeverResolvesSecrets(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "existing.db")
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	for _, args := range [][]string{{"-listen", "0.0.0.0:8080"}, {"-listen", "example.invalid:8080"}, {"-listen", "127.0.0.1:0", "-unknown"}, {"unexpected"}, {"-listen", "[::]:8080"}} {
		var out, errs bytes.Buffer
		if code := run(context.Background(), append([]string{"dashboard"}, args...), func(string) string { t.Fatal("dashboard resolved environment"); return "" }, &out, &errs); code != 1 {
			t.Fatalf("args %v: code %d", args, code)
		}
		if strings.Contains(out.String()+errs.String(), "example.invalid") {
			t.Fatal("reflected listen address")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errs bytes.Buffer
	if code := run(ctx, []string{"dashboard", "-db", dbPath, "-agents", filepath.Join("..", "..", "agents"), "-listen", "127.0.0.1:0"}, func(string) string { t.Fatal("dashboard resolved environment"); return "" }, &out, &errs); code != 0 {
		t.Fatalf("cancelled CLI: %d %s", code, errs.String())
	}
}
