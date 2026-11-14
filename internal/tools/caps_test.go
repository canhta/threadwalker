package tools_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/tools"
)

func notClosedOrStalled(t *testing.T, err error) string {
	t.Helper()
	if errors.Is(err, tools.ErrStallStop) {
		t.Fatalf("stalled: %v", err)
	}
	reason, _ := blockedResult(t, err)
	if reason == "tool_closed" || reason == "tool_hidden" {
		t.Fatalf("closed: %s", reason)
	}
	return reason
}

// Five different unread posts are five different refusals, not one repeated.
func TestRefusalsOfDifferentTargetsNeverCloseATool(t *testing.T) {
	_, _, d := setup(t)
	for i := 1; i <= 5; i++ {
		_, err := d.Dispatch(context.Background(), call("reply", unreadReply(i)))
		if reason := notClosedOrStalled(t, err); reason != "reply_target_unread" {
			t.Fatalf("call %d: %s", i, reason)
		}
	}
}

// A time gate can open later in the wake, so refusing at it never closes the tool.
func TestTimeGatedRefusalsNeverCloseATool(t *testing.T) {
	st, opts, _ := setup(t)
	opts.Template.Limits.MinMinutesBetweenPosts = 60
	opts.Scope.Template = opts.Template
	if _, err := st.SeedAction(context.Background(), store.SeededAction{AgentID: "agent", Kind: "post", Text: "earlier", DryRun: true, PostedAt: opts.Now()}); err != nil {
		t.Fatal(err)
	}
	d, err := tools.NewDispatcher(opts)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		_, err := d.Dispatch(context.Background(), call("post", fmt.Sprintf(`{"text":"unrelated words number%d"}`, i)))
		if reason := notClosedOrStalled(t, err); reason != "min_minutes_between_posts" {
			t.Fatalf("call %d: %s", i, reason)
		}
	}
	// The same post again and again is waiting for the gate, not repeating a failure.
	for i := 1; i <= 5; i++ {
		_, err := d.Dispatch(context.Background(), call("post", `{"text":"the same words each time"}`))
		if reason := notClosedOrStalled(t, err); reason != "min_minutes_between_posts" {
			t.Fatalf("same post %d: %s", i, reason)
		}
	}
}

// A hidden tool is refused, and refusing it never ends the wake.
func TestHiddenToolIsRefusedWithoutStalling(t *testing.T) {
	_, opts, _ := setup(t)
	opts.Hidden = []string{"search"}
	d, err := tools.NewDispatcher(opts)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		_, err := d.Dispatch(context.Background(), call("search", `{"query":"q"}`))
		if errors.Is(err, tools.ErrStallStop) {
			t.Fatal("stalled")
		}
		if reason, _ := blockedResult(t, err); reason != "tool_hidden" {
			t.Fatalf("call %d: %s", i, reason)
		}
	}
}

// Past the wake's tool call cap the call is refused, and the next one ends the wake.
func TestToolCallCap(t *testing.T) {
	_, _, d := setup(t)
	for i := 1; i <= tools.MaxToolCallsPerWake; i++ {
		d.Dispatch(context.Background(), call("nope", fmt.Sprintf(`{"i":%d}`, i)))
	}
	_, err := d.Dispatch(context.Background(), call("fetch_trends", "{}"))
	if reason, _ := blockedResult(t, err); reason != "tool_call_cap" || errors.Is(err, tools.ErrToolCallCap) {
		t.Fatalf("first call past the cap: %v", err)
	}
	if _, err = d.Dispatch(context.Background(), call("fetch_trends", "{}")); !errors.Is(err, tools.ErrToolCallCap) {
		t.Fatalf("second call past the cap: %v", err)
	}
}

// The tools that reach the owner or add a permanent record have their own caps.
func TestPermanentToolsAreCappedPerWake(t *testing.T) {
	_, _, d := setup(t)
	for i := 1; i <= 4; i++ {
		_, err := d.Dispatch(context.Background(), call("mark_do_not_contact", fmt.Sprintf(`{"username":"user%d"}`, i)))
		if (i == 4) != (err != nil) {
			t.Fatalf("call %d: %v", i, err)
		}
		if i == 4 {
			if reason, _ := blockedResult(t, err); reason != "limit_per_wake" {
				t.Fatalf("reason %s", reason)
			}
		}
	}
}
