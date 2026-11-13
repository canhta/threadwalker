package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/admin"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
)

const defaultAdminSocket = "/run/threadz/admin.sock"

func defaultSocketPath(getenv func(string) string) string {
	return envOr(getenv, "THREADZ_ADMIN_SOCKET", defaultAdminSocket)
}

func defaultCtlDeps() ctlDeps {
	return ctlDeps{httpClient: &http.Client{}, freeBytes: freeDiskBytes, now: time.Now, journal: runJournalctl}
}

const ctlUsage = `usage: threadz ctl <command> [flags]

Commands talk to the running service over its admin socket:
  status [--agent ID]                       state, next wake, spend, last wake, health
  wake --agent ID [--phase agent|dream|study] [--dry-run]
  pause|resume|kill --agent ID
  metrics run --agent ID                    measure the targets that are due now
  prune                                     apply the retention table now
  trends refresh --agent ID
  memory restore <journal-id> --agent ID   put an archived memory entry back (ids are on the dashboard)
  strategy revert --agent ID [--to REVISION]  restore an earlier strategy revision as a new one (default: the one before the newest)
  threads connect --agent ID                send the owner a one-time link to authorize Threads
  threads cookies --agent ID                import a cookie export (JSON or Cookie header) read from stdin into the Threads browser profile
  context --agent ID [--phase P]            the exact messages the next wake sends

Commands that read the database or the host:
  wakes --agent ID [--since 24h|RFC3339] [--limit N]
  wake-show <id>                            the stored transcript of one wake
  wake-diagnose <id>                        one wake as a funnel: reads, every post and reply with its guard verdict, editor drafts, browser stage
  wake-export <id> [--keep-text]            one wake as a replay eval case (post text of other people removed unless --keep-text)
  logs [--agent ID] [--wake ID] [--event NAME] [--follow] [--source journal|db]
  draft --agent ID --input FILE             run the writer and critic on fixed items, publish nothing
  capture-api --agent ID [--out DIR]        read-only API calls, redacted responses saved as contract-test fixtures (default internal/threads/testdata/live)
  doctor                                    check config, keys, database, browser, Telegram, disk, retention
  update                                    install the latest release (root runs it; else asks the root updater)

Every command takes --json. The socket is --socket, THREADZ_ADMIN_SOCKET or
` + defaultAdminSocket + `.
Commands that change anything name an agent: --agent, or the only template under --agents.
`

type ctlFlags struct {
	fs     *flag.FlagSet
	agent  *string
	json   *bool
	socket *string
	commonFlags
	timeout *time.Duration
}

func newCtlFlags(name string, getenv func(string) string) ctlFlags {
	fs := flag.NewFlagSet("ctl "+name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return ctlFlags{fs: fs,
		agent:       fs.String("agent", "", "agent ID"),
		json:        fs.Bool("json", false, "print JSON"),
		socket:      fs.String("socket", defaultSocketPath(getenv), "admin socket path"),
		commonFlags: addCommonFlags(fs, getenv, "data/threadz.db"),
		timeout:     fs.Duration("timeout", 30*time.Minute, "how long to wait for the service"),
	}
}

func runCtl(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, deps ctlDeps) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		w := stdout
		code := exitOK
		if len(args) == 0 {
			w, code = stderr, exitUsage
		}
		fmt.Fprint(w, ctlUsage)
		return code
	}
	name, rest := args[0], args[1:]
	switch name {
	case "status":
		return ctlStatus(ctx, getenv, rest, stdout, stderr)
	case "wake":
		return ctlWake(ctx, getenv, rest, stdout, stderr)
	case "wake-show":
		return ctlWakeShow(ctx, getenv, rest, stdout, stderr)
	case "wake-diagnose":
		return ctlWakeDiagnose(ctx, getenv, rest, stdout, stderr)
	case "wake-export":
		return ctlWakeExport(ctx, getenv, rest, stdout, stderr)
	case "pause", "resume", "kill":
		return ctlControl(ctx, getenv, name, rest, stdout, stderr)
	case "metrics":
		if len(rest) == 0 || rest[0] != "run" {
			return ctlUsageError(stderr, "metrics takes the subcommand run")
		}
		return ctlMaintenance(ctx, getenv, admin.MetricsRun, true, rest[1:], stdout, stderr)
	case "trends":
		if len(rest) == 0 || rest[0] != "refresh" {
			return ctlUsageError(stderr, "trends takes the subcommand refresh")
		}
		return ctlMaintenance(ctx, getenv, admin.TrendsRefresh, true, rest[1:], stdout, stderr)
	case "prune":
		return ctlMaintenance(ctx, getenv, admin.Prune, false, rest, stdout, stderr)
	case "memory":
		return ctlMemoryRestore(ctx, getenv, rest, stdout, stderr)
	case "strategy":
		return ctlStrategyRevert(ctx, getenv, rest, stdout, stderr)
	case "threads":
		if len(rest) > 0 && rest[0] == "cookies" {
			return ctlCookies(ctx, getenv, rest[1:], stdout, stderr, deps)
		}
		if len(rest) == 0 || rest[0] != "connect" {
			return ctlUsageError(stderr, "threads takes the subcommand connect or cookies")
		}
		return ctlMaintenance(ctx, getenv, admin.OAuthLink, true, rest[1:], stdout, stderr)
	case "context":
		return ctlContext(ctx, getenv, rest, stdout, stderr)
	case "wakes":
		return ctlWakes(ctx, getenv, rest, stdout, stderr, deps)
	case "logs":
		return ctlLogs(ctx, getenv, rest, stdout, stderr, deps)
	case "draft":
		return ctlDraft(ctx, getenv, rest, stdout, stderr)
	case "capture-api":
		return ctlCaptureAPI(ctx, getenv, rest, stdout, stderr)
	case "doctor":
		return ctlDoctor(ctx, getenv, rest, stdout, stderr, deps)
	case "update":
		return ctlUpdate(ctx, rest, getenv, stdout, stderr)
	}
	return ctlUsageError(stderr, "unknown ctl command "+name)
}

func ctlUsageError(stderr io.Writer, msg string) int {
	fmt.Fprintln(stderr, msg)
	fmt.Fprint(stderr, ctlUsage)
	return exitUsage
}

func (f ctlFlags) parse(args []string, needAgent bool, stderr io.Writer) (rest []string, ok bool) {
	if err := f.fs.Parse(args); err != nil {
		ctlUsageError(stderr, "invalid arguments: "+err.Error())
		return nil, false
	}
	if needAgent && *f.agent == "" {
		id, ok := f.soleAgentID()
		if !ok {
			ctlUsageError(stderr, "this command needs --agent: --agents holds no template, or more than one")
			return nil, false
		}
		*f.agent = id
	}
	return f.fs.Args(), true
}

// call sends one admin command and prints the result as JSON or through render.
func ctlCall[T any](ctx context.Context, f ctlFlags, req admin.Request, stdout, stderr io.Writer, render func(io.Writer, T)) int {
	callCtx, cancel := context.WithTimeout(ctx, *f.timeout)
	defer cancel()
	var out T
	if err := admin.Call(callCtx, *f.socket, req, &out); err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	return printResult(stdout, *f.json, out, render)
}

func printResult[T any](w io.Writer, asJSON bool, out T, render func(io.Writer, T)) int {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return exitOK
	}
	render(w, out)
	return exitOK
}

func ctlStatus(ctx context.Context, getenv func(string) string, args []string, stdout, stderr io.Writer) int {
	f := newCtlFlags("status", getenv)
	if _, ok := f.parse(args, false, stderr); !ok {
		return exitUsage
	}
	return ctlCall(ctx, f, admin.Request{Command: admin.Status, Agent: *f.agent}, stdout, stderr, func(w io.Writer, all []agentStatus) {
		for _, s := range all {
			mode := "live"
			if s.DryRun {
				mode = "dry run"
			}
			fmt.Fprintf(w, "%s  %s (%s)\n", s.Agent, s.State, mode)
			if !s.NextWake.IsZero() {
				fmt.Fprintf(w, "  next: %s at %s\n", s.NextKind, s.NextWake.UTC().Format(time.RFC3339))
			}
			fmt.Fprintf(w, "  spend today: $%.4f of $%.2f; dream $%.4f of $%.2f\n", s.SpentTodayUSD, s.DailyCapUSD, s.DreamSpentUSD, s.DreamCapUSD)
			if s.LastWake != nil {
				fmt.Fprintf(w, "  last wake: #%d %s, %s, %d steps\n", s.LastWake.ID, s.LastWake.Kind, orDash(s.LastWake.EndReason), s.LastWake.Steps)
			}
			fmt.Fprintf(w, "  pending metric jobs: %d\n", s.PendingMetricJobs)
			writeHealth(w, s.Health, "  ")
		}
	})
}

func orDash(s string) string {
	if s == "" {
		return "running"
	}
	return s
}

func writeHealth(w io.Writer, results []healthResult, indent string) {
	for _, r := range results {
		mark := "ok  "
		if !r.OK {
			mark = "FAIL"
		}
		fmt.Fprintf(w, "%s%s %s: %s\n", indent, mark, r.Name, r.Detail)
	}
}

func ctlWake(ctx context.Context, getenv func(string) string, args []string, stdout, stderr io.Writer) int {
	f := newCtlFlags("wake", getenv)
	phase := f.fs.String("phase", "agent", "agent, dream or study")
	dry := f.fs.Bool("dry-run", false, "force dry-run writes for this wake only")
	if _, ok := f.parse(args, true, stderr); !ok {
		return exitUsage
	}
	switch *phase {
	case "agent", "dream", "study":
	default:
		return ctlUsageError(stderr, "--phase is agent, dream or study")
	}
	return ctlCall(ctx, f, admin.Request{Command: admin.Wake, Agent: *f.agent, Phase: *phase, DryRun: *dry}, stdout, stderr, func(w io.Writer, o wakeOutcome) {
		fmt.Fprintf(w, "%s %s wake #%d ended: %s after %d steps (dry run: %t)\n", o.Agent, o.Phase, o.WakeID, o.EndReason, o.Steps, o.DryRun)
	})
}

func ctlControl(ctx context.Context, getenv func(string) string, command string, args []string, stdout, stderr io.Writer) int {
	f := newCtlFlags(command, getenv)
	if _, ok := f.parse(args, true, stderr); !ok {
		return exitUsage
	}
	return ctlCall(ctx, f, admin.Request{Command: command, Agent: *f.agent}, stdout, stderr, func(w io.Writer, o controlOutcome) {
		fmt.Fprintf(w, "%s is now %s\n", o.Agent, o.State)
	})
}

func ctlMaintenance(ctx context.Context, getenv func(string) string, command string, needAgent bool, args []string, stdout, stderr io.Writer) int {
	f := newCtlFlags(command, getenv)
	if _, ok := f.parse(args, needAgent, stderr); !ok {
		return exitUsage
	}
	return ctlCall(ctx, f, admin.Request{Command: command, Agent: *f.agent}, stdout, stderr, func(w io.Writer, o maintenanceOutcome) {
		fmt.Fprintf(w, "%s: %d\n", strings.ReplaceAll(o.Step, "_", " "), o.Count)
		for table, n := range o.Tables {
			fmt.Fprintf(w, "  %s: %d\n", table, n)
		}
	})
}

// ctlCookies reads the cookie text from stdin, never from the arguments, so it
// stays out of shell history and the process list.
func ctlCookies(ctx context.Context, getenv func(string) string, args []string, stdout, stderr io.Writer, deps ctlDeps) int {
	f := newCtlFlags("threads cookies", getenv)
	if rest, ok := f.parse(args, true, stderr); !ok || len(rest) != 0 {
		return ctlUsageError(stderr, "threads cookies takes --agent ID and the cookie export on stdin")
	}
	in := deps.stdin
	if in == nil {
		in = os.Stdin
	}
	raw, err := readCookies(in)
	if err != nil || strings.TrimSpace(raw) == "" {
		return ctlUsageError(stderr, "pipe the cookie export (JSON or Cookie header) to stdin")
	}
	return ctlCall(ctx, f, admin.Request{Command: admin.Cookies, Agent: *f.agent, Cookies: raw}, stdout, stderr, func(w io.Writer, o cookiesOutcome) {
		fmt.Fprintln(w, o.Message)
	})
}

func ctlMemoryRestore(ctx context.Context, getenv func(string) string, args []string, stdout, stderr io.Writer) int {
	f := newCtlFlags("memory restore", getenv)
	if len(args) < 2 || args[0] != "restore" {
		return ctlUsageError(stderr, "memory takes restore <journal-id>")
	}
	rest, ok := f.parse(args[2:], true, stderr)
	id, err := strconv.ParseInt(args[1], 10, 64)
	if !ok || len(rest) != 0 || err != nil || id < 1 {
		return ctlUsageError(stderr, "memory restore takes one journal id")
	}
	return ctlCall(ctx, f, admin.Request{Command: admin.MemoryRestore, Agent: *f.agent, ID: id}, stdout, stderr, func(w io.Writer, o maintenanceOutcome) {
		fmt.Fprintf(w, "restored journal row %d\n", id)
	})
}

func ctlStrategyRevert(ctx context.Context, getenv func(string) string, args []string, stdout, stderr io.Writer) int {
	f := newCtlFlags("strategy revert", getenv)
	to := f.fs.Int64("to", 0, "revision to restore; default the one before the newest")
	if len(args) < 1 || args[0] != "revert" {
		return ctlUsageError(stderr, "strategy takes the subcommand revert")
	}
	rest, ok := f.parse(args[1:], true, stderr)
	if !ok || len(rest) != 0 || *to < 0 {
		return ctlUsageError(stderr, "strategy revert takes --agent and optionally --to REVISION")
	}
	return ctlCall(ctx, f, admin.Request{Command: admin.StrategyRevert, Agent: *f.agent, ID: *to}, stdout, stderr, func(w io.Writer, o maintenanceOutcome) {
		fmt.Fprintf(w, "strategy restored as revision %d\n", o.Count)
	})
}

func ctlContext(ctx context.Context, getenv func(string) string, args []string, stdout, stderr io.Writer) int {
	f := newCtlFlags("context", getenv)
	phase := f.fs.String("phase", "agent", "agent, dream or study")
	if _, ok := f.parse(args, true, stderr); !ok {
		return exitUsage
	}
	return ctlCall(ctx, f, admin.Request{Command: admin.Context, Agent: *f.agent, Phase: *phase}, stdout, stderr, func(w io.Writer, v contextView) {
		for _, m := range v.Messages {
			fmt.Fprintf(w, "=== %s ===\n%s\n", m.Role, m.Content)
		}
	})
}

// openReadOnly serves the commands that read the database. They never open a
// second writer.
func openReadOnly(ctx context.Context, path string, stderr io.Writer) (*store.Store, bool) {
	openCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st, err := store.OpenReadOnly(openCtx, path)
	if err != nil {
		fmt.Fprintln(stderr, "database is unavailable; check --db")
		return nil, false
	}
	return st, true
}

func parseSince(value string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(value); err == nil && d >= 0 {
		return now.Add(-d), nil
	}
	if at, err := time.Parse(time.RFC3339, value); err == nil {
		return at, nil
	}
	return time.Time{}, errors.New("--since is a duration such as 24h or an RFC 3339 time")
}

func ctlWakes(ctx context.Context, getenv func(string) string, args []string, stdout, stderr io.Writer, deps ctlDeps) int {
	f := newCtlFlags("wakes", getenv)
	since := f.fs.String("since", "24h", "duration or RFC 3339 time")
	limit := f.fs.Int("limit", 20, "most wakes to list")
	if _, ok := f.parse(args, true, stderr); !ok {
		return exitUsage
	}
	from, err := parseSince(*since, deps.now())
	if err != nil || *limit < 1 {
		return ctlUsageError(stderr, "invalid --since or --limit")
	}
	st, ok := openReadOnly(ctx, *f.db, stderr)
	if !ok {
		return exitFailure
	}
	defer st.Close()
	rows, err := st.Wakes(ctx, *f.agent, from, *limit)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	out := make([]wakeSummary, 0, len(rows))
	for _, w := range rows {
		out = append(out, wakeSummary{ID: w.ID, Kind: w.Kind, StartedAt: w.StartedAt, EndedAt: w.EndedAt, Steps: w.Steps, EndReason: w.EndReason, CostUSD: w.CostUSD})
	}
	return printResult(stdout, *f.json, out, func(w io.Writer, all []wakeSummary) {
		for _, s := range all {
			fmt.Fprintf(w, "#%d  %s  %-7s %-16s %2d steps  $%.4f\n", s.ID, s.StartedAt.UTC().Format(time.RFC3339), s.Kind, orDash(s.EndReason), s.Steps, s.CostUSD)
		}
	})
}

// transcriptView is the --json shape of `wake-show`.
type transcriptView struct {
	Wake    wakeSummary       `json:"wake"`
	Entries []transcriptEntry `json:"entries"`
	Guard   []guardDecision   `json:"guard"`
}

type transcriptEntry struct {
	Seq  int             `json:"seq"`
	Kind string          `json:"kind"`
	At   time.Time       `json:"at"`
	Body json.RawMessage `json:"body"`
}

type guardDecision struct {
	Tool      string    `json:"tool"`
	At        time.Time `json:"at"`
	BlockedBy string    `json:"blocked_by"`
}

func ctlWakeShow(ctx context.Context, getenv func(string) string, args []string, stdout, stderr io.Writer) int {
	id, f, ok := wakeCommand("wake-show", getenv, args, stderr)
	if !ok {
		return exitUsage
	}
	st, ok := openReadOnly(ctx, *f.db, stderr)
	if !ok {
		return exitFailure
	}
	defer st.Close()
	view, err := loadTranscript(ctx, st, *f.agent, id)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	return printResult(stdout, *f.json, view, renderTranscript)
}

// findWake returns one wake of an agent.
func findWake(ctx context.Context, st *store.Store, agentID string, id int64) (wakeSummary, error) {
	wakes, err := st.Wakes(ctx, agentID, time.Time{}.AddDate(1, 0, 0), 1<<30)
	if err != nil {
		return wakeSummary{}, err
	}
	for _, w := range wakes {
		if w.ID == id {
			return wakeSummary{ID: w.ID, Kind: w.Kind, StartedAt: w.StartedAt, EndedAt: w.EndedAt, Steps: w.Steps, EndReason: w.EndReason, CostUSD: w.CostUSD}, nil
		}
	}
	return wakeSummary{}, fmt.Errorf("wake %d not found for agent %s", id, agentID)
}

func loadTranscript(ctx context.Context, st *store.Store, agentID string, id int64) (transcriptView, error) {
	var view transcriptView
	wake, err := findWake(ctx, st, agentID, id)
	if err != nil {
		return view, err
	}
	view.Wake = wake
	entries, err := st.Transcript(ctx, agentID, id)
	if err != nil {
		return view, err
	}
	view.Entries = []transcriptEntry{}
	for _, e := range entries {
		view.Entries = append(view.Entries, transcriptEntry{Seq: e.Seq, Kind: e.Kind, At: e.At, Body: json.RawMessage(rawOrNull(e.Body))})
	}
	decisions, err := st.GuardDecisions(ctx, agentID, id)
	if err != nil {
		return view, err
	}
	view.Guard = []guardDecision{}
	for _, d := range decisions {
		view.Guard = append(view.Guard, guardDecision{Tool: d.Tool, At: d.At, BlockedBy: d.BlockedBy})
	}
	return view, nil
}

func rawOrNull(body string) string {
	if json.Valid([]byte(body)) {
		return body
	}
	return "null"
}

func renderTranscript(w io.Writer, v transcriptView) {
	fmt.Fprintf(w, "wake #%d  %s  %s  %d steps  $%.4f\n", v.Wake.ID, v.Wake.Kind, orDash(v.Wake.EndReason), v.Wake.Steps, v.Wake.CostUSD)
	if len(v.Entries) == 0 {
		fmt.Fprintln(w, "no transcript is stored for this wake (pruned or never recorded)")
	}
	for _, e := range v.Entries {
		fmt.Fprintf(w, "[%d] %s %s\n    %s\n", e.Seq, e.At.UTC().Format("15:04:05"), e.Kind, bytes.TrimSpace(e.Body))
	}
	for _, d := range v.Guard {
		verdict := "allowed"
		if d.BlockedBy != "" {
			verdict = "blocked by " + d.BlockedBy
		}
		fmt.Fprintf(w, "guard: %s %s\n", d.Tool, verdict)
	}
}

func ctlLogs(ctx context.Context, getenv func(string) string, args []string, stdout, stderr io.Writer, deps ctlDeps) int {
	f := newCtlFlags("logs", getenv)
	wake := f.fs.Int64("wake", 0, "wake id")
	event := f.fs.String("event", "", "event name")
	follow := f.fs.Bool("follow", false, "keep reading new records (journal only)")
	source := f.fs.String("source", "journal", "journal or db")
	unit := f.fs.String("unit", "threadz", "systemd unit")
	if _, ok := f.parse(args, false, stderr); !ok {
		return exitUsage
	}
	switch *source {
	case "journal":
		return logsFromJournal(ctx, deps, *unit, *follow, logFilter{agent: *f.agent, wake: *wake, event: *event}, stdout, stderr)
	case "db":
		if *wake == 0 || *f.agent == "" || *follow {
			return ctlUsageError(stderr, "--source db reads one wake: it needs --agent and --wake and cannot --follow")
		}
		st, ok := openReadOnly(ctx, *f.db, stderr)
		if !ok {
			return exitFailure
		}
		defer st.Close()
		entries, err := st.Transcript(ctx, *f.agent, *wake)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitFailure
		}
		enc := json.NewEncoder(stdout)
		for _, e := range entries {
			if *event == "" || *event == e.Kind {
				_ = enc.Encode(transcriptEntry{Seq: e.Seq, Kind: e.Kind, At: e.At, Body: json.RawMessage(rawOrNull(e.Body))})
			}
		}
		return exitOK
	}
	return ctlUsageError(stderr, "--source is journal or db")
}

type logFilter struct {
	agent string
	wake  int64
	event string
}

func (f logFilter) match(line []byte) bool {
	var rec struct {
		AgentID string `json:"agent_id"`
		WakeID  int64  `json:"wake_id"`
		Event   string `json:"event"`
	}
	if json.Unmarshal(line, &rec) != nil {
		return false
	}
	return (f.agent == "" || rec.AgentID == f.agent) && (f.wake == 0 || rec.WakeID == f.wake) && (f.event == "" || rec.Event == f.event)
}

func logsFromJournal(ctx context.Context, deps ctlDeps, unit string, follow bool, filter logFilter, stdout, stderr io.Writer) int {
	args := []string{"-u", unit, "-o", "cat", "--no-pager"}
	if follow {
		args = append(args, "-f")
	}
	r, err := deps.journal(ctx, args)
	if err != nil {
		fmt.Fprintln(stderr, "reading the journal failed; use --source db on a host without journald")
		return exitFailure
	}
	defer r.Close()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		if filter.match(scanner.Bytes()) {
			fmt.Fprintln(stdout, scanner.Text())
		}
	}
	if err = scanner.Err(); err != nil && ctx.Err() == nil {
		fmt.Fprintln(stderr, "reading the journal failed")
		return exitFailure
	}
	return exitOK
}

func ctlDoctor(ctx context.Context, getenv func(string) string, args []string, stdout, stderr io.Writer, deps ctlDeps) int {
	f := newCtlFlags("doctor", getenv)
	if _, ok := f.parse(args, false, stderr); !ok {
		return exitUsage
	}
	var results []healthResult
	templates, loadErr := f.load(config.Options{Public: true})
	switch {
	case loadErr != nil:
		var details []string
		for _, e := range unwrapErrors(loadErr) {
			details = append(details, e.Error())
		}
		results = append(results, healthResult{Name: "config", Detail: strings.Join(details, "; ")})
	default:
		results = append(results, healthResult{Name: "config", OK: true, Detail: fmt.Sprintf("%d agent templates valid", len(templates))})
		// Credentials load only for the checks that need them; a missing key
		// is reported by env_keys and leaves the secrets empty.
		if full, fullErr := f.load(config.Options{Getenv: getenv}); fullErr == nil {
			templates = full
		}
		results = append(results, runChecks(ctx, deploymentChecks(deps, getenv, templates, *f.db), false)...)
	}
	failedAny := false
	for _, r := range results {
		failedAny = failedAny || !r.OK
	}
	printResult(stdout, *f.json, results, func(w io.Writer, all []healthResult) { writeHealth(w, all, "") })
	if failedAny {
		return exitFailure
	}
	return exitOK
}
