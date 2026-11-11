# Local runtime evidence (#5)

Measured offline on 2026-11-11 from runtime source `1b2d3b2`, after integrating the
local browser research decision in `83b2156`. All providers, trends and Threads data
were synthetic; commands used an empty process environment. No account, Telegram,
VPS, paid model or real Threads operation was used.

## Reproduce

From the repository root, using the Go version in `go.mod`:

```sh
go vet ./... && go test ./...
go test -race ./...
go build -o /tmp/threadwalker-runtime ./cmd/threadwalker
env -i /tmp/threadwalker-runtime run -once -offline
env -i /tmp/threadwalker-runtime eval -suite internal/eval/testdata/runtime-v1.json -seed 1 -repeat 2 -out /tmp/runtime-eval.json
env -i /tmp/threadwalker-runtime run -offline -soak-hours 48 -evidence /tmp/threadwalker-soak
```

Use a fresh evidence directory. The smoke defaults to an isolated temporary DB;
adding `-db` retains it for inspection. No environment key is resolved offline.
The soak exports all metadata events, all wake summaries and the shared eval report.

## Observed

| Check | Result |
|---|---|
| Full offline vet/test | All packages passed |
| Full race run | All packages passed |
| Compiled smoke | 1 completed wake, 5 audited tool calls, 1 note, 1 action with `dry_run=1` |
| Shared runtime eval, 2 repeats | 32/32 passed, OpenAI and Anthropic synthetic protocols |
| Logical soak | 48 logical hours, 48 completed wakes, 1 interrupted wake, 49 dry-run actions, 1 restart |
| Soak event export | 2,168 metadata-only records |

The restart closes and reopens SQLite after a guarded dry-run post but before
wake completion. Recovery marks that wake interrupted and advances its schedule;
the next fresh wake does not replay the interrupted action. The action totals
include the one action before the crash. Unknown unfinished audit outcomes are
retired with retry disabled, as covered separately by store recovery tests.

Saved evidence is canonical in [testdata/runtime](testdata/runtime):
[wakes.json](testdata/runtime/wakes.json), [eval.json](testdata/runtime/eval.json),
[smoke.jsonl](testdata/runtime/smoke.jsonl) and
[events-excerpt.jsonl](testdata/runtime/events-excerpt.jsonl). The excerpt contains
98 actual records covering the first wake, interrupted wake, recovery and first
fresh wake; reproduce the command for the complete event stream. Eval timing is
wall-clock overhead, while wake timestamps and the 48-hour interval are logical.
No elapsed live-run duration is claimed.

## Named checks

- `TestWakeReadsWritesDryRunAndSchedules`: context, read/note/post/schedule and self-note in the next actual wake.
- `TestWakeStopReasonsAndScheduleDefaults`: both clamps, default, step limit and canonical stall stop.
- `TestSchedulerRestartDoesNotReplayAndHonorsOwnerControls` and `TestInterruptedWakeRecoveryAdvancesScheduleAndNeverReplays`: restart, future scheduling, no replay and paused/killed state.
- `TestEveryExternalToolResultIsUntrustedAndLogsAreRedacted` and `TestExternalWrappersAndDryRunActions`: every external tool envelope and payload-free logs/audits.
- `TestRuntimeSuiteUsesGuardedToolsAndPersistentWakes`: the versioned runtime eval suite through both real adapters and guarded tools.
- `TestOfflineSoak48LogicalHoursWithRestart`: repeated wakes and restart against actual temporary SQLite.
- `TestSavedLogicalSoakEvidenceAndEventRedaction`: evidence labels, counts, unique closed wakes and metadata field allowlist.
- `TestRuntimeCLIOnceOfflineWakePersistsCompletedAudit` and the independent `TestRuntimeCLI*` acceptance tests: persisted smoke, credential-free custom profiles, lifecycle/logging, cancellation, bad inputs and live-backend refusal.
- `TestTelegramOptionalAndExplicitlyEnabled`: default-off credential resolution; enabled fixtures retain required-secret validation.

This measures local harness behavior only. Real browser/account qualification is
#2, elapsed single-agent/provider/bot qualification is #18, and real two-agent
memory observation is #21. Their live criteria remain unmeasured and open.
