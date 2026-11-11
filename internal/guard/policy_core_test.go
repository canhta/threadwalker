package guard_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/guard"
)

func TestPolicyReservesQuotaBeforeEffect(t *testing.T) {
	st, opts := setup(t)
	opts.Template.Limits.PostsPerDay = 1
	opts.Template.Limits.RepliesPerDay = 3
	opts.Template.Limits.RepliesPerUser7d = 2
	opts.Template.Limits.MinSecondsBetweenActions = 1
	g, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	effect := func() (string, error) {
		calls++
		counts, err := st.CountActions(context.Background(), "agent", opts.Now().Add(-time.Second), opts.Now().Add(time.Second))
		if err != nil || counts.Posts != 1 {
			t.Fatalf("intent missing before effect %+v %v", counts, err)
		}
		return "", errors.New("synthetic unknown effect")
	}
	if _, err = g.Execute(context.Background(), guard.Attempt{Tool: "post", Arguments: `{"text":"First synthetic action"}`}, effect); err == nil {
		t.Fatal("effect failure lost")
	}
	if _, err = g.Execute(context.Background(), guard.Attempt{Tool: "post", Arguments: `{"text":"Second synthetic action"}`}, effect); !errors.Is(err, guard.ErrBlocked) || calls != 1 {
		t.Fatalf("quota bypass %d %v", calls, err)
	}
}
