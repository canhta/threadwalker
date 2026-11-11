package tools_test

import (
	"context"
	"github.com/canhta/threadwalker/internal/guard"
	"github.com/canhta/threadwalker/internal/tools"
	"testing"
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
	g, _ := guard.New(guard.Options{Store: st, Template: opts.Template, WakeID: 1})
	d, err := tools.NewDispatcher(g, opts)
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
