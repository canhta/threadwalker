package tools_test

import (
	"testing"

	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/tools"
)

func TestLimitedAccessReadIsRecordedAndHiddenNextWake(t *testing.T) {
	st, opts, _ := setup(t)
	memory := threads.NewMemory()
	memory.Pages[threads.OpSearch] = threads.Page{Kind: "search", LimitedAccess: "No results."}
	opts.Threads = memory
	d, err := tools.NewDispatcher(opts)
	if err != nil {
		t.Fatal(err)
	}
	dispatch(t, d, "find_posts", `{"source":"search","query":"agents"}`)
	hidden, err := st.RefusedTools(t.Context(), "agent", opts.Now())
	if err != nil || len(hidden) != 1 || hidden[0] != "find_posts:search" {
		t.Fatalf("hidden=%v err=%v", hidden, err)
	}
	opts.Hidden = hidden
	next, err := tools.NewDispatcher(opts)
	if err != nil {
		t.Fatal(err)
	}
	// The closed source is refused with a hint; the other sources stay open.
	_, err = next.Dispatch(t.Context(), call("find_posts", `{"source":"search","query":"agents"}`))
	if reason, _ := blockedResult(t, err); reason != "tool_hidden" {
		t.Fatalf("closed source: %v", err)
	}
	if _, err = next.Dispatch(t.Context(), call("find_posts", `{"source":"profile","username":"someone"}`)); err != nil {
		t.Fatalf("profile source: %v", err)
	}
}
