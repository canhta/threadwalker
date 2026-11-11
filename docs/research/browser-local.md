# Local browser implementation verification (#8)

Source `b845416`, measured locally on 2026-11-11 with Go 1.27.3 and the existing
Chrome for Testing 157.0.8103.50 binary from [#20](spike-browser.md). This is
synthetic implementation evidence: no Threads account, Telegram, paid model,
VPS or real credentials were used. Canonical HTML remains in
[`testdata/browser`](testdata/browser/manifest.json).

The configured browser starts once per wake and closes on every exit. The
default backend remains an in-process dry-run stub; offline CLI operation forces
that stub. Explicit browser configuration controls the origin, executable and
agent-owned profile. Page scripts are disabled and requests stay on the configured
origin. Logout/captcha pauses only that running agent and retains a safe owner
notice even when delivery is disabled. Unknown pages fail with an operation code.

The mandatory guard commits one audit attempt and one action intent before each
submission. A recognized acknowledgement fills `thread_url`; missing/uncertain
acknowledgements and failed confirmation persistence retain the intent and end
the wake without retry. Reply quotas continue using the original target thread.
Addressed stop requests store only the contact flag, never incoming text.

## Results and saved evidence

Evidence files are in [`testdata/browser-runtime`](testdata/browser-runtime).

| Check | Result / evidence |
|---|---|
| Full offline checks | `go vet ./... && go test ./...` passed; `checks.txt` |
| Full race checks | `go test -race ./...` passed; `race.txt` |
| Actual local CDP, with race | All tagged browser/driver acceptance checks passed; `cdp-checks.txt` |
| Six guarded operations | Four reads, one local post and one local reply succeeded; `cdp-actions-audit.json` records two confirmed actions, six successful payload-free audits and two local submissions |
| Captured events | Actual CDP lifecycle/guard/tool events in `cdp-events.jsonl`; deterministic scripted-boundary trace in [`browser-v1-events.jsonl`](../../internal/eval/testdata/browser-v1-events.jsonl) |
| Default shared eval | 30/30 passed, seed 8; `default-eval.json` |
| Browser shared eval | 28/28 passed, seven scenarios × two protocol labels × two repeats, seed 8; `eval.json` |
| CLI and logical soak | `smoke.jsonl`; `soak-wakes.json` records 48 completed wakes, one interrupted wake, 49 dry-run intents and one restart over 48 **logical** hours |
| Process cleanup | Profile process checks passed and final executable scan found zero remaining measured-Chrome processes; `process-cleanup.txt` |

The fixture HTTP handler in `TestBrowserGuardedSyntheticFixtureOperations`
observes each committed action and unfinished audit before accepting its POST.
`TestBrowserGuardedUnknownSubmitIsTerminalAndNotReplayed` accepts once without
acknowledgement, retains exactly one unconfirmed intent, refuses another tool
effect and constructs a new dispatcher without replaying it.
`TestBrowserGuardedAuthStatesPauseAndQueueNotice`,
`TestBrowserGuardedDryRunUsesBrowserForReadsButNeverSubmits`,
`TestBrowserBackendRejectsForeignOriginWithoutNavigation` and
`TestBrowserCloseReleasesLeaseAndKeepsProfile` cover the actual CDP state,
dry-run, origin and lifecycle boundaries. Default fake-backend tests separately
cover callback failure/pending notice, killed-state preservation and redaction.

Runtime regressions verify cleanup after cancellation, terminal outcomes and
launch failure, while preserving partial LLM accounting. Additional guard/tool
tests cover confirmation-storage failures, original-thread quotas, addressed
stop ingestion, escaped secret redaction and exact action-ID preservation.
Shared browser evals run the production parser, guard, SQLite and wake runtime
with scripted transport outcomes; their snapshots are fingerprinted with their
suite and selected inputs. They do not require Chrome and cannot run in live mode.

## Reproduce

From the repository root, using the Go version in `go.mod`:

```sh
go vet ./... && go test ./...
go test -race ./...
CHROME_BIN=/path/to/chrome THREADWALKER_BROWSER_EVIDENCE_DIR=/tmp/browser-evidence \
  go test -race -tags browser_integration ./internal/threads \
  -run 'TestBrowser|TestDriverLocal' -count=1 -v
go run ./cmd/threadwalker eval -suite internal/eval/testdata/default.json \
  -revision b845416 -seed 8 -out /tmp/default-eval.json
go run ./cmd/threadwalker eval -suite internal/eval/testdata/browser-v1.json \
  -revision b845416 -seed 8 -repeat 2 -out /tmp/browser-eval.json
go run ./cmd/threadwalker run -once -offline
go run ./cmd/threadwalker run -offline -soak-hours 48 -evidence /tmp/browser-soak
```

The tagged tests explicitly use `NoSandbox: true` on this constrained local host,
temporary profiles and loopback fixture servers. Event durations follow injected
clocks; Go test summaries separately report actual wall time. Sandbox-enabled host behavior,
current Threads selectors, real sessions/login, account survival, datacenter-IP
behavior, production memory and elapsed live operation remain unqualified in
#2/#18/#21/#9. Simulated time and synthetic submissions are not live proof.
