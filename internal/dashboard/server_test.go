package dashboard_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/dashboard"
	"github.com/canhta/threadwalker/internal/store"
)

func TestServerServesLocalHTTPAndShutsDownOnContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h, err := dashboard.New(dashboard.Options{DB: st.DB(), Now: func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }, Agents: []dashboard.Agent{{ID: "alpha", DailyBudgetUSD: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- dashboard.Serve(ctx, listener, h) }()
	reqCtx, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	req, _ := http.NewRequestWithContext(reqCtx, "GET", "http://"+listener.Addr().String()+"/", nil)
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("status %d", response.StatusCode)
	}
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not shut down")
	}
	nonlocal, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer nonlocal.Close()
	if err = dashboard.Serve(context.Background(), nonlocal, h); err == nil {
		t.Fatal("accepted public listener")
	}
}
