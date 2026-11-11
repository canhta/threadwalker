package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/canhta/threadwalker/internal/threads"
	"github.com/canhta/threadwalker/internal/tools"
)

// pipelineActionSource is a browser backend that also exposes the optional
// action-scoped reader. It is deliberately separate from the aggregate method
// so a missing optional reader can be tested without fallback.
type pipelineActionSource struct {
	fakeThreadsBackend

	mu      sync.Mutex
	urls    []string
	values  threads.Metrics
	err     error
	onRead  func(context.Context, string)
	timeout bool
}

func (s *pipelineActionSource) ReadActionMetrics(ctx context.Context, threadURL string) (threads.Metrics, error) {
	s.mu.Lock()
	s.urls = append(s.urls, threadURL)
	onRead := s.onRead
	s.mu.Unlock()
	if _, ok := ctx.Deadline(); !ok {
		s.mu.Lock()
		s.timeout = true
		s.mu.Unlock()
	}
	if onRead != nil {
		onRead(ctx, threadURL)
	}
	if err := ctx.Err(); err != nil {
		return threads.Metrics{}, err
	}
	return s.values, s.err
}

func (s *pipelineActionSource) readURLs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.urls...)
}

func (s *pipelineActionSource) missingDeadline() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.timeout
}

func insertPipelineAction(t *testing.T, h *browserHarness, id int64, agent string, dryRun bool, threadURL string) {
	t.Helper()
	dry := 0
	if dryRun {
		dry = 1
	}
	_, err := h.store.DB().Exec(`INSERT INTO actions(id,agent_id,kind,text,target_username,dry_run,posted_at,thread_url) VALUES(?,?,'reply','',?,?,?,?)`,
		id, agent, "foreign_person", dry, "2026-11-11T01:00:00Z", threadURL)
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

func TestReadOwnMetricsDefinitionKeepsAggregateAndAddsOptionalActionID(t *testing.T) {
	h := newBrowserHarness(t, true)
	var definition json.RawMessage
	for _, tool := range h.dispatch.Definitions() {
		if tool.Function.Name == "read_own_metrics" {
			definition = tool.Function.Parameters
		}
	}
	if definition == nil {
		t.Fatal("read_own_metrics definition missing")
	}
	var schema struct {
		Properties map[string]struct {
			Type    string `json:"type"`
			Minimum *int   `json:"minimum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(definition, &schema); err != nil {
		t.Fatal(err)
	}
	action, ok := schema.Properties["action_id"]
	if !ok || action.Type != "integer" || action.Minimum == nil || *action.Minimum != 1 {
		t.Fatalf("action_id schema = %+v", action)
	}
	for _, required := range schema.Required {
		if required == "action_id" {
			t.Fatal("action_id must stay optional so the aggregate call remains compatible")
		}
	}

	h.backend.metrics = threads.Metrics{Views: 31, Likes: 4, Replies: 2, Reposts: 1}
	out, err := h.call("read_own_metrics", `{}`)
	if err != nil {
		t.Fatalf("aggregate read: %v", err)
	}
	envelope := decodeBrowserEnvelope(t, out)
	var aggregate threads.Metrics
	if err := json.Unmarshal(envelope.Data, &aggregate); err != nil {
		t.Fatal(err)
	}
	if aggregate != h.backend.metrics {
		t.Fatalf("aggregate metrics = %+v", aggregate)
	}
	if h.backend.count("read_own_metrics") != 1 {
		t.Fatalf("aggregate backend calls = %d", h.backend.count("read_own_metrics"))
	}
}

func TestActionScopedReadBindsOwnURLAndReturnsActionIdentity(t *testing.T) {
	const (
		agentID   = "agent"
		actionID  = int64(711)
		threadURL = "https://threads.example/own/path?reply=one"
	)
	source := &pipelineActionSource{values: threads.Metrics{Views: 412, Likes: 9, Replies: 4, Reposts: 2}}
	h := newBrowserHarness(t, true, func(o *tools.Options) { o.Threads = source })
	insertPipelineAction(t, h, actionID, agentID, false, threadURL)

	out, err := h.call("read_own_metrics", fmt.Sprintf(`{"action_id":%d}`, actionID))
	if err != nil {
		t.Fatalf("action read: %v", err)
	}
	got := decodeActionMetrics(t, out)
	if got.ActionID != actionID || got.Metrics != source.values {
		t.Fatalf("action metrics = %+v", got)
	}
	if urls := source.readURLs(); len(urls) != 1 || urls[0] != threadURL {
		t.Fatalf("reader URLs = %v", urls)
	}
	if source.missingDeadline() {
		t.Fatal("reader context had no deadline")
	}
	if h.backend.count("read_own_metrics") != 0 {
		t.Fatal("action read fell back to the aggregate method")
	}
	if h.backend.count("post") != 0 || h.backend.count("reply") != 0 {
		t.Fatalf("metrics performed a write: %v", h.backend.calls)
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
	if source.fakeThreadsBackend.count("read_own_metrics") != 1 || len(source.readURLs()) != 1 {
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
			source := &pipelineActionSource{values: threads.Metrics{Views: 1}}
			h := newBrowserHarness(t, true, func(o *tools.Options) { o.Threads = source })
			if tc.name != "unknown" {
				insertPipelineAction(t, h, tc.wantID, tc.agent, tc.dryRun, tc.binding)
			}
			out, err := h.call("read_own_metrics", fmt.Sprintf(`{"action_id":%d}`, tc.wantID))
			if err == nil {
				t.Fatalf("unbound action accepted: %s", out)
			}
			if calls := len(source.readURLs()); calls != 0 {
				t.Fatalf("reader called %d times for %s", calls, tc.name)
			}
			if h.backend.count("read_own_metrics") != 0 {
				t.Fatal("refused action fell back to aggregate metrics")
			}
			if h.backend.count("post") != 0 || h.backend.count("reply") != 0 {
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
		`{"action_id":null}`,
		`{"action_id":"1"}`,
		`{"action_id":9223372036854775808}`,
		`{"action_id":true}`,
	} {
		t.Run(strings.NewReplacer(`{`, "", `}`, "", `"`, "", `:`, "_").Replace(args), func(t *testing.T) {
			source := &pipelineActionSource{values: threads.Metrics{Views: 1}}
			h := newBrowserHarness(t, true, func(o *tools.Options) { o.Threads = source })
			insertPipelineAction(t, h, 901, "agent", false, "https://threads.example/own")
			out, err := h.call("read_own_metrics", args)
			if err == nil {
				t.Fatalf("invalid action_id accepted: %s", out)
			}
			if len(source.readURLs()) != 0 {
				t.Fatal("invalid argument reached the optional reader")
			}
		})
	}
}

func TestActionScopedReadWithoutBackendFailsSafely(t *testing.T) {
	h := newBrowserHarness(t, true, func(o *tools.Options) { o.Threads = nil })
	insertPipelineAction(t, h, 902, "agent", false, "https://threads.example/own")

	var panicked any
	var err error
	func() {
		defer func() { panicked = recover() }()
		_, err = h.call("read_own_metrics", `{"action_id":902}`)
	}()
	if panicked != nil {
		t.Fatalf("missing backend panicked instead of failing safely: %v", panicked)
	}
	if err == nil {
		t.Fatal("action read without a backend succeeded")
	}
}

func TestActionScopedReadWithoutOptionalReaderNeverFallsBack(t *testing.T) {
	h := newBrowserHarness(t, true)
	h.backend.metrics = threads.Metrics{Views: 99}
	insertPipelineAction(t, h, 903, "agent", false, "https://threads.example/own")

	out, err := h.call("read_own_metrics", `{"action_id":903}`)
	if err == nil {
		t.Fatalf("action read without optional reader succeeded: %s", out)
	}
	if h.backend.count("read_own_metrics") != 0 {
		t.Fatal("missing optional reader fell back to aggregate metrics")
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
			source := &pipelineActionSource{values: tc.values, err: tc.err}
			h := newBrowserHarness(t, true, func(o *tools.Options) { o.Threads = source })
			insertPipelineAction(t, h, 904, "agent", false, "https://threads.example/own")
			out, err := h.call("read_own_metrics", `{"action_id":904}`)
			if err == nil {
				t.Fatalf("unsafe source result accepted: %s", out)
			}
			if strings.Contains(err.Error(), "private transport detail") {
				t.Fatalf("raw source error leaked: %v", err)
			}
			if h.backend.count("read_own_metrics") != 0 {
				t.Fatal("unsafe source result fell back to aggregate metrics")
			}
		})
	}
}
