package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
)

func TestWakeLogsBrowserLaunchAndClose(t *testing.T) {
	for _, mode := range []string{"ok", "launch_failed", "close_failed"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(ctx, filepath.Join(t.TempDir(), "ev.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
			tmpl := agent.OfflineTemplate()
			fixture := agent.NewOfflineFixture(func() time.Time { return now })
			backend := threads.NewMemory()
			if mode == "close_failed" {
				backend.Errors[threads.OpClose] = errors.New("close detail")
			}
			var logs bytes.Buffer
			logger, _ := logging.New(&logs, "", "")
			r, err := agent.New(agent.Options{Store: st, Template: tmpl, Logger: logger, Now: func() time.Time { return now }, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends,
				ThreadsFactory: func(context.Context, config.Template) (threads.Backend, error) {
					if mode == "launch_failed" {
						return nil, errors.New("launch detail")
					}
					return backend, nil
				}})
			if err != nil {
				t.Fatal(err)
			}
			if err = r.Start(ctx); err != nil {
				t.Fatal(err)
			}
			wake, _ := r.Wake(ctx)
			got := map[string]map[string]any{}
			dec := json.NewDecoder(&logs)
			for dec.More() {
				var rec map[string]any
				if err := dec.Decode(&rec); err != nil {
					t.Fatal(err)
				}
				if e, _ := rec["event"].(string); e == logging.BrowserLaunch || e == logging.BrowserClose {
					got[e] = rec
				}
			}
			launch, closed := got[logging.BrowserLaunch], got[logging.BrowserClose]
			wantLaunch := map[string]string{"ok": "success", "launch_failed": "error", "close_failed": "success"}[mode]
			if launch == nil || launch["outcome"] != wantLaunch || int64(launch["wake_id"].(float64)) != wake.ID || launch["phase"] != "agent" {
				t.Fatalf("launch=%v", launch)
			}
			switch mode {
			case "launch_failed":
				if closed != nil {
					t.Fatalf("close without a browser: %v", closed)
				}
			default:
				want := map[string]string{"ok": "success", "close_failed": "error"}[mode]
				if closed == nil || closed["outcome"] != want || int64(closed["wake_id"].(float64)) != wake.ID {
					t.Fatalf("close=%v", closed)
				}
			}
		})
	}
}
