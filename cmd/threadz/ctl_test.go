package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/admin"
	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/api"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/store"
)

var ctlNow = time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)

type ctlServer struct {
	t       *testing.T
	socket  string
	db      string
	st      *store.Store
	fixture *agent.OfflineFixture
	id      string
	deps    ctlDeps
	service *adminService
}

// newCtlServer runs a scheduler with a Control and the admin service on a temp
// socket, the way `run` wires them, with the offline fixture as the model.
var updateCtlContext = flag.Bool("update-ctl-context", false, "rewrite the ctl context golden file")

func newCtlServer(t *testing.T) *ctlServer {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	dir, err := os.MkdirTemp("", "twctl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	s := &ctlServer{t: t, socket: filepath.Join(dir, "a.sock"), db: filepath.Join(dir, "tw.db")}
	s.st, err = store.Open(ctx, s.db)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := agent.OfflineTemplate()
	tmpl.Limits.MaxUSDPerDream, tmpl.Limits.MaxStepsPerDream = 1, 5
	s.id = tmpl.ID
	now := func() time.Time { return ctlNow }
	s.fixture = agent.NewOfflineFixture(now)
	r, err := agent.New(agent.Options{Store: s.st, Template: tmpl, Now: now, HTTPClient: s.fixture.Client(), FetchTrends: s.fixture.Trends})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.st.DB().Exec(`UPDATE agents SET next_wake_at=? WHERE id=?`, ctlNow.Add(24*time.Hour).Format(time.RFC3339Nano), tmpl.ID); err != nil {
		t.Fatal(err)
	}
	control := agent.NewControl()
	s.deps = ctlDeps{httpClient: &http.Client{}, freeBytes: func(string) (uint64, error) { return 10 << 30, nil }, now: now}
	service := &adminService{templates: []config.Template{tmpl}, runtimes: map[string]*agent.Runtime{tmpl.ID: r}, control: control, store: s.st, deps: s.deps}
	s.service = service
	ln, err := admin.Listen(s.socket)
	if err != nil {
		t.Fatal(err)
	}
	logger, _ := logging.New(io.Discard, "", "")
	served, schedDone := make(chan error, 1), make(chan error, 1)
	go func() { served <- admin.Serve(ctx, ln, service.Handle, logger) }()
	go func() {
		schedDone <- agent.Scheduler{Agents: []*agent.Runtime{r}, Control: control, Now: now, Wait: func(ctx context.Context, _ time.Duration) error { <-ctx.Done(); return ctx.Err() }}.Run(ctx, false)
	}()
	t.Cleanup(func() { cancel(); <-served; <-schedDone; s.st.Close() })
	return s
}

func (s *ctlServer) ctl(args ...string) (int, string, string) {
	s.t.Helper()
	var out, errOut bytes.Buffer
	full := append(args, "--socket", s.socket, "--db", s.db)
	code := runCtl(context.Background(), full, func(string) string { return "" }, &out, &errOut, s.deps)
	return code, out.String(), errOut.String()
}

func decode[T any](t *testing.T, text string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatalf("decoding %q: %v", text, err)
	}
	return v
}

func TestCtlStatusReportsStateScheduleSpendAndHealth(t *testing.T) {
	s := newCtlServer(t)
	code, out, stderr := s.ctl("status", "--json")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	all := decode[[]agentStatus](t, out)
	if len(all) != 1 {
		t.Fatalf("status=%s", out)
	}
	st := all[0]
	if st.Agent != s.id || st.State != "running" || st.NextKind != "agent" || !st.NextWake.Equal(ctlNow.Add(24*time.Hour)) || st.DailyCapUSD != 1 || st.SpentTodayUSD != 0 || st.LastWake != nil || len(st.Health) != 2 || !st.DryRun {
		t.Fatalf("status=%+v", st)
	}
	if code, out, _ = s.ctl("status"); code != 0 || !strings.Contains(out, "next: agent at") || !strings.Contains(out, "spend today") {
		t.Fatalf("human status=%q", out)
	}
	if code, _, stderr = s.ctl("status", "--agent", "nobody"); code == 0 || !strings.Contains(stderr, "unknown agent") {
		t.Fatalf("unknown agent: code=%d %s", code, stderr)
	}
}

func TestCtlWakeRunsInsideTheLimitsAndShowsItsTranscript(t *testing.T) {
	s := newCtlServer(t)
	code, out, stderr := s.ctl("wake", "--agent", s.id, "--json")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	o := decode[wakeOutcome](t, out)
	if o.EndReason != "completed" || o.Phase != "agent" || o.WakeID == 0 || !o.DryRun {
		t.Fatalf("outcome=%+v", o)
	}
	_, out, _ = s.ctl("wakes", "--agent", s.id, "--json")
	list := decode[[]wakeSummary](t, out)
	if len(list) != 1 || list[0].ID != o.WakeID || list[0].EndReason != "completed" {
		t.Fatalf("wakes=%s", out)
	}
	_, out, _ = s.ctl("wake-show", "1", "--agent", s.id, "--json")
	view := decode[transcriptView](t, out)
	kinds := map[string]int{}
	for _, e := range view.Entries {
		kinds[e.Kind]++
	}
	if kinds[store.TranscriptContext] != 1 || kinds[store.TranscriptToolResult] != 3 || kinds[store.TranscriptEnd] != 1 || len(view.Guard) != 4 || view.Wake.ID != 1 {
		t.Fatalf("kinds=%v guard=%d view=%s", kinds, len(view.Guard), out)
	}
	if _, out, _ = s.ctl("wake-show", "1", "--agent", s.id); !strings.Contains(out, "wake #1") || !strings.Contains(out, "tool_result") || !strings.Contains(out, "guard: post allowed") {
		t.Fatalf("rendered=%s", out)
	}
	if code, _, stderr = s.ctl("wake-show", "99", "--agent", s.id); code == 0 || !strings.Contains(stderr, "not found") {
		t.Fatalf("missing wake: %d %s", code, stderr)
	}

	// The daily cap still binds a manual wake.
	if err := s.st.RecordLLMUsage(context.Background(), s.id, o.WakeID, ctlNow.Format(time.DateOnly), store.LLMUsage{TokensIn: 1, CostUSD: 5}); err != nil {
		t.Fatal(err)
	}
	_, out, _ = s.ctl("wake", "--agent", s.id, "--json")
	if o = decode[wakeOutcome](t, out); o.EndReason != "budget" {
		t.Fatalf("over budget: %+v", o)
	}
}

func TestCtlWakeNeedsAnAgentAndAValidPhase(t *testing.T) {
	s := newCtlServer(t)
	for _, args := range [][]string{{"wake"}, {"pause"}, {"kill"}, {"metrics", "run"}, {"trends", "refresh"}, {"wake", "--agent", s.id, "--phase", "nap"}, {"bogus"}, {"metrics", "walk"}} {
		if code, _, _ := s.ctl(args...); code != exitUsage {
			t.Fatalf("%v: code=%d", args, code)
		}
	}
}

func TestCtlSecondConcurrentTriggerIsRefused(t *testing.T) {
	s := newCtlServer(t)
	inWake, release := make(chan struct{}), make(chan struct{})
	first := true
	s.fixture.Observe = func([]llm.Message) {
		if first {
			first = false
			close(inWake)
			<-release
		}
	}
	done := make(chan int, 1)
	go func() { code, _, _ := s.ctl("wake", "--agent", s.id); done <- code }()
	<-inWake
	code, _, stderr := s.ctl("wake", "--agent", s.id, "--phase", "dream")
	if code != exitFailure || !strings.Contains(stderr, "another operator job") {
		t.Fatalf("second trigger: code=%d %s", code, stderr)
	}
	if code, _, _ = s.ctl("prune"); code != exitFailure {
		t.Fatalf("prune during a wake: %d", code)
	}
	close(release)
	if code = <-done; code != 0 {
		t.Fatalf("first trigger: %d", code)
	}
	if code, _, stderr = s.ctl("prune"); code != 0 {
		t.Fatalf("prune after the wake: %d %s", code, stderr)
	}
}

func TestCtlPauseResumeKillShareTheOwnerPath(t *testing.T) {
	s := newCtlServer(t)
	state := func(args ...string) string {
		_, out, _ := s.ctl(append(args, "--agent", s.id, "--json")...)
		return decode[controlOutcome](t, out).State
	}
	if got := state("pause"); got != "paused" {
		t.Fatalf("pause: %s", got)
	}
	if code, _, stderr := s.ctl("wake", "--agent", s.id); code == 0 || !strings.Contains(stderr, "not running") {
		t.Fatalf("paused agent woke: %d %s", code, stderr)
	}
	if got := state("resume"); got != "running" {
		t.Fatalf("resume: %s", got)
	}
	if got := state("kill"); got != "killed" {
		t.Fatalf("kill: %s", got)
	}
	if got := state("resume"); got != "killed" {
		t.Fatalf("a killed agent resumed: %s", got)
	}
	var n int
	if err := s.st.DB().QueryRow(`SELECT COUNT(*) FROM owner_msgs WHERE kind='command' AND text LIKE 'operator %'`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("receipts=%d err=%v", n, err)
	}
}

func TestCtlMaintenanceCommands(t *testing.T) {
	s := newCtlServer(t)
	_, out, stderr := s.ctl("trends", "refresh", "--agent", s.id, "--json")
	if got := decode[maintenanceOutcome](t, out); got.Count != 1 || got.Step != admin.TrendsRefresh {
		t.Fatalf("trends=%s %s", out, stderr)
	}
	_, out, _ = s.ctl("metrics", "run", "--agent", s.id, "--json")
	if got := decode[maintenanceOutcome](t, out); got.Count != 0 || got.Step != admin.MetricsRun {
		t.Fatalf("metrics=%s", out)
	}
	if _, err := s.st.DB().Exec(`INSERT INTO owner_msgs(agent_id,direction,text,at,kind,event_key) VALUES(?,'out','old',?,'command','old')`, s.id, ctlNow.Add(-100*24*time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	_, out, _ = s.ctl("prune", "--json")
	if got := decode[maintenanceOutcome](t, out); got.Tables["owner_msgs"] != 1 || got.Count < 1 {
		t.Fatalf("prune=%s", out)
	}
}

func TestCtlContextIsWhatTheNextWakeSends(t *testing.T) {
	for _, phase := range []string{"agent", "dream"} {
		t.Run(phase, func(t *testing.T) {
			s := newCtlServer(t)
			var sent []llm.Message
			s.fixture.Observe = func(m []llm.Message) {
				if sent == nil {
					sent = append([]llm.Message(nil), m...)
				}
			}
			s.fixture.Calls = []llm.ToolCall{}
			code, out, stderr := s.ctl("context", "--agent", s.id, "--phase", phase, "--json")
			if code != 0 {
				t.Fatalf("code=%d %s", code, stderr)
			}
			view := decode[contextView](t, out)
			if code, _, stderr = s.ctl("wake", "--agent", s.id, "--phase", phase); code != 0 {
				t.Fatalf("wake: %d %s", code, stderr)
			}
			if len(view.Messages) != len(sent) || len(sent) < 2 {
				t.Fatalf("ctl %d messages, wake %d", len(view.Messages), len(sent))
			}
			for i, m := range sent {
				if view.Messages[i].Role != m.Role || view.Messages[i].Content != m.Content {
					t.Fatalf("message %d differs", i)
				}
			}
			if _, text, _ := s.ctl("context", "--agent", s.id, "--phase", phase); !strings.Contains(text, "=== system ===") {
				t.Fatalf("human context=%q", text)
			}
		})
	}
}

func TestCtlLogsFiltersTheJournalAndReadsTheTranscriptTable(t *testing.T) {
	s := newCtlServer(t)
	journal := `{"event":"wake.started","agent_id":"a","wake_id":1}
not json
{"event":"tool.finished","agent_id":"a","wake_id":2}
{"event":"wake.started","agent_id":"b","wake_id":2}
{"event":"wake.finished","agent_id":"a","wake_id":2}
`
	var gotArgs []string
	s.deps.journal = func(_ context.Context, args []string) (io.ReadCloser, error) {
		gotArgs = args
		return io.NopCloser(strings.NewReader(journal)), nil
	}
	_, out, _ := s.ctl("logs", "--agent", "a", "--wake", "2")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "tool.finished") || !strings.Contains(lines[1], "wake.finished") {
		t.Fatalf("filtered=%q", out)
	}
	_, out, _ = s.ctl("logs", "--event", "wake.started", "--follow")
	if strings.Count(out, "\n") != 2 || gotArgs[len(gotArgs)-1] != "-f" {
		t.Fatalf("event filter=%q args=%v", out, gotArgs)
	}

	s.ctl("wake", "--agent", s.id)
	_, out, _ = s.ctl("logs", "--source", "db", "--agent", s.id, "--wake", "1", "--event", store.TranscriptEnd)
	if got := decode[transcriptEntry](t, out); got.Kind != store.TranscriptEnd {
		t.Fatalf("db logs=%q", out)
	}
	if code, _, _ := s.ctl("logs", "--source", "db"); code != exitUsage {
		t.Fatalf("db source without a wake: %d", code)
	}
}

func TestCtlWithoutAServiceSaysSo(t *testing.T) {
	s := newCtlServer(t)
	var out, errOut bytes.Buffer
	code := runCtl(context.Background(), []string{"status", "--socket", s.socket + ".none"}, func(string) string { return "" }, &out, &errOut, s.deps)
	if code != exitFailure || !strings.Contains(errOut.String(), "admin socket is not answering") {
		t.Fatalf("code=%d %s", code, errOut.String())
	}
}

type signalWriter struct {
	once   sync.Once
	needle string
	seen   chan struct{}
}

func (w *signalWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), w.needle) {
		w.once.Do(func() { close(w.seen) })
	}
	return len(p), nil
}

// run binds the socket before the scheduler starts, so the first scheduler
// record proves the service answers.
func TestRunServesTheAdminSocketAndRemovesIt(t *testing.T) {
	dir, err := os.MkdirTemp("", "twrun")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "a.sock")
	ctx, cancel := context.WithCancel(context.Background())
	out := &signalWriter{needle: "scheduler.", seen: make(chan struct{})}
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"run", "-offline", "-socket", socket, "-db", filepath.Join(dir, "tw.db")}, func(string) string { return "" }, out, io.Discard)
	}()
	<-out.seen
	var status bytes.Buffer
	code := runCtl(ctx, []string{"status", "--json", "--socket", socket}, func(string) string { return "" }, &status, io.Discard, ctlDeps{})
	if all := decode[[]agentStatus](t, status.String()); code != 0 || len(all) != 1 || all[0].Agent != "synthetic-offline" {
		t.Fatalf("code=%d status=%s", code, status.String())
	}
	cancel()
	if code := <-done; code != 0 {
		t.Fatalf("run exited %d", code)
	}
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatalf("socket left behind: %v", err)
	}

	var errOut bytes.Buffer
	file := filepath.Join(dir, "plain")
	os.WriteFile(file, nil, 0o600)
	if code := run(context.Background(), []string{"run", "-offline", "-socket", file, "-db", filepath.Join(dir, "tw2.db")}, func(string) string { return "" }, io.Discard, &errOut); code != exitFailure || !strings.Contains(errOut.String(), "not a socket") {
		t.Fatalf("occupied socket path: code=%d %s", code, errOut.String())
	}
}

func TestCtlContextGolden(t *testing.T) {
	s := newCtlServer(t)
	code, out, stderr := s.ctl("context", "--agent", s.id, "--json")
	if code != 0 {
		t.Fatalf("code=%d %s", code, stderr)
	}
	path := filepath.Join("testdata", "ctl-context-agent.json")
	if *updateCtlContext {
		if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if out != string(want) {
		t.Fatalf("ctl context changed; inspect, then regenerate with -update-ctl-context\n%s", out)
	}
}

type noTokens struct{}

func (noTokens) Install(context.Context, string, config.Secret, time.Duration) error { return nil }

func TestCtlThreadsConnectQueuesAOneTimeLinkForTheOwner(t *testing.T) {
	s := newCtlServer(t)
	ctx := context.Background()
	service := &adminService{templates: []config.Template{{ID: s.id}}, store: s.st, deps: s.deps}
	if _, err := service.Handle(ctx, admin.Request{Command: admin.OAuthLink, Agent: s.id}); err == nil || !strings.Contains(err.Error(), "api server") {
		t.Fatalf("without a server: %v", err)
	}
	srv, err := api.New(api.Options{Agents: []api.Agent{{ID: s.id, UserID: "1"}}, AppID: "app", AppSecret: config.NewSecret("s"), VerifyToken: config.NewSecret("v"), PublicURL: "https://api.example.com", Store: s.st, Tokens: noTokens{}, Now: func() time.Time { return ctlNow }, Rand: bytes.NewReader(bytes.Repeat([]byte{7}, 48))})
	if err != nil {
		t.Fatal(err)
	}
	service.oauth = srv
	if _, err = service.Handle(ctx, admin.Request{Command: admin.OAuthLink, Agent: s.id}); err != nil {
		t.Fatal(err)
	}
	var text string
	if err = s.st.DB().QueryRow(`SELECT text FROM owner_msgs WHERE agent_id=? AND text LIKE 'Authorize Threads%'`, s.id).Scan(&text); err != nil || !strings.Contains(text, "https://api.example.com/threads/oauth/start?k=") {
		t.Fatalf("owner message %q err=%v", text, err)
	}
	if _, err = service.Handle(ctx, admin.Request{Command: admin.OAuthLink, Agent: "nobody"}); err == nil {
		t.Fatal("unknown agent accepted")
	}
}

func TestDraftSampleInputParses(t *testing.T) {
	raw, err := os.ReadFile("testdata/draft-input.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct{ Items []draftItem }
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	posts, replies, ap := 0, 0, false
	for _, it := range file.Items {
		posts += map[bool]int{true: 1}[it.Kind == "post"]
		replies += map[bool]int{true: 1}[it.Kind == "reply"]
		ap = ap || strings.Contains(it.Text, "Colette Kress")
	}
	if !ap || posts < 3 || replies < 5 {
		t.Fatalf("posts %d replies %d ap %v", posts, replies, ap)
	}
}
