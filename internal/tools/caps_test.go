package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/quality"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
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
		if reason := notClosedOrStalled(t, err); reason != "unknown_target" {
			t.Fatalf("call %d: %s", i, reason)
		}
	}
}

// A time gate that opens within the wake can open later in it, so refusing at it never closes the tool.
func TestTimeGatedRefusalsNeverCloseATool(t *testing.T) {
	st, opts, _ := setup(t)
	opts.Template.Limits.MinMinutesBetweenPosts = 20
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
	opts.Hidden = []string{"find_posts:search"}
	d, err := tools.NewDispatcher(opts)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		_, err := d.Dispatch(context.Background(), call("find_posts", `{"source":"search","query":"q"}`))
		if errors.Is(err, tools.ErrStallStop) {
			t.Fatal("stalled")
		}
		if reason, _ := blockedResult(t, err); reason != "tool_hidden" {
			t.Fatalf("call %d: %s", i, reason)
		}
	}
}

// A hidden find_posts source leaves the schema's source values, and with every
// source hidden the tool itself is not offered.
func TestDefinitionsDropHiddenFindSources(t *testing.T) {
	sources := func(hidden ...string) []string {
		_, opts, _ := setup(t)
		opts.Hidden = hidden
		d, err := tools.NewDispatcher(opts)
		if err != nil {
			t.Fatal(err)
		}
		for _, def := range d.Definitions() {
			if def.Function.Name == "find_posts" {
				var schema struct {
					Properties struct {
						Source struct {
							Enum []string `json:"enum"`
						} `json:"source"`
					} `json:"properties"`
				}
				if err := json.Unmarshal(def.Function.Parameters, &schema); err != nil {
					t.Fatal(err)
				}
				return schema.Properties.Source.Enum
			}
		}
		return nil
	}
	if got := sources(); !slices.Equal(got, []string{"feed", "topic", "search", "profile"}) {
		t.Fatalf("sources %v", got)
	}
	if got := sources("find_posts:search", "find_posts:topic"); !slices.Equal(got, []string{"feed", "profile"}) {
		t.Fatalf("sources %v", got)
	}
	all := []string{}
	for _, s := range guard.FindSources {
		all = append(all, guard.FindScope(s.Name))
	}
	if got := sources(all...); got != nil {
		t.Fatalf("find_posts offered with every source hidden: %v", got)
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
		if _, err := d.Guard().ObserveStopRequest(context.Background(), fmt.Sprintf("user%d", i), "hello", true); err != nil {
			t.Fatal(err)
		}
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

// #95: a post published, the next one is allowed after the wake ends. The first
// refusal says so and closes post; the model is not left to try again.
func TestPostSpacedPastTheWakeClosesAfterTheFirstRefusal(t *testing.T) {
	h := newBrowserHarness(t, true, func(o *tools.Options) { o.Template.Limits.MinMinutesBetweenPosts = 120 })
	if _, err := h.call("post", `{"text":"Synthetic first post"}`); err != nil {
		t.Fatal(err)
	}
	_, err := h.call("post", `{"text":"unrelated words number2"}`)
	if reason, hint := blockedResult(t, err); reason != "min_minutes_between_posts" || !strings.Contains(hint, "closed for the rest of the wake") {
		t.Fatalf("first refusal: %s %q", reason, hint)
	}
	_, err = h.call("post", `{"text":"unrelated words number3"}`)
	if reason, _ := blockedResult(t, err); reason != "tool_closed" || errors.Is(err, tools.ErrStallStop) {
		t.Fatalf("second attempt: %s %v", reason, err)
	}
}

// #95: the daily browser reply cap shuts only the browser path; replies to a
// post with an API id still go out, and the reply tool is never closed.
func TestBrowserReplyCapLeavesAPIRepliesOpen(t *testing.T) {
	h := newBrowserHarness(t, true, func(o *tools.Options) { o.Template.Limits.BrowserRepliesPerDay = 1 })
	reply := func(slug, id string) error {
		h.next()
		target := readRef(t, h.guard, "https://www.threads.com/@user"+slug+"/post/"+slug, id)
		_, err := h.call("reply", `{"text":"Synthetic reply `+slug+`","target":"`+target+`"}`)
		return err
	}
	if err := reply("a", ""); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"b", "c", "d", "e"} {
		if reason, hint := blockedResult(t, reply(slug, "")); reason != "browser_replies_per_day" || !strings.Contains(hint, "inbox item or an open reply candidate") {
			t.Fatalf("browser reply %s: %s %q", slug, reason, hint)
		}
	}
	if err := reply("f", "api-f"); err != nil {
		t.Fatalf("api reply after the browser cap: %v", err)
	}
}

func TestIdeasIsCappedPerWake(t *testing.T) {
	_, opts, _ := setup(t)
	opts.Ideas = func(context.Context, []quality.Item) ([]quality.Idea, error) { return []quality.Idea{}, nil }
	d, err := tools.NewDispatcher(opts)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		dispatch(t, d, "ideas", "{}")
	}
	_, err = d.Dispatch(context.Background(), call("ideas", "{}"))
	if reason, _ := blockedResult(t, err); reason != "limit_per_wake" {
		t.Fatalf("4th ideas call: %s", reason)
	}
}

// With post and reply both closed the wake ends on the call that closed the
// last one: no model request is left to spend on reads.
func TestWakeEndsWhenEveryWritePathIsClosed(t *testing.T) {
	h := newBrowserHarness(t, false)
	h.backend.Quota = threads.Quota{PostsUsed: 250, PostsTotal: 250, RepliesUsed: 1000, RepliesTotal: 1000}
	h.next()
	_, err := h.call("post", `{"text":"Synthetic quota post"}`)
	if reason, _ := blockedResult(t, err); reason != "api_quota_exhausted" || errors.Is(err, tools.ErrNothingToDo) || !h.dispatch.Closed("post") {
		t.Fatalf("post: %s %v", reason, err)
	}
	target := readRef(t, h.guard, "https://www.threads.com/@alice/post/a1", "api-a1")
	_, err = h.call("reply", `{"text":"Synthetic reply","target":"`+target+`"}`)
	if !errors.Is(err, tools.ErrNothingToDo) || errors.Is(err, tools.ErrStallStop) {
		t.Fatalf("last write path closed: %v", err)
	}
}

// A schema refusal names the argument, so the model knows what to fix.
func TestArgumentRefusalNamesTheArgument(t *testing.T) {
	_, _, d := setup(t)
	_, err := d.Dispatch(context.Background(), call("post", `{"text":1}`))
	reason, hint := blockedResult(t, err)
	if reason != "invalid_tool_arguments" || !strings.Contains(hint, "argument text") {
		t.Fatalf("reason %s hint %q", reason, hint)
	}
}

// read_post takes a p ref; one this wake never issued is refused before any read.
func TestReadPostResolvesOnlyIssuedRefs(t *testing.T) {
	_, _, d := setup(t)
	_, err := d.Dispatch(context.Background(), call("read_post", `{"id":"p999"}`))
	if reason, _ := blockedResult(t, err); reason != "unknown_target" {
		t.Fatalf("reason %s err %v", reason, err)
	}
}
