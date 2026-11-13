package tools_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/tools"
)

func TestToolArgumentsAreStoredRedactedAndCapped(t *testing.T) {
	const secret = "sk-synthetic-detail-secret"
	h := newBrowserHarness(t, true, func(o *tools.Options) { o.Template.LLM.APIKey = config.NewSecret(secret) })
	long := strings.Repeat("é", store.MaxArgField)
	h.mustCall("search", `{"query":"`+long+`"}`)
	if _, err := h.call("post", `{"text":"carries `+secret+`"}`); err == nil {
		t.Fatal("a post with a secret was accepted")
	}
	calls, err := h.store.WakeCalls(context.Background(), "agent", 1)
	if err != nil || len(calls) != 2 {
		t.Fatalf("%d calls, %v", len(calls), err)
	}
	var search struct {
		Query string `json:"query"`
	}
	if err = json.Unmarshal(calls[0].Args, &search); err != nil || len(search.Query) > store.MaxArgField || len(search.Query) < store.MaxArgField-2 {
		t.Fatalf("search query stored with %d bytes, %v", len(search.Query), err)
	}
	if got := string(calls[1].Args); strings.Contains(got, secret) || got != `{"keys":["text"]}` {
		t.Fatalf("secret-bearing arguments stored as %s", got)
	}
	if calls[0].Detail.Counts["posts"] != 1 {
		t.Fatalf("read detail %+v", calls[0].Detail)
	}
}

func TestFailedBrowserStepSavesItsSnapshotUnderARing(t *testing.T) {
	const secret = "sk-synthetic-evidence-secret"
	var logs bytes.Buffer
	logger, err := logging.New(&logs, "debug", "json")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	h := newBrowserHarnessWithLogger(t, false, logger, func(o *tools.Options) {
		o.Template.LLM.APIKey = config.NewSecret(secret)
		o.EvidenceDir = dir
	})
	agentDir := filepath.Join(dir, "agent")
	if err = os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(agentDir, "20260101T000000.000-w1-reply-submit.txt")
	if err = os.WriteFile(stale, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.backend.Errors[threads.OpReply] = &threads.StageError{Stage: threads.StageComposerMissing, Origin: "https://threads.com/", Snapshot: "main\n  textbox " + secret + "\n  link \"@someone\"", Err: fmt.Errorf("threads reply: %w", threads.ErrWriteRefused)}
	for i := 0; i < tools.EvidencePerAgent+2; i++ {
		url := fmt.Sprintf("https://threads.com/@synthetic_user%d/post/p%d", i, i)
		h.guard.NoteRead("", url, "")
		h.next()
		if _, err = h.call("reply", fmt.Sprintf(`{"text":"Synthetic reply","target_url":%q,"target_username":"synthetic_user%d"}`, url, i)); err == nil {
			t.Fatal("reply should have failed")
		}
	}
	files, err := os.ReadDir(agentDir)
	if err != nil || len(files) != tools.EvidencePerAgent {
		t.Fatalf("%d snapshots kept, %v", len(files), err)
	}
	if _, err = os.Stat(stale); err == nil {
		t.Fatal("a snapshot older than the transcripts' retention was kept")
	}
	calls, err := h.store.WakeCalls(context.Background(), "agent", 1)
	if err != nil {
		t.Fatal(err)
	}
	last := calls[len(calls)-1]
	if last.Detail.Stage != threads.StageComposerMissing || !strings.HasPrefix(last.Detail.Evidence, agentDir) || last.Detail.ActionID == 0 {
		t.Fatalf("detail %+v", last.Detail)
	}
	saved, err := os.ReadFile(last.Detail.Evidence)
	if err != nil || strings.Contains(string(saved), secret) || !strings.Contains(string(saved), "textbox [REDACTED]") {
		t.Fatalf("snapshot %q, %v", saved, err)
	}
	if info, _ := os.Stat(last.Detail.Evidence); info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode %v", info.Mode())
	}
	if !strings.Contains(logs.String(), logging.BrowserEvidenceSaved) || !strings.Contains(logs.String(), last.Detail.Evidence) || strings.Contains(logs.String(), secret) {
		t.Fatal("the saved path is not logged, or the log leaked")
	}
	if string(last.Result) == "" || !strings.Contains(string(last.Result), `"reason":"write_refused"`) {
		t.Fatalf("result %s", last.Result)
	}
}
