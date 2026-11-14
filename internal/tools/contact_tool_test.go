package tools_test

import (
	"context"
	"errors"
	"testing"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/threads"
)

func markCall(username string) llm.ToolCall {
	return llm.ToolCall{ID: "dnc", Type: "function", Function: llm.CallFunction{Name: "mark_do_not_contact", Arguments: `{"username":"` + username + `"}`}}
}

func TestMarkDoNotContactRefusesSomeoneWhoDidNotAddressTheAgent(t *testing.T) {
	st, _, d := setup(t)
	_, err := d.Dispatch(context.Background(), markCall("stranger"))
	var blocked *guard.BlockedError
	if !errors.As(err, &blocked) || blocked.Reason != "not_addressed" {
		t.Fatalf("got %v", err)
	}
	var n int
	st.DB().QueryRow(`SELECT COUNT(*) FROM contacts WHERE do_not_contact=1`).Scan(&n)
	if n != 0 {
		t.Fatal("stranger was marked")
	}
}

func TestMarkDoNotContactAllowsAMentionAuthor(t *testing.T) {
	_, _, d := setup(t)
	if _, err := d.Guard().ObserveStopRequest(context.Background(), "@Asker", "thanks, bye", true); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Dispatch(context.Background(), markCall("asker")); err != nil {
		t.Fatal(err)
	}
}

func TestMarkDoNotContactUsesGuardedBuiltin(t *testing.T) {
	st, _, d := setup(t)
	d.Guard().NoteInbox(context.Background(), threads.Post{URL: "https://threads.com/@mixed.name/post/1"}, "")
	call := llm.ToolCall{ID: "dnc", Type: "function", Function: llm.CallFunction{Name: "mark_do_not_contact", Arguments: `{"username":"@Mixed.Name"}`}}
	if _, err := d.Dispatch(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	var dnc int
	var reason string
	if err := st.DB().QueryRow(`SELECT do_not_contact,reason FROM contacts WHERE username='mixed.name'`).Scan(&dnc, &reason); err != nil || dnc != 1 || reason != "agent_request" {
		t.Fatalf("dnc %d %q %v", dnc, reason, err)
	}
	var audits int
	st.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE tool='mark_do_not_contact' AND blocked_by=''`).Scan(&audits)
	if audits != 1 {
		t.Fatal("missing guard audit")
	}
}
