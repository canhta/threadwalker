package metrics_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/metrics"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
)

const pipelinePlantedSecret = "pipeline-planted-secret-value"

type pipelineClock struct{ now time.Time }

func (c *pipelineClock) Now() time.Time { return c.now }

// pipelineSource reads the Memory the pipeline must use only for per-action
// samples; every other operation it served is evidence of a bug.
type pipelineSource struct{ *threads.Memory }

func (s pipelineSource) actionURLs() []string {
	urls := []string{}
	for _, c := range s.Calls() {
		if c.Op == threads.OpReadOwnMetrics && c.Arg != "" {
			urls = append(urls, c.Arg)
		}
	}
	return urls
}
func (s pipelineSource) readCount() int     { return len(s.actionURLs()) }
func (s pipelineSource) readURLs() []string { return s.actionURLs() }
func (s pipelineSource) aggregateCount() int {
	return s.Count(threads.OpReadOwnMetrics) - s.readCount()
}
func (s pipelineSource) writeCount() int { return s.Count(threads.OpPost) + s.Count(threads.OpReply) }
func (s pipelineSource) closeCount() int { return s.Count(threads.OpClose) }

type pipelineOwner struct {
	mu                     sync.Mutex
	queued, browserStopped int
	browserStoppedErr      error
}

func (o *pipelineOwner) QueueOwner(context.Context, string, string, bool) (int64, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.queued++
	return 1, nil
}

func (o *pipelineOwner) BrowserStopped(context.Context, string, string, string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.browserStopped++
	return o.browserStoppedErr
}

func (o *pipelineOwner) stoppedCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.browserStopped
}

type pipelineHarness struct {
	t     *testing.T
	ctx   context.Context
	path  string
	store *store.Store

	clock  *pipelineClock
	source pipelineSource
	owner  *pipelineOwner
	logs   *bytes.Buffer
	logger *slog.Logger
	tmpl   config.Template

	factories int
}

func newPipelineHarness(t *testing.T, agentID string) *pipelineHarness {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "metrics-pipeline.db")
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("opening pipeline store: %v", err)
	}
	logs := &bytes.Buffer{}
	h := &pipelineHarness{
		t:      t,
		ctx:    ctx,
		path:   path,
		store:  st,
		clock:  &pipelineClock{now: time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)},
		source: pipelineSource{threads.NewMemory()},
		owner:  &pipelineOwner{},
		logs:   logs,
		logger: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		tmpl: config.Template{
			ID:     agentID,
			DryRun: false,
			Limits: config.LimitsConfig{
				PostsPerDay:              10,
				RepliesPerDay:            10,
				RepliesPerUser7d:         3,
				MinSecondsBetweenActions: 1,
				MinMinutesBetweenWakes:   15,
			},
			LLM: config.LLMConfig{APIKey: config.NewSecret(pipelinePlantedSecret)},
		},
	}
	t.Cleanup(func() { _ = h.store.Close() })
	h.addAgent(agentID)
	h.setSchedule(agentID, h.clock.now.Add(48*time.Hour))
	return h
}

func (h *pipelineHarness) addAgent(agentID string) {
	h.t.Helper()
	if err := h.store.EnsureAgent(h.ctx, agentID); err != nil {
		h.t.Fatalf("ensuring agent %s: %v", agentID, err)
	}
}

func (h *pipelineHarness) setSchedule(agentID string, at time.Time) {
	h.t.Helper()
	if _, err := h.store.DB().Exec(`UPDATE agents SET next_wake_at=? WHERE id=?`, at.UTC().Format(time.RFC3339Nano), agentID); err != nil {
		h.t.Fatalf("setting schedule for %s: %v", agentID, err)
	}
}

func (h *pipelineHarness) insertAction(id int64, agentID string, postedAt time.Time, threadURL string, dryRun bool) {
	h.t.Helper()
	dry := 0
	if dryRun {
		dry = 1
	}
	_, err := h.store.DB().Exec(`INSERT INTO actions(id,agent_id,kind,text,target_username,dry_run,posted_at,thread_url,media_id) VALUES(?,?,'post','',?,?,?,?,?)`,
		id, agentID, fmt.Sprintf("foreign_person_%d", id), dry, postedAt.UTC().Format(time.RFC3339Nano), threadURL, mediaID(threadURL))
	if err != nil {
		h.t.Fatalf("inserting action %d: %v", id, err)
	}
}

func (h *pipelineHarness) templateFor(agentID string) config.Template {
	tmpl := h.tmpl
	tmpl.ID = agentID
	return tmpl
}

func (h *pipelineHarness) newProcessor(agentID string, withReader bool, mutate func(*metrics.Options)) *metrics.Processor {
	h.t.Helper()
	opts := metrics.Options{
		Store:         h.store,
		Template:      h.templateFor(agentID),
		Now:           h.clock.Now,
		Logger:        h.logger,
		Owner:         h.owner,
		LeaseDuration: time.Minute,
		RetryDelay:    5 * time.Minute,
		BatchSize:     10,
		FetchTrends:   metricTrends,
		ThreadsFactory: func(context.Context, config.Template) (threads.Backend, error) {
			h.factories++
			return h.source, nil
		},
	}
	if mutate != nil {
		mutate(&opts)
	}
	p, err := metrics.New(opts)
	if err != nil {
		h.t.Fatalf("creating metrics processor: %v", err)
	}
	return p
}

func (h *pipelineHarness) reopen() {
	h.t.Helper()
	if err := h.store.Close(); err != nil {
		h.t.Fatalf("closing pipeline store: %v", err)
	}
	st, err := store.Open(h.ctx, h.path)
	if err != nil {
		h.t.Fatalf("reopening pipeline store: %v", err)
	}
	h.store = st
}

type pipelineSample struct {
	ActionID   int64
	MeasuredAt time.Time
	Views      int64
	Likes      int64
	Replies    int64
	Reposts    int64
}

func (h *pipelineHarness) samples(agentID string) []pipelineSample {
	h.t.Helper()
	rows, err := h.store.DB().Query(`SELECT action_id,measured_at,views,likes,replies,reposts FROM metrics WHERE agent_id=? ORDER BY measured_at,action_id`, agentID)
	if err != nil {
		h.t.Fatalf("reading metric samples: %v", err)
	}
	defer rows.Close()
	out := []pipelineSample{}
	for rows.Next() {
		var sample pipelineSample
		var measured string
		if err := rows.Scan(&sample.ActionID, &measured, &sample.Views, &sample.Likes, &sample.Replies, &sample.Reposts); err != nil {
			h.t.Fatalf("scanning metric sample: %v", err)
		}
		at, err := time.Parse(time.RFC3339Nano, measured)
		if err != nil {
			h.t.Fatalf("parsing measured_at %q: %v", measured, err)
		}
		sample.MeasuredAt = at
		out = append(out, sample)
	}
	if err := rows.Err(); err != nil {
		h.t.Fatalf("iterating metric samples: %v", err)
	}
	return out
}

func (h *pipelineHarness) assertSchedule(agentID string, want time.Time) {
	h.t.Helper()
	state, err := h.store.AgentState(h.ctx, agentID)
	if err != nil {
		h.t.Fatalf("reading agent state: %v", err)
	}
	if !state.NextWake.Equal(want) {
		h.t.Fatalf("%s next wake = %v, want %v", agentID, state.NextWake, want)
	}
}

func (h *pipelineHarness) assertNoLLMSpend(agentID string) {
	h.t.Helper()
	var ledger int
	if err := h.store.DB().QueryRow(`SELECT COUNT(*) FROM ledger_phases WHERE agent_id=?`, agentID).Scan(&ledger); err != nil {
		h.t.Fatalf("counting ledger rows: %v", err)
	}
	if ledger != 0 {
		h.t.Fatalf("%s recorded %d LLM ledger rows", agentID, ledger)
	}
	var tokens int64
	if err := h.store.DB().QueryRow(`SELECT COALESCE(SUM(tokens_in+tokens_out),0) FROM wakes WHERE agent_id=?`, agentID).Scan(&tokens); err != nil {
		h.t.Fatalf("summing wake tokens: %v", err)
	}
	if tokens != 0 {
		h.t.Fatalf("%s metric wakes recorded %d LLM tokens", agentID, tokens)
	}
	var agentWakes int
	if err := h.store.DB().QueryRow(`SELECT COUNT(*) FROM wakes WHERE agent_id=? AND kind='agent'`, agentID).Scan(&agentWakes); err != nil {
		h.t.Fatalf("counting agent wakes: %v", err)
	}
	if agentWakes != 0 {
		h.t.Fatalf("%s created %d agent wakes", agentID, agentWakes)
	}
}

func (h *pipelineHarness) assertMetricsWakesClosed(agentID string) {
	h.t.Helper()
	var open int
	if err := h.store.DB().QueryRow(`SELECT COUNT(*) FROM wakes WHERE agent_id=? AND kind='metrics' AND ended_at IS NULL`, agentID).Scan(&open); err != nil {
		h.t.Fatalf("counting open metric wakes: %v", err)
	}
	if open != 0 {
		h.t.Fatalf("%s left %d metric wakes open", agentID, open)
	}
}

var pipelineSafeReasons = map[string]bool{
	"metrics_unavailable": true,
	"source_unavailable":  true,
	"read_failed":         true,
	"binding_mismatch":    true,
	"auth_required":       true,
	"cancelled":           true,
	"persistence_failed":  true,
	"measurement_failed":  true,
}

func (h *pipelineHarness) assertDeferred(agentID string, actionID int64) {
	h.t.Helper()
	var reason string
	if err := h.store.DB().QueryRow(`SELECT reason FROM metric_jobs WHERE agent_id=? AND action_id=? AND window_hours=1`, agentID, actionID).Scan(&reason); err != nil {
		h.t.Fatalf("reading metric deferral reason: %v", err)
	}
	if !pipelineSafeReasons[reason] {
		h.t.Fatalf("deferral reason %q is not a safe reason code", reason)
	}
	var completed int
	if err := h.store.DB().QueryRow(`SELECT COUNT(*) FROM metric_jobs WHERE agent_id=? AND action_id=? AND completed_at IS NOT NULL`, agentID, actionID).Scan(&completed); err != nil {
		h.t.Fatalf("counting completed metric jobs: %v", err)
	}
	if completed != 0 {
		h.t.Fatalf("deferred action %d completed %d targets", actionID, completed)
	}
}

func TestPipelineCoversThreeWindowsForMultipleActionsAndAgents(t *testing.T) {
	h := newPipelineHarness(t, "pipeline-a")
	h.addAgent("pipeline-b")
	t0 := h.clock.now
	h.setSchedule("pipeline-b", t0.Add(72*time.Hour))

	type action struct {
		id     int64
		agent  string
		url    string
		values threads.Metrics
	}
	actions := []action{
		{id: 101, agent: "pipeline-a", url: "https://threads.example/own/101", values: threads.Metrics{Views: 10, Likes: 1, Replies: 2, Reposts: 3}},
		{id: 102, agent: "pipeline-a", url: "https://threads.example/own/102", values: threads.Metrics{Views: 20, Likes: 2, Replies: 3, Reposts: 4}},
		{id: 201, agent: "pipeline-b", url: "https://threads.example/own/201", values: threads.Metrics{Views: 30, Likes: 3, Replies: 4, Reposts: 5}},
	}
	for _, a := range actions {
		h.insertAction(a.id, a.agent, t0, a.url, false)
	}

	for _, hours := range []int{1, 6, 24} {
		windowAt := t0.Add(time.Duration(hours) * time.Hour)
		h.clock.now = windowAt
		for _, agentID := range []string{"pipeline-a", "pipeline-b"} {
			for _, a := range actions {
				if a.agent == agentID {
					values := a.values
					values.Views += int64(hours * 1000)
					h.source.Actions[mediaID(a.url)] = values
				}
			}
			p := h.newProcessor(agentID, true, nil)
			result, err := p.RunDue(h.ctx)
			if err != nil {
				t.Fatalf("%s at %dh: %v", agentID, hours, err)
			}
			want := 0
			for _, a := range actions {
				if a.agent == agentID {
					want++
				}
			}
			if result.ClaimedJobs != want || result.Samples != want || result.DeferredJobs != 0 {
				t.Fatalf("%s at %dh result = %+v, want %d jobs/samples", agentID, hours, result, want)
			}
			due, ok, err := p.NextDue(h.ctx)
			if err != nil {
				t.Fatalf("%s next due at %dh: %v", agentID, hours, err)
			}
			if hours == 24 {
				if ok {
					t.Fatalf("%s still due at %v after the last window", agentID, due)
				}
			} else {
				nextWindow := map[int]int{1: 6, 6: 24}[hours]
				if wantAt := t0.Add(time.Duration(nextWindow) * time.Hour); !ok || !due.Equal(wantAt) {
					t.Fatalf("%s next due at %dh = %v,%v want %v", agentID, hours, due, ok, wantAt)
				}
			}
		}
	}

	for _, a := range actions {
		got := h.samples(a.agent)
		actionSamples := []pipelineSample{}
		for _, sample := range got {
			if sample.ActionID == a.id {
				actionSamples = append(actionSamples, sample)
			}
		}
		if len(actionSamples) != 3 {
			t.Fatalf("action %d samples = %+v", a.id, actionSamples)
		}
		for i, hours := range []int{1, 6, 24} {
			wantAt := t0.Add(time.Duration(hours) * time.Hour)
			if !actionSamples[i].MeasuredAt.Equal(wantAt) {
				t.Fatalf("action %d window %dh measured at %v, want %v", a.id, hours, actionSamples[i].MeasuredAt, wantAt)
			}
			if actionSamples[i].Views != a.values.Views+int64(hours*1000) {
				t.Fatalf("action %d window %dh views = %d", a.id, hours, actionSamples[i].Views)
			}
		}
		var storedURL string
		if err := h.store.DB().QueryRow(`SELECT thread_url FROM actions WHERE id=?`, a.id).Scan(&storedURL); err != nil || storedURL != a.url {
			t.Fatalf("action %d URL = %q err=%v", a.id, storedURL, err)
		}
	}

	if h.source.aggregateCount() != 0 {
		t.Fatalf("account reads = %d, want none: the batch takes the recorded count", h.source.aggregateCount())
	}
	if h.source.writeCount() != 0 {
		t.Fatalf("action writes = %d", h.source.writeCount())
	}
	if got := h.source.readCount(); got != len(actions)*3 {
		t.Fatalf("action reads = %d, want %d", got, len(actions)*3)
	}
	if h.source.closeCount() != h.factories {
		t.Fatalf("browser closes = %d, factories = %d", h.source.closeCount(), h.factories)
	}
	h.assertSchedule("pipeline-a", t0.Add(48*time.Hour))
	h.assertSchedule("pipeline-b", t0.Add(72*time.Hour))
	h.assertNoLLMSpend("pipeline-a")
	h.assertNoLLMSpend("pipeline-b")
	h.assertMetricsWakesClosed("pipeline-a")
	h.assertMetricsWakesClosed("pipeline-b")

	// One more run after the last completed target must not read or sample.
	h.clock.now = t0.Add(25 * time.Hour)
	before := h.source.readCount()
	p := h.newProcessor("pipeline-a", true, nil)
	result, err := p.RunDue(h.ctx)
	if err != nil || result.Samples != 0 || result.ClaimedJobs != 0 {
		t.Fatalf("post-completion run = %+v err=%v", result, err)
	}
	if h.source.readCount() != before {
		t.Fatal("completed targets were read again")
	}
}

func TestPipelineLateCatchUpCoalescesToOneCurrentSampleAndRestartIsIdempotent(t *testing.T) {
	h := newPipelineHarness(t, "pipeline-late")
	t0 := h.clock.now
	const (
		actionID  = int64(301)
		threadURL = "https://threads.example/own/late"
	)
	h.insertAction(actionID, "pipeline-late", t0, threadURL, false)
	h.source.Actions[mediaID(threadURL)] = threads.Metrics{Views: 501, Likes: 51, Replies: 5, Reposts: 1}

	h.clock.now = t0.Add(25 * time.Hour)
	p := h.newProcessor("pipeline-late", true, nil)
	result, err := p.RunDue(h.ctx)
	if err != nil {
		t.Fatalf("late catch-up: %v", err)
	}
	if result.ClaimedJobs != 3 || result.Samples != 1 || result.DeferredJobs != 0 {
		t.Fatalf("late catch-up result = %+v", result)
	}
	samples := h.samples("pipeline-late")
	if len(samples) != 1 {
		t.Fatalf("late catch-up samples = %+v", samples)
	}
	if !samples[0].MeasuredAt.Equal(h.clock.now) {
		t.Fatalf("late sample measured at %v, want actual %v", samples[0].MeasuredAt, h.clock.now)
	}
	if samples[0].Views != 501 || samples[0].Likes != 51 {
		t.Fatalf("late sample values = %+v", samples[0])
	}
	if urls := h.source.readURLs(); len(urls) != 1 || urls[0] != mediaID(threadURL) {
		t.Fatalf("late reads = %v", urls)
	}
	if due, ok, err := p.NextDue(h.ctx); err != nil || ok {
		t.Fatalf("next due after catch-up = %v,%v err=%v", due, ok, err)
	}

	result, err = p.RunDue(h.ctx)
	if err != nil || result.Samples != 0 || result.ClaimedJobs != 0 {
		t.Fatalf("repeat run = %+v err=%v", result, err)
	}
	if h.source.readCount() != 1 {
		t.Fatalf("repeat run read %d times", h.source.readCount())
	}

	h.reopen()
	h.clock.now = t0.Add(25*time.Hour + time.Minute)
	restarted := h.newProcessor("pipeline-late", true, nil)
	result, err = restarted.RunDue(h.ctx)
	if err != nil || result.Samples != 0 || result.ClaimedJobs != 0 {
		t.Fatalf("restart run = %+v err=%v", result, err)
	}
	if h.source.readCount() != 1 {
		t.Fatalf("restart re-read completed targets: %v", h.source.readURLs())
	}
	if samples := h.samples("pipeline-late"); len(samples) != 1 {
		t.Fatalf("restart samples = %+v", samples)
	}
	if due, ok, err := restarted.NextDue(h.ctx); err != nil || ok {
		t.Fatalf("next due after restart = %v,%v err=%v", due, ok, err)
	}
	h.assertSchedule("pipeline-late", t0.Add(48*time.Hour))
	h.assertNoLLMSpend("pipeline-late")
	h.assertMetricsWakesClosed("pipeline-late")
	if h.source.writeCount() != 0 {
		t.Fatalf("late catch-up performed %d writes", h.source.writeCount())
	}
	if h.source.closeCount() != h.factories {
		t.Fatalf("browser closes = %d, factories = %d", h.source.closeCount(), h.factories)
	}
}

func TestPipelineDefersMissingUnavailableOrInvalidSourceWithoutSample(t *testing.T) {
	for _, tc := range []struct {
		name       string
		withReader bool
		err        error
		values     map[string]threads.Metrics
		wantReads  int
	}{
		{name: "source_unavailable", withReader: true, err: errors.New("private source detail"), wantReads: 1},
		{name: "invalid_snapshot_values", withReader: true, values: map[string]threads.Metrics{"https://threads.example/own/defer": {Views: -1}}, wantReads: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPipelineHarness(t, "pipeline-defer")
			t0 := h.clock.now
			const (
				actionID  = int64(401)
				threadURL = "https://threads.example/own/defer"
			)
			h.insertAction(actionID, "pipeline-defer", t0, threadURL, false)
			if tc.values != nil {
				for url, value := range tc.values {
					h.source.Actions[mediaID(url)] = value
				}
			}
			h.source.Errors[threads.OpReadOwnMetrics] = tc.err
			h.clock.now = t0.Add(time.Hour)
			p := h.newProcessor("pipeline-defer", tc.withReader, nil)
			result, err := p.RunDue(h.ctx)
			if err != nil {
				t.Fatalf("source failure returned an error: %v", err)
			}
			if result.Samples != 0 || result.ClaimedJobs != 1 || result.DeferredJobs != 1 {
				t.Fatalf("deferred result = %+v", result)
			}
			if h.source.readCount() != tc.wantReads {
				t.Fatalf("action reads = %d, want %d", h.source.readCount(), tc.wantReads)
			}
			if h.source.aggregateCount() != 0 {
				t.Fatalf("account reads = %d, want none", h.source.aggregateCount())
			}
			if len(h.samples("pipeline-defer")) != 0 {
				t.Fatal("source failure fabricated a metric sample")
			}
			h.assertDeferred("pipeline-defer", actionID)
			due, ok, err := p.NextDue(h.ctx)
			if err != nil || !ok || !due.After(h.clock.now) || !due.Equal(h.clock.now.Add(5*time.Minute)) {
				t.Fatalf("retry due = %v,%v err=%v", due, ok, err)
			}
			if h.source.writeCount() != 0 {
				t.Fatalf("source failure performed %d writes", h.source.writeCount())
			}
			if h.source.closeCount() != h.factories {
				t.Fatalf("browser closes = %d, factories = %d", h.source.closeCount(), h.factories)
			}
			h.assertSchedule("pipeline-defer", t0.Add(48*time.Hour))
			h.assertNoLLMSpend("pipeline-defer")
			h.assertMetricsWakesClosed("pipeline-defer")
			logs := h.logs.String()
			for _, secret := range []string{"private source detail", threadURL, "foreign_person_401", pipelinePlantedSecret} {
				if strings.Contains(logs, secret) {
					t.Fatalf("logs contain %q: %s", secret, logs)
				}
			}
		})
	}
}

func TestPipelinePausedKilledCancellationAndDatabaseFailure(t *testing.T) {
	const threadURL = "https://threads.example/own/state"

	t.Run("paused", func(t *testing.T) {
		h := newPipelineHarness(t, "pipeline-paused")
		t0 := h.clock.now
		h.insertAction(501, "pipeline-paused", t0, threadURL, false)
		if _, err := h.store.DB().Exec(`UPDATE agents SET status='paused' WHERE id='pipeline-paused'`); err != nil {
			t.Fatal(err)
		}
		h.clock.now = t0.Add(time.Hour)
		p := h.newProcessor("pipeline-paused", true, nil)
		result, err := p.RunDue(h.ctx)
		if err != nil {
			t.Fatalf("paused run: %v", err)
		}
		if result.Samples != 0 || h.factories != 0 || h.source.readCount() != 0 || len(h.samples("pipeline-paused")) != 0 {
			t.Fatalf("paused run result=%+v factories=%d reads=%d", result, h.factories, h.source.readCount())
		}
		h.assertSchedule("pipeline-paused", t0.Add(48*time.Hour))
	})

	t.Run("killed", func(t *testing.T) {
		h := newPipelineHarness(t, "pipeline-killed")
		t0 := h.clock.now
		h.insertAction(502, "pipeline-killed", t0, threadURL, false)
		if _, err := h.store.DB().Exec(`UPDATE agents SET status='killed' WHERE id='pipeline-killed'`); err != nil {
			t.Fatal(err)
		}
		h.clock.now = t0.Add(time.Hour)
		p := h.newProcessor("pipeline-killed", true, nil)
		result, err := p.RunDue(h.ctx)
		if err != nil {
			t.Fatalf("killed run: %v", err)
		}
		if result.Samples != 0 || h.factories != 0 || h.source.readCount() != 0 || len(h.samples("pipeline-killed")) != 0 {
			t.Fatalf("killed run result=%+v factories=%d reads=%d", result, h.factories, h.source.readCount())
		}
	})

	t.Run("cancelled_before_read", func(t *testing.T) {
		h := newPipelineHarness(t, "pipeline-cancel")
		t0 := h.clock.now
		h.insertAction(503, "pipeline-cancel", t0, threadURL, false)
		h.clock.now = t0.Add(time.Hour)
		ctx, cancel := context.WithCancel(h.ctx)
		cancel()
		p := h.newProcessor("pipeline-cancel", true, nil)
		result, err := p.RunDue(ctx)
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled run error = %v", err)
		}
		if result.Samples != 0 || h.source.readCount() != 0 || len(h.samples("pipeline-cancel")) != 0 {
			t.Fatalf("cancelled run result = %+v reads=%d", result, h.source.readCount())
		}
	})

	t.Run("cancelled_during_read", func(t *testing.T) {
		h := newPipelineHarness(t, "pipeline-cancel-read")
		t0 := h.clock.now
		h.insertAction(504, "pipeline-cancel-read", t0, threadURL, false)
		h.clock.now = t0.Add(time.Hour)
		ctx, cancel := context.WithCancel(h.ctx)
		h.source.Hook = func(_ context.Context, c threads.Call) error {
			if c.Arg != "" {
				cancel()
			}
			return nil
		}
		p := h.newProcessor("pipeline-cancel-read", true, nil)
		result, err := p.RunDue(ctx)
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled read error = %v (result %+v)", err, result)
		}
		if result.Samples != 0 || len(h.samples("pipeline-cancel-read")) != 0 {
			t.Fatalf("cancelled read result = %+v", result)
		}
		if h.source.readCount() != 1 {
			t.Fatalf("cancelled read count = %d", h.source.readCount())
		}
		if h.source.closeCount() != h.factories {
			t.Fatalf("cancelled browser closes = %d, factories = %d", h.source.closeCount(), h.factories)
		}
	})

	t.Run("database_failure", func(t *testing.T) {
		h := newPipelineHarness(t, "pipeline-db")
		t0 := h.clock.now
		h.insertAction(505, "pipeline-db", t0, threadURL, false)
		h.clock.now = t0.Add(time.Hour)
		p := h.newProcessor("pipeline-db", true, nil)
		if err := h.store.Close(); err != nil {
			t.Fatalf("closing store: %v", err)
		}
		result, err := p.RunDue(h.ctx)
		if err == nil {
			t.Fatalf("closed DB run succeeded: %+v", result)
		}
		if h.source.readCount() != 0 {
			t.Fatal("closed DB run reached the source")
		}
	})
}

func TestPipelineStaleLeaseRecoversWithFreshFenceAndOneSample(t *testing.T) {
	h := newPipelineHarness(t, "pipeline-lease")
	t0 := h.clock.now
	const (
		actionID  = int64(601)
		threadURL = "https://threads.example/own/lease"
	)
	h.insertAction(actionID, "pipeline-lease", t0, threadURL, false)
	if _, err := h.store.SeedMetricJobs(h.ctx, "pipeline-lease", time.Unix(0, 0)); err != nil {
		t.Fatalf("seeding metric jobs: %v", err)
	}
	now := t0.Add(time.Hour)
	stale, err := h.store.ClaimMetricJobs(h.ctx, "pipeline-lease", now, time.Minute, 10)
	if err != nil || len(stale) != 1 {
		t.Fatalf("stale claim = %+v err=%v", stale, err)
	}

	h.clock.now = now
	p := h.newProcessor("pipeline-lease", true, nil)
	result, err := p.RunDue(h.ctx)
	if err != nil {
		t.Fatalf("live lease run: %v", err)
	}
	if result.Samples != 0 || result.ClaimedJobs != 0 || h.source.readCount() != 0 {
		t.Fatalf("live lease was stolen: result=%+v reads=%d", result, h.source.readCount())
	}
	if due, ok, err := p.NextDue(h.ctx); err != nil || !ok || !due.Equal(now.Add(time.Minute)) {
		t.Fatalf("lease expiry due = %v,%v err=%v", due, ok, err)
	}

	h.clock.now = now.Add(time.Minute)
	h.source.Actions[mediaID(threadURL)] = threads.Metrics{Views: 77, Likes: 7}
	result, err = p.RunDue(h.ctx)
	if err != nil {
		t.Fatalf("expired lease run: %v", err)
	}
	if result.ClaimedJobs != 1 || result.Samples != 1 {
		t.Fatalf("expired lease recovery result = %+v", result)
	}
	samples := h.samples("pipeline-lease")
	if len(samples) != 1 || !samples[0].MeasuredAt.Equal(h.clock.now) || samples[0].Views != 77 {
		t.Fatalf("recovered sample = %+v", samples)
	}
	var version int64
	var completed int
	if err := h.store.DB().QueryRow(`SELECT MAX(claim_version) FROM metric_jobs WHERE agent_id='pipeline-lease' AND action_id=?`, actionID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := h.store.DB().QueryRow(`SELECT COUNT(*) FROM metric_jobs WHERE agent_id='pipeline-lease' AND action_id=? AND completed_at IS NOT NULL`, actionID).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if version <= stale[0].ClaimVersion || completed != 1 {
		t.Fatalf("fence version = %d (stale %d), completed = %d", version, stale[0].ClaimVersion, completed)
	}
	if h.source.writeCount() != 0 {
		t.Fatal("lease recovery performed a write")
	}
	h.assertMetricsWakesClosed("pipeline-lease")
	h.assertNoLLMSpend("pipeline-lease")
	if h.source.closeCount() != h.factories {
		t.Fatalf("browser closes = %d, factories = %d", h.source.closeCount(), h.factories)
	}
}

func TestPipelineUsesGuardedMetricsWakeAuditAndRedactedLogs(t *testing.T) {
	h := newPipelineHarness(t, "pipeline-audit")
	t0 := h.clock.now
	const (
		actionID  = int64(701)
		threadURL = "https://threads.example/own/keep-away-path"
	)
	h.insertAction(actionID, "pipeline-audit", t0, threadURL, false)
	h.source.Actions[mediaID(threadURL)] = threads.Metrics{Views: 412, Likes: 19, Replies: 4, Reposts: 2}
	h.clock.now = t0.Add(time.Hour)
	p := h.newProcessor("pipeline-audit", true, nil)
	result, err := p.RunDue(h.ctx)
	if err != nil || result.Samples != 1 {
		t.Fatalf("audit run = %+v err=%v", result, err)
	}
	h.assertMetricsWakesClosed("pipeline-audit")
	h.assertNoLLMSpend("pipeline-audit")
	h.assertSchedule("pipeline-audit", t0.Add(48*time.Hour))

	rows, err := h.store.DB().Query(`SELECT w.kind,tc.blocked_by,tc.args_json,tc.result_json FROM tool_calls tc JOIN wakes w ON w.id=tc.wake_id WHERE tc.agent_id='pipeline-audit' AND tc.tool='read_own_metrics'`)
	if err != nil {
		t.Fatalf("reading audit rows: %v", err)
	}
	defer rows.Close()
	type auditRow struct{ kind, blocked, args, result string }
	audit := []auditRow{}
	for rows.Next() {
		var row auditRow
		if err := rows.Scan(&row.kind, &row.blocked, &row.args, &row.result); err != nil {
			t.Fatal(err)
		}
		audit = append(audit, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(audit) != 1 {
		t.Fatalf("read_own_metrics audit rows = %d, want the action read", len(audit))
	}
	if audit[0].kind != "metrics" {
		t.Fatalf("audit wake kind = %q, want metrics", audit[0].kind)
	}
	if audit[0].blocked != "" {
		t.Fatalf("audit was blocked by %q", audit[0].blocked)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(audit[0].args), &args); err != nil {
		t.Fatalf("decoding audit args: %v", err)
	}
	if len(args) != 2 || args["bytes"] == nil || args["valid_object"] == nil {
		t.Fatalf("audit args retained non-metadata fields: %s", audit[0].args)
	}
	var effect map[string]any
	if err := json.Unmarshal([]byte(audit[0].result), &effect); err != nil {
		t.Fatalf("decoding audit result: %v", err)
	}
	if len(effect) != 2 || effect["outcome"] != "success" || effect["bytes"] == nil {
		t.Fatalf("audit result retained source values: %s", audit[0].result)
	}

	logs := h.logs.String()
	for _, sensitive := range []string{threadURL, "keep-away-path", "foreign_person_701", pipelinePlantedSecret, "aggregate metrics are not"} {
		if strings.Contains(logs, sensitive) {
			t.Fatalf("logs contain %q: %s", sensitive, logs)
		}
	}
	if h.source.aggregateCount() != 0 || h.source.writeCount() != 0 {
		t.Fatalf("aggregate reads=%d writes=%d", h.source.aggregateCount(), h.source.writeCount())
	}
	if h.source.closeCount() != h.factories {
		t.Fatalf("browser closes = %d, factories = %d", h.source.closeCount(), h.factories)
	}
}

func TestPipelineAuthPausesAgentAndQueuesNoticeWithoutSampleOrWrite(t *testing.T) {
	h := newPipelineHarness(t, "pipeline-auth")
	t0 := h.clock.now
	const (
		actionID  = int64(801)
		threadURL = "https://threads.example/own/auth"
	)
	h.insertAction(actionID, "pipeline-auth", t0, threadURL, false)
	h.source.Errors[threads.OpReadOwnMetrics] = threads.ErrLoggedOut
	h.clock.now = t0.Add(time.Hour)
	p := h.newProcessor("pipeline-auth", true, nil)
	result, err := p.RunDue(h.ctx)
	if err != nil {
		t.Fatalf("auth source failure returned an error: %v", err)
	}
	if result.Samples != 0 || len(h.samples("pipeline-auth")) != 0 {
		t.Fatalf("auth source produced a sample: %+v", result)
	}
	if h.source.readCount()+h.source.aggregateCount() != 1 {
		t.Fatalf("auth read count = %d", h.source.readCount()+h.source.aggregateCount())
	}
	if h.source.writeCount() != 0 {
		t.Fatalf("auth path performed %d action writes", h.source.writeCount())
	}
	state, err := h.store.AgentState(h.ctx, "pipeline-auth")
	if err != nil {
		t.Fatalf("reading agent state: %v", err)
	}
	if state.Status != "paused" {
		t.Fatalf("auth status = %q, want paused", state.Status)
	}
	if h.owner.stoppedCount() == 0 {
		pending, err := h.store.PendingOwner(h.ctx, []string{"pipeline-auth"})
		if err != nil || len(pending) == 0 {
			t.Fatalf("auth queued no owner notice: pending=%d err=%v", len(pending), err)
		}
	}
	if due, ok, err := p.NextDue(h.ctx); err != nil || ok {
		t.Fatalf("paused agent remained due at %v,%v err=%v", due, ok, err)
	}
	if h.source.closeCount() != h.factories {
		t.Fatalf("auth browser closes = %d, factories = %d", h.source.closeCount(), h.factories)
	}
	h.assertMetricsWakesClosed("pipeline-auth")
	h.assertNoLLMSpend("pipeline-auth")
}

// mediaID is the media id the Memory backend derives from a confirmed URL.
func mediaID(url string) string {
	if url == "" {
		return ""
	}
	return path.Base(url)
}
