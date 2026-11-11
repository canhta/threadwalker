# Local metric polling evidence (#19)

This verifies functional code with invented action records, offline sources and injected logical
time. It does not establish current Threads selectors/insights, real engagement, elapsed soak,
account survival or production hosting. No real account, VPS, Telegram, paid provider or secret
file was used. The behavioral contract lives in [the spec](../spec.md#7-learning), implementation
in [`internal/metrics`](../../internal/metrics), checkpoint DDL in
[`004_metric_jobs.sql`](../../internal/store/migrations/004_metric_jobs.sql).

At 72 logical hours, the actual processor/guard/SQLite simulation produces seven confirmed
synthetic actions, 21 completed 1/6/24-hour targets and 19 actual-timestamp samples. One initially
overdue action coalesces its three targets into one current observation. At logical hour 24 a
guarded read occurs before a simulated crash; the file database closes/reopens and the expired
lease is reclaimed at hour 26. The additional read creates no duplicate sample/checkpoint:
20 source reads and 20 guarded audit rows, one restart, unchanged agent notes/schedules and zero
model usage. `simulation.json` retains the actual observation times; `events.jsonl`
retains the metadata-only sample/target and recovery events. A sample and a target are different
counts: 19 `metrics.measured` events and 21 `metrics.target.completed` events.

The shared versioned [metrics suite](../../internal/eval/testdata/metrics-v1.json) executes seven
actual-code scenarios under both default profile labels (14 passing results): three windows,
restart/catch-up, independent agents/actions, cancelled reads, unavailable metrics, DB rollback
and retry, and the multi-day simulation. These labels make no LLM requests; report execution
duration is wall-clock, while scenario hours are explicitly logical. Independent acceptance
tests add exact nanosecond timing, concurrent exclusive leases, stale fences, atomic rollback,
owner stops, authentication notices, backend cleanup and redacted logs.

An explicit local Chromium test runs the processor and guarded tool against the saved
[`action-metrics.html`](testdata/metrics/action-metrics.html) on a loopback `httptest` server.
The matching own action measures views=123, likes=7, replies=3, reposts=1; a second action with a
mismatched URL defers, with zero writes and no payload/URL in logs. A second browser can acquire
the profile after processor cleanup. This proves CDP extraction and source binding only for
that synthetic DOM, not live Threads insights.

Reproduce from the repo root (use installed Go 1.27.3; the measured binary was
`/tmp/threadwalker-go/go/bin/go`):

```sh
GOPROXY=off GOSUMDB=off go vet ./...
GOPROXY=off GOSUMDB=off go test ./...
GOPROXY=off GOSUMDB=off go test -race ./internal/metrics ./internal/store ./internal/tools ./internal/agent ./internal/threads ./internal/eval ./cmd/threadwalker
go run ./cmd/threadwalker metrics -offline -soak-hours 72 -evidence /tmp/metrics-evidence-new
go run ./cmd/threadwalker eval -suite internal/eval/testdata/metrics-v1.json -seed 19 -out /tmp/metrics-eval-new.json
go run ./cmd/threadwalker run -once -offline
CHROME_BIN=/path/to/local/chrome go test -tags browser_integration ./internal/metrics -run TestMetricProcessorWithActualLocalCDPAndBoundSyntheticDOM -count=1 -v
```

Recorded outputs, fingerprints and an immutable source revision are in
[`testdata/metrics`](testdata/metrics). The simulation command requires a fresh evidence
directory, uses a disposable DB and reads no credentials. Routine tests require no Chromium.
Live qualification remains in #2/#18/#21/#9.
