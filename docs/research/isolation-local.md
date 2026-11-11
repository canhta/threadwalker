# Local two-agent isolation evidence

Measured from source `2766bfbd268a333f35cf3a05159d0483417850f6` on Linux x86_64,
using the real Go scheduler, guarded tools, SQLite and in-process synthetic LLM/Threads
backends. No environment credentials, external services, real accounts or provider charges
were used. This advances **48 logical hours**, not 48 elapsed hours. Real-account and
Chromium memory qualification remains open in #21.

## Reproduce

From the repository root, with the Go version in `go.mod` and a fresh evidence directory:

```sh
go build -o /tmp/threadwalker ./cmd/threadwalker
/tmp/threadwalker run -offline -soak-hours 48 -soak-agents 2 -evidence /tmp/threadwalker-isolation
/tmp/threadwalker eval -suite internal/eval/testdata/default.json -seed 12 -out /tmp/default-eval.json
/tmp/threadwalker eval -suite internal/eval/testdata/isolation-v1.json -seed 12 -repeat 2 -out /tmp/isolation-eval.json
```

`isolation.json` contains the source revision, scoped wake/accounting/state rows and memory
samples. `events.jsonl` is the actual safe operational trace; `eval.json` contains eight focused
companion results, all passing. The separate full isolation eval produced **20/20 passing**
results (five cases, two synthetic profile labels, two repeats); the default client suite
produced **30/30 passing** results. Both profile labels execute the same component fixtures;
the logical soak uses its fixed synthetic OpenAI transport, not a paid model comparison.
Controls/data cases seed prior synthetic ledger usage to exercise budget exhaustion; their
reported accounting includes that fixture usage and must not be interpreted as provider spend.

## Observed state and scheduling

The two synthetic templates use distinct own usernames, personas/seeds and profile paths.
Agent A pauses at logical hour 6, resumes at hour 10, and continues after B is killed at hour
36. B has a tiny independent daily budget, exhausted on each UTC day; A keeps running. A's
independent action quota stops only A's wake. An interrupted A wake crosses a real file DB
close/reopen at hour 24 and recovers without replay. Two explicitly synthetic confirmed seed
publications drive actual guarded metric maintenance; they are fixture rows, not live posts.

The run recorded **86 wake rows**, **96 hourly state rows**, **one interrupted wake**, **six
metric maintenance wakes**, and a maximum of **one active synthetic backend**. Paused/killed
intervals contain no wake for the stopped agent. Own context markers never crossed agents.
Completed-wake counts below include metric maintenance scopes.

| Agent | Completed wakes | Budget stops | Action-limit stops | Paused/killed hourly skips | Dry-run actions | Tokens in/out | Synthetic estimated USD |
|---|---:|---:|---:|---:|---:|---:|---:|
| synthetic-isolation-a | 15 | 0 | 31 | 4 / 0 | 11 | 550 / 275 | 0.00110 |
| synthetic-isolation-b | 7 | 32 | 0 | 0 / 12 | 3 | 80 / 40 | 0.00016 |

## Local memory observations

These are `runtime.ReadMemStats` samples of the **local Go process with synthetic backends**,
without a forced GC, from the run above. They are not RSS, Chromium measurements, a leak
proof, deployment capacity evidence, or elapsed soak qualification. Allocation/GC scheduling
and goroutine counts vary between runs; no acceptance threshold is inferred from them.

| Logical hour | Go allocated bytes | Go heap in-use bytes | Go system bytes | Goroutines |
|---:|---:|---:|---:|---:|
| 0 | 1,006,128 | 1,982,464 | 12,540,168 | 4 |
| 12 | 1,699,440 | 3,366,912 | 18,749,704 | 8 |
| 24 | 1,808,392 | 3,391,488 | 19,282,184 | 5 |
| 36 | 1,940,960 | 3,284,992 | 19,290,376 | 4 |
| 48 | 1,875,312 | 3,375,104 | 19,290,376 | 6 |

## Regression and event evidence

The independent `TestIsolationAcceptance*` suites in `internal/config`, `internal/guard` and
`internal/agent` cover complete-set validation, profile aliases, no identity inference,
notes/playbook/contacts/ledger invisibility, pause/kill/budget independence, selected Telegram
commands, serial wake/metric lifetimes and scoped redacted logs. `TestTwoAgentLogical48HourSoak`
and `TestTwoAgentOfflineSoakCLIExportsScopedEvidence` exercise the complete logical run and CLI.
`TestConfirmedOwnPublicationCannotBeDisguisedAsForeign` checks opaque publication URL aliases
and outgoing own links; promotion tests cover canonical URL equivalence, meaningful query/path
differences, bare domains and concurrent reservation.

Versioned cases live in `internal/eval/testdata/isolation-v1.json`; the captured metadata-only
trace is `isolation-v1-events.jsonl` beside it. `TestCapturedIsolationEventsAreRedactedAndScoped`
regenerates and compares it, requiring both agent correlations plus `own_account`,
`shared_promotion_link` and `budget` reasons. It rejects credentials, private state, account or
stranger usernames, URLs and action text in the trace. Shared report validation preserves
separate agent counts/accounting while rejecting duplicate/invalid IDs and numeric values.

```sh
go vet ./... && go test ./...
go test -race ./internal/config ./internal/guard ./internal/agent ./internal/eval ./cmd/threadwalker -run 'Isolation|Promotion|OwnAccount|ConfirmedOwn|AgentScopedReport|TwoAgent' -count=1
```

The full vet/test command passed on the source above. Focused race checks passed across
config, guard, agent, eval and CLI isolation cases; a separate race invocation passed both
`TwoAgent` soak/CLI tests. The source-scoped soak trace contains 2,288 redacted events and
its eight focused companion eval results all pass.

The URL policy and its limits are specified once in `docs/spec.md`: these are local syntactic
keys, not redirect discovery or proof that two real account sessions are correctly bound.
