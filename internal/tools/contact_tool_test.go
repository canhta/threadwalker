package tools_test

import (
	"context"
	"testing"

	"github.com/canhta/threadwalker/internal/llm"
)

func TestMarkDoNotContactUsesGuardedBuiltin(t *testing.T) {
	st, _, d := setup(t)
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
