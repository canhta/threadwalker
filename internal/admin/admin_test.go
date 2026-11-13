package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/canhta/threadz/internal/admin"
	"github.com/canhta/threadz/internal/logging"
)

func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "tw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "admin.sock")
}

func serve(t *testing.T, path string, h admin.Handler) *bytes.Buffer {
	t.Helper()
	ln, err := admin.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger, _ := logging.New(&logs, "", "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- admin.Serve(ctx, ln, h, logger) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	return &logs
}

func TestSocketIsOwnerOnlyAndRoundTripsARequest(t *testing.T) {
	path := socketPath(t)
	logs := serve(t, path, func(_ context.Context, r admin.Request) (any, error) {
		return map[string]string{"agent": r.Agent, "phase": r.Phase}, nil
	})
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("mode=%v err=%v", info.Mode(), err)
	}
	var out map[string]string
	if err = admin.Call(context.Background(), path, admin.Request{Command: admin.Wake, Agent: "a1", Phase: "dream"}, &out); err != nil {
		t.Fatal(err)
	}
	if out["agent"] != "a1" || out["phase"] != "dream" {
		t.Fatalf("out=%v", out)
	}
	var rec map[string]any
	if err = json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &rec); err != nil {
		t.Fatalf("log=%q err=%v", logs.String(), err)
	}
	if rec["event"] != logging.AdminCommand || rec["operation"] != admin.Wake || rec["outcome"] != "success" || rec["agent_id"] != "a1" {
		t.Fatalf("audit=%v", rec)
	}
}

func TestErrorsAreTypedAndAudited(t *testing.T) {
	path := socketPath(t)
	logs := serve(t, path, func(_ context.Context, r admin.Request) (any, error) {
		if r.Agent == "busy" {
			return nil, admin.ErrBusy
		}
		return nil, errors.New("starting wake: agent is not running")
	})
	ctx := context.Background()
	if err := admin.Call(ctx, path, admin.Request{Command: admin.Wake, Agent: "busy"}, nil); !errors.Is(err, admin.ErrBusy) {
		t.Fatalf("busy: %v", err)
	}
	err := admin.Call(ctx, path, admin.Request{Command: admin.Pause, Agent: "x"}, nil)
	if err == nil || err.Error() != "starting wake: agent is not running" || errors.Is(err, admin.ErrBusy) {
		t.Fatalf("failed: %v", err)
	}
	if err = admin.Call(ctx, path, admin.Request{Command: "format_disk"}, nil); err == nil {
		t.Fatal("unknown command accepted")
	}
	var reasons []string
	dec := json.NewDecoder(logs)
	for dec.More() {
		var rec map[string]any
		if err := dec.Decode(&rec); err != nil {
			t.Fatal(err)
		}
		if rec["event"] != logging.AdminCommand {
			t.Fatalf("event=%v", rec["event"])
		}
		reasons = append(reasons, rec["reason"].(string))
	}
	if len(reasons) != 3 || reasons[0] != "busy" || reasons[1] != "failed" || reasons[2] != "unknown_command" {
		t.Fatalf("reasons=%v", reasons)
	}
}

func TestListenReplacesAStaleSocketButNotALiveOne(t *testing.T) {
	path := socketPath(t)
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()
	ln, err := admin.Listen(path)
	if err != nil {
		t.Fatalf("stale socket: %v", err)
	}
	defer ln.Close()
	if _, err = admin.Listen(path); err == nil {
		t.Fatal("second listener took a live socket")
	}
	file := filepath.Join(filepath.Dir(path), "plain")
	if err = os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Listen(file); err == nil {
		t.Fatal("regular file replaced")
	}
}

func TestCallWithoutAServerNamesTheSocket(t *testing.T) {
	err := admin.Call(context.Background(), socketPath(t), admin.Request{Command: admin.Status}, nil)
	if !errors.Is(err, admin.ErrUnavailable) {
		t.Fatalf("err=%v", err)
	}
}
