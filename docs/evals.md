# Evals

The eval harness replays versioned synthetic suites through the real client, protocol adapters,
tool loop and accounting. Scripted fake HTTP responses and fixture-local tools mean an offline run
makes no provider request and reads no API key. It evaluates the **harness**, not a model's
personality or popularity: a model scoring its own output is never evidence of engagement.

## Reproduce

The canonical suite is [`internal/eval/testdata/default.json`](../internal/eval/testdata/default.json).
Offline is the default mode, and with no catalog/profile selection it uses synthetic fixture
profiles. These commands do not need credentials:

```sh
go run ./cmd/threadwalker eval -suite internal/eval/testdata/default.json
go run ./cmd/threadwalker eval -suite internal/eval/testdata/default.json -out /tmp/eval-report.json
```

Defaults are seed `1`, one repeat, and per-case limits of 16 steps, 1024 output tokens per
request and USD 1 in configured estimated cost. Explicit case/global limits use the smaller
positive value. To compare configured profiles offline, pass `-llms <catalog>` and
`-profiles <comma-separated IDs>` together; only dummy credentials are resolved.

Use `-repeat`, `-seed`, `-cases` and `-revision` to reproduce one run. Selecting a single failing
case keeps the fixture honest without changing the suite:

```sh
go run ./cmd/threadwalker eval -suite internal/eval/testdata/default.json \
  -cases <case-id> -seed 1 -repeat 1 -out /tmp/eval-failing.json
```

The command exits `0` when every semantic expectation passes and any comparison shows no
regression. Failed cases, regressions, malformed suites, invalid flags/caps and unwritable report
paths exit nonzero. The human summary goes to stdout; `-out` atomically writes the JSON report.
The saved report indexes failed synthetic cases by suite digest, case/profile/repeat, seed and
safe outcome; keep it with the versioned fixture checkout. Reproduce with that fixture and the
report's seed/repeat/caps instead of copying raw prompts or outputs into failure artifacts.

## Compare

Save a baseline from one suite, then run the same suite/profile inputs again with `-compare`:

```sh
go run ./cmd/threadwalker eval -suite internal/eval/testdata/default.json -out /tmp/eval-baseline.json
go run ./cmd/threadwalker eval -suite internal/eval/testdata/default.json \
  -compare /tmp/eval-baseline.json -out /tmp/eval-current.json
```

A comparison requires the same suite version and digest and the same evaluation inputs
(`inputs_fingerprint`: selected cases, seed, repeat, mode and effective global/per-case caps).
Revision, wall-clock timing, model/profile names and configured prices may differ. With one
selected profile per run, different profiles/models can be compared; runs with multiple profiles
require identical profile IDs, otherwise the comparison fails as ambiguous. Correctness/safety/
deterministic-rubric regressions are separate from measured token, estimated-cost and wall-clock
deltas. Timing never gates a comparison.

The canonical comparison fixture deliberately gives one fake provider an incorrect answer and
higher token usage. These commands use unchanged inputs and no credentials; the second exits
nonzero, names `seeded_provider_regression`/`content_mismatch`, and separately reports token/cost
deltas:

```sh
go run ./cmd/threadwalker eval -suite internal/eval/testdata/comparison.json \
  -llms internal/eval/testdata/comparison-catalog.yaml -profiles baseline -out /tmp/eval-good.json
go run ./cmd/threadwalker eval -suite internal/eval/testdata/comparison.json \
  -llms internal/eval/testdata/comparison-catalog.yaml -profiles regressed \
  -compare /tmp/eval-good.json -out /tmp/eval-regressed.json
```

Inspect failed cases in the report:

```sh
jq -r '.results[] | select(.passed | not) | [.case_id,.profile_id,.repeat,.reason,.end_reason] | @tsv' /tmp/eval-report.json
```

## Report

| Field | Meaning |
|---|---|
| `report_version` | Report schema; currently `v1` |
| `suite_version`, `suite_digest` | Fixture contract version and content digest |
| `mode` | `offline` or `live` |
| `revision` | Build revision when embedded, or explicit `-revision`; otherwise `unknown` |
| `seed`, `repeat` | Determinism inputs; result `repeat` is one-based |
| `config_fingerprint` | Safe provider/model/profile/price configuration; excludes credentials |
| `inputs_fingerprint` | Selected cases, seed, repeat, mode and effective caps; excludes models and prices |
| `timing_kind` | Timing label; offline uses wall-clock and excludes it from deterministic comparisons |
| `results` | One row per case/profile/repeat |

Each result carries `case_id`, `profile_id`, `provider_id`, `model`, `protocol`, `repeat`,
`passed`, a stable safe `reason` code, `end_reason`, `steps`, ordered executed known `tool_calls`,
`tokens_in`, `tokens_out`, `cost_usd` and `duration_ms`.

`reason` and `end_reason` are codes, never raw errors, prompts, message text, response bodies or
tool output. Reports contain no final content, planted secrets, stranger usernames or credentials.
`cost_usd` uses configured prices: synthetic usage offline and provider-reported usage live,
never provider billing. `duration_ms` is wall-clock and is reported separately; it is not a
deterministic pass/fail threshold. Offline timing measures harness/fake-transport overhead,
not real model latency. Build revisions marked modified carry a `+dirty` suffix; supply the
tested commit with `-revision "$(git rev-parse HEAD)"` when build metadata is unavailable.

Quality checks are explicit and deterministic: exact final content, ordered tool names,
required/forbidden substrings and max runes. They are not human-reviewed style judgments and say
nothing about engagement.

## Optional live comparison

Live is opt-in and is never part of the default checks or CI. It requires real credentials in the
environment named by the selected catalog, explicit selected profiles **and** cases, a positive
finite `-max-usd`, and positive step and output caps:

Configure actual model IDs and positive prices first; the example catalog contains placeholders.

```sh
go run ./cmd/threadwalker eval -live \
  -suite internal/eval/testdata/default.json \
  -llms llms.example.yaml -profiles fast,backup \
  -cases valid_two_tool_loop,deterministic_quality_rubric \
  -max-usd 0.05 -max-steps 8 -max-output-tokens 512 \
  -out /tmp/eval-live.json
```

Before each request, live admission reserves a conservative input estimate of serialized request
bytes plus 1024 tokens, and the bounded output, at configured rates. The cap is shared across cases,
profiles and repeats, and retries are checked too. A response whose reported usage exceeds its
reservation is preserved as actual accounting, stops further requests and fails the cap; the JSON
report is still written with that usage and the command exits nonzero. This is a configured
estimate, not a guarantee of provider billing; a server can report an overshoot, which is retained
truthfully. Reservations remain charged when billing status is unknown, such as a transport error
or missing usage. Never describe synthetic or estimated cost as an invoice.

## Local runtime cases

```sh
go run ./cmd/threadwalker eval -suite internal/eval/testdata/runtime-v1.json -seed 1 -repeat 2 -out /tmp/runtime-eval.json
```

These offline cases run the production wake lifecycle, guarded dispatcher and temporary SQLite
for both protocols: note carryover, dry-run action, default/minimum/maximum scheduling, step/stall
stops, interrupted-no-replay and every external-result envelope. The optional `runtime` case
contract in `internal/eval/types.go` declares synthetic calls and persistent-state expectations;
it has no live mode. Reports retain the shared identities, normalized usage, safe reasons and
separately labelled wall-clock timing. The logical soak command in [setup](setup.md) exports the
same report alongside wake and event evidence.

## Pending coverage

The current suites cover the client, protocols, configuration, accounting and local runtime boundaries.
They do not certify components that do not exist yet. Full guard policy and do-not-contact boundaries belong to
[#6](https://github.com/canhta/threadwalker/issues/6), Telegram/kill-switch cases to
[#7](https://github.com/canhta/threadwalker/issues/7), browser/Threads cases to
[#8](https://github.com/canhta/threadwalker/issues/8), and integrated live validation to
[#9](https://github.com/canhta/threadwalker/issues/9). Playbook/gate cases belong to
[#10](https://github.com/canhta/threadwalker/issues/10), and multiple-agent/template cases to
[#12](https://github.com/canhta/threadwalker/issues/12) as those components land. Until then,
their coverage is pending, not passing.
