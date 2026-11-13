package tools_test

import (
	"context"
	"strings"
	"testing"
)

func TestPlaybookReadUsesGuardAndOwnBoundedEntries(t *testing.T) {
	st, _, d := setup(t)
	if _, err := st.DB().Exec(`INSERT INTO playbook(agent_id,text,status,created_at) VALUES('agent','own testing idea','testing','2026-11-11T00:00:00Z'),('other','foreign private idea','testing','2026-11-11T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	out, err := d.Dispatch(context.Background(), call("playbook_read", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "own testing idea") || strings.Contains(out, "foreign") {
		t.Fatal(out)
	}
	var audits int
	if err := st.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE tool='playbook_read'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audits=%d err=%v", audits, err)
	}
}
