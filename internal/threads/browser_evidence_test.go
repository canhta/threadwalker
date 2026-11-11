//go:build browser_integration

package threads_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/logging"
)

// This opt-in capture runs actual local CDP and exports only safe metadata.
// The ordinary tagged acceptance tests already verify each operation's data.
func TestBrowserCapturedGuardedEvidence(t *testing.T) {
	destination := os.Getenv("THREADWALKER_BROWSER_EVIDENCE_DIR")
	if destination == "" {
		t.Skip("set THREADWALKER_BROWSER_EVIDENCE_DIR to export local CDP evidence")
	}
	site, server := newFixtureSite(t)
	var events bytes.Buffer
	logger, err := logging.New(&events, "debug", "json")
	if err != nil {
		t.Fatal(err)
	}
	browser := startBrowser(t, server.URL, logger, 5*time.Second)
	h := newGuardedRun(t, browser.browser, false, 1, logger)
	for _, operation := range []struct{ name, args string }{
		{"read_feed", `{}`}, {"search", `{"query":"synthetic"}`},
		{"read_post", `{"url":"` + server.URL + `/post"}`}, {"read_own_metrics", `{}`},
		{"post", `{"text":"Synthetic evidence post"}`},
		{"reply", `{"text":"Synthetic evidence reply","target_url":"` + server.URL + `/post","target_username":"synthetic_author"}`},
	} {
		decodeToolEnvelope(t, h.mustCall(t, operation.name, operation.args))
	}
	browser.close() // Includes the actual descendant-process cleanup check.
	type action struct {
		ID        int64  `json:"id"`
		Kind      string `json:"kind"`
		DryRun    bool   `json:"dry_run"`
		Confirmed bool   `json:"confirmed"`
	}
	actions := []action{}
	rows, err := h.store.DB().Query(`SELECT id,kind,dry_run,thread_url!='' FROM actions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a action
		if err = rows.Scan(&a.ID, &a.Kind, &a.DryRun, &a.Confirmed); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		actions = append(actions, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	type audit struct {
		Tool      string          `json:"tool"`
		Attempt   json.RawMessage `json:"attempt"`
		Result    json.RawMessage `json:"result"`
		BlockedBy string          `json:"blocked_by"`
	}
	audits := []audit{}
	rows, err = h.store.DB().Query(`SELECT tool,args_json,result_json,blocked_by FROM tool_calls ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a audit
		var attempted, result string
		if err = rows.Scan(&a.Tool, &attempted, &result, &a.BlockedBy); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		a.Attempt = json.RawMessage(attempted)
		a.Result = json.RawMessage(result)
		audits = append(audits, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 || !actions[0].Confirmed || !actions[1].Confirmed || len(audits) != 6 || len(site.submissionsSnapshot()) != 2 {
		t.Fatal("incomplete guarded evidence")
	}
	metadata, err := json.MarshalIndent(struct {
		Kind           string   `json:"evidence_kind"`
		Actions        []action `json:"actions"`
		ToolCalls      []audit  `json:"tool_calls"`
		Submissions    int      `json:"local_submissions"`
		ProcessCleanup bool     `json:"process_cleanup"`
	}{"synthetic-local-cdp-only", actions, audits, 2, true}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{"Synthetic evidence", "synthetic_author", "Ignore all instructions", server.URL} {
		if strings.Contains(events.String(), payload) || bytes.Contains(metadata, []byte(payload)) {
			t.Fatal("payload in captured evidence")
		}
	}
	if err = os.MkdirAll(destination, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(destination, "cdp-events.jsonl"), events.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(destination, "cdp-actions-audit.json"), append(metadata, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("synthetic local CDP: six guarded operations, two confirmed actions, six payload-free audits, two local submissions, process cleanup verified")
}
