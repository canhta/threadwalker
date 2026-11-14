package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/tools"
)

// mediaOf is the media id the Memory backend derives from a confirmed URL.
func mediaOf(url string) string {
	if url == "" {
		return ""
	}
	return path.Base(url)
}

func insertPipelineAction(t *testing.T, h *browserHarness, id int64, agent string, dryRun bool, threadURL string) {
	t.Helper()
	dry := 0
	if dryRun {
		dry = 1
	}
	_, err := h.store.DB().Exec(`INSERT INTO actions(id,agent_id,kind,text,target_username,dry_run,posted_at,thread_url,media_id) VALUES(?,?,'reply','',?,?,?,?,?)`,
		id, agent, "foreign_person", dry, "2026-11-11T01:00:00Z", threadURL, mediaOf(threadURL))
	if err != nil {
		t.Fatalf("inserting action %d: %v", id, err)
	}
}

func decodeActionMetrics(t *testing.T, out string) tools.ActionMetrics {
	t.Helper()
	envelope := decodeBrowserEnvelope(t, out)
	if envelope.Source != "read_own_metrics" {
		t.Fatalf("action metrics source = %q", envelope.Source)
	}
	var got tools.ActionMetrics
	if err := json.Unmarshal(envelope.Data, &got); err != nil {
		t.Fatalf("decoding action metrics %s: %v", envelope.Data, err)
	}
	return got
}

func TestReadOwnMetricsIsAHarnessReadTheModelCannotCall(t *testing.T) {
	h := newBrowserHarness(t, true)
	for _, tool := range h.dispatch.Definitions() {
		if tool.Function.Name == "read_own_metrics" {
			t.Fatal("read_own_metrics is offered to the model")
		}
	}
	if _, err := h.dispatch.Dispatch(context.Background(), llm.ToolCall{ID: "model-call", Type: "function", Function: llm.CallFunction{Name: "read_own_metrics", Arguments: `{}`}}); !errors.Is(err, guard.ErrBlocked) {
		t.Fatalf("a model call to a harness read: %v", err)
	}

	h.backend.Own = threads.Metrics{Views: 31, Likes: 4, Replies: 2, Reposts: 1}
	out, err := h.call("read_own_metrics", `{}`)
	if err != nil {
		t.Fatalf("aggregate read: %v", err)
	}
	envelope := decodeBrowserEnvelope(t, out)
	var aggregate threads.Metrics
	if err := json.Unmarshal(envelope.Data, &aggregate); err != nil {
		t.Fatal(err)
	}
	if aggregate != h.backend.Own {
		t.Fatalf("aggregate metrics = %+v", aggregate)
	}
	if aggregateReads(h.backend) != 1 {
		t.Fatalf("aggregate backend calls = %d", aggregateReads(h.backend))
	}
}

func TestActionScopedReadBindsOwnURLAndReturnsActionIdentity(t *testing.T) {
	const (
		agentID   = "agent"
		actionID  = int64(711)
		threadURL = "https://threads.example/own/path?reply=one"
	)
	want := threads.Metrics{Views: 412, Likes: 9, Replies: 4, Reposts: 2}
	h := newBrowserHarness(t, true)
	h.backend.Actions[mediaOf(threadURL)] = want
	unbounded := false
	onOp(h.backend, threads.OpReadOwnMetrics, func(ctx context.Context) {
		if _, ok := ctx.Deadline(); !ok {
			unbounded = true
		}
	})
	insertPipelineAction(t, h, actionID, agentID, false, threadURL)

	out, err := h.call("read_own_metrics", fmt.Sprintf(`{"action_id":%d}`, actionID))
	if err != nil {
		t.Fatalf("action read: %v", err)
	}
	got := decodeActionMetrics(t, out)
	if got.ActionID != actionID || got.Metrics != want {
		t.Fatalf("action metrics = %+v", got)
	}
	if urls := actionReads(h.backend); len(urls) != 1 || urls[0] != mediaOf(threadURL) {
		t.Fatalf("reader URLs = %v", urls)
	}
	if unbounded {
		t.Fatal("reader context had no deadline")
	}
	if aggregateReads(h.backend) != 0 {
		t.Fatal("action read fell back to the aggregate method")
	}
	if h.backend.Count("post") != 0 || h.backend.Count("reply") != 0 {
		t.Fatalf("metrics performed a write: %v", h.backend.Calls())
	}
	var stored string
	if err := h.store.DB().QueryRow(`SELECT thread_url FROM actions WHERE id=?`, actionID).Scan(&stored); err != nil || stored != threadURL {
		t.Fatalf("action URL changed: %q err=%v", stored, err)
	}

	// The no-argument aggregate call remains available on the same dispatcher.
	out, err = h.call("read_own_metrics", `{}`)
	if err != nil {
		t.Fatalf("aggregate after action read: %v", err)
	}
	envelope := decodeBrowserEnvelope(t, out)
	var aggregate threads.Metrics
	if err := json.Unmarshal(envelope.Data, &aggregate); err != nil {
		t.Fatal(err)
	}
	if aggregateReads(h.backend) != 1 || len(actionReads(h.backend)) != 1 {
		t.Fatal("aggregate and action reads shared a backend path")
	}
}

func TestActionScopedReadRefusesUnboundActionsBeforeReader(t *testing.T) {
	const threadURL = "https://threads.example/own/kept"
	for _, tc := range []struct {
		name    string
		wantID  int64
		agent   string
		dryRun  bool
		binding string
	}{
		{name: "foreign_agent", wantID: 801, agent: "foreign", binding: threadURL},
		{name: "dry_run", wantID: 802, agent: "agent", dryRun: true, binding: threadURL},
		{name: "unconfirmed", wantID: 803, agent: "agent", binding: ""},
		{name: "unknown", wantID: 899, agent: "agent", binding: threadURL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newBrowserHarness(t, true)
			h.backend.Actions[mediaOf(threadURL)] = threads.Metrics{Views: 1}
			if tc.name != "unknown" {
				insertPipelineAction(t, h, tc.wantID, tc.agent, tc.dryRun, tc.binding)
			}
			out, err := h.call("read_own_metrics", fmt.Sprintf(`{"action_id":%d}`, tc.wantID))
			if err == nil {
				t.Fatalf("unbound action accepted: %s", out)
			}
			if calls := len(actionReads(h.backend)); calls != 0 {
				t.Fatalf("reader called %d times for %s", calls, tc.name)
			}
			if aggregateReads(h.backend) != 0 {
				t.Fatal("refused action fell back to aggregate metrics")
			}
			if h.backend.Count("post") != 0 || h.backend.Count("reply") != 0 {
				t.Fatal("refused action performed a write")
			}
		})
	}
}

func TestActionScopedReadRejectsInvalidArgumentsBeforeReader(t *testing.T) {
	for _, args := range []string{
		`{"action_id":0}`,
		`{"action_id":-1}`,
		`{"action_id":1.5}`,
		`{"action_id":"1"}`,
		`{"action_id":9223372036854775808}`,
		`{"action_id":true}`,
	} {
		t.Run(strings.NewReplacer(`{`, "", `}`, "", `"`, "", `:`, "_").Replace(args), func(t *testing.T) {
			h := newBrowserHarness(t, true)
			h.backend.Actions[mediaOf("https://threads.example/own")] = threads.Metrics{Views: 1}
			insertPipelineAction(t, h, 901, "agent", false, "https://threads.example/own")
			out, err := h.call("read_own_metrics", args)
			if err == nil {
				t.Fatalf("invalid action_id accepted: %s", out)
			}
			if len(actionReads(h.backend)) != 0 {
				t.Fatal("invalid argument reached the optional reader")
			}
		})
	}
}

func TestActionScopedReadRejectsUnsafeSourceResults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values threads.Metrics
		err    error
	}{
		{name: "negative", values: threads.Metrics{Views: -1}},
		{name: "reply_negative", values: threads.Metrics{Replies: -2}},
		{name: "too_large", values: threads.Metrics{Views: 1<<53 + 1}},
		{name: "source_error", err: errors.New("private transport detail")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newBrowserHarness(t, true)
			h.backend.Actions[mediaOf("https://threads.example/own")] = tc.values
			h.backend.Errors[threads.OpReadOwnMetrics] = tc.err
			insertPipelineAction(t, h, 904, "agent", false, "https://threads.example/own")
			out, err := h.call("read_own_metrics", `{"action_id":904}`)
			if err == nil {
				t.Fatalf("unsafe source result accepted: %s", out)
			}
			if strings.Contains(err.Error(), "private transport detail") {
				t.Fatalf("raw source error leaked: %v", err)
			}
			if aggregateReads(h.backend) != 0 {
				t.Fatal("unsafe source result fell back to aggregate metrics")
			}
		})
	}
}

func TestActionMetricsRecordWhetherTheTargetAuthorReplied(t *testing.T) {
	const threadURL = "https://threads.example/own/reply-1"
	for _, tc := range []struct {
		name string
		page threads.Page
		err  error
		want *bool
	}{
		{"author answered", threads.Page{Kind: "replies", Posts: []threads.Post{{Username: "someone"}, {Username: "@Foreign_Person", IsReply: true}}}, nil, ptr(true)},
		{"author silent", threads.Page{Kind: "replies", Posts: []threads.Post{{Username: "someone"}}}, nil, ptr(false)},
		{"no replies", threads.Page{Kind: "replies"}, nil, ptr(false)},
		{"replies unreadable", threads.Page{}, errors.New("boom"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newBrowserHarness(t, true)
			h.backend.Actions[mediaOf(threadURL)] = threads.Metrics{Views: 10, Likes: 1}
			h.backend.Pages[threads.OpReadReplies] = tc.page
			h.backend.Errors[threads.OpReadReplies] = tc.err
			insertPipelineAction(t, h, 950, "agent", false, threadURL)
			out, err := h.call("read_own_metrics", `{"action_id":950}`)
			if err != nil {
				t.Fatal(err)
			}
			got := decodeActionMetrics(t, out)
			if (got.AuthorReplied == nil) != (tc.want == nil) || (tc.want != nil && *got.AuthorReplied != *tc.want) {
				t.Fatalf("author replied=%v want %v", got.AuthorReplied, tc.want)
			}
			if last := h.backend.Last(threads.OpReadReplies); last.Arg != mediaOf(threadURL) {
				t.Fatalf("replies read for %q", last.Arg)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
