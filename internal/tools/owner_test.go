package tools_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/tools"
	"testing"
	"time"
)

type ownerQueue struct {
	asked bool
	calls int
}

func (o *ownerQueue) QueueOwner(context.Context, string, string, bool) (int64, error) {
	o.calls++
	o.asked = true
	return 42, nil
}
func TestOwnerToolsUseGuardAndReturnQueuedID(t *testing.T) {
	st, opts, _ := setup(t)
	q := &ownerQueue{}
	opts.Owner = q
	opts.Scope = guard.Scope{Store: st, Template: opts.Template, WakeID: 1}
	d, err := tools.NewDispatcher(opts)
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.Dispatch(context.Background(), call("ask_owner", `{"text":"Which topic?"}`))
	if err != nil || out != `{"queued_id":42}` || !q.asked {
		t.Fatalf("ask: %s %v", out, err)
	}
	if _, err = st.DB().Exec(`UPDATE agents SET status='paused'`); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Dispatch(context.Background(), call("notify_owner", `{"text":"Notice."}`)); err == nil || q.calls != 1 {
		t.Fatalf("guard bypass %v %d", err, q.calls)
	}
}

// storeQueue queues like the Telegram service: a notification or question row
// without an event key.
type storeQueue struct{ st *store.Store }

func (q storeQueue) QueueOwner(ctx context.Context, id, text string, question bool) (int64, error) {
	kind := "notification"
	if question {
		kind = "question"
	}
	return q.st.QueueOwner(ctx, id, kind, text, "", time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
}

func TestOwnerMessagesHaveADailyCapAcrossWakes(t *testing.T) {
	st, opts, _ := setup(t)
	opts.Owner = storeQueue{st}
	// A service notice is not a tool call and does not count.
	if _, err := st.QueueOwner(context.Background(), "agent", "notice", "Browser logged out.", "notice:x", opts.Now()); err != nil {
		t.Fatal(err)
	}
	// Each wake has its own per-wake caps (notify 2, ask 1); only the day cap spans wakes.
	wakes := []struct {
		notify []string
		ask    string
	}{{[]string{"", ""}, ""}, {[]string{"", "limit_per_day"}, ""}, {[]string{"limit_per_day"}, "limit_per_day"}}
	for i, w := range wakes {
		id := int64(i + 1)
		if _, err := st.DB().Exec(`INSERT OR IGNORE INTO wakes(id,agent_id,started_at) VALUES(?,'agent','2026-11-11T00:00:00Z')`, id); err != nil {
			t.Fatal(err)
		}
		opts.Scope.WakeID = id
		d, err := tools.NewDispatcher(opts)
		if err != nil {
			t.Fatal(err)
		}
		for j, want := range w.notify {
			_, err = d.Dispatch(context.Background(), call("notify_owner", fmt.Sprintf(`{"text":"Note %d.%d"}`, id, j)))
			if got := refusal(err); got != want {
				t.Fatalf("wake %d notify %d: refused %q want %q", id, j, got, want)
			}
		}
		_, err = d.Dispatch(context.Background(), call("ask_owner", `{"text":"Which?"}`))
		if got := refusal(err); got != w.ask {
			t.Fatalf("wake %d ask: refused %q want %q", id, got, w.ask)
		}
	}
}

func refusal(err error) string {
	var blocked *guard.BlockedError
	if errors.As(err, &blocked) {
		return blocked.Reason
	}
	if err != nil {
		return err.Error()
	}
	return ""
}
