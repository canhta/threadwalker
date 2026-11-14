# Evals

The eval harness replays versioned synthetic suites through the real client, protocol adapters, tool
loop and accounting. Scripted fake HTTP responses and fixture-local tools mean an offline run makes
no provider request and reads no API key. It evaluates the harness, not a model's personality or
popularity.

## Case kinds

Every case carries a `kind` that selects its runner: `chat` (scripted client loop), `quality`,
`guard`, `replay`, `runtime`, `owner`, `browser`, `metrics`, `dashboard` or `isolation`. The kinds
`owner`, `browser`, `metrics`, `dashboard` and `isolation` take a `scenario`; `quality`,
`guard`, `replay`, `runtime` and `briefs` carry their own payload object. Only `chat`, `quality` and `briefs` run live; `briefs` runs only live. The metrics
suite is embedded in the binary, so `threadz eval metrics` runs from any directory.

## Reproduce

The canonical suite is [`internal/eval/testdata/default.json`](../internal/eval/testdata/default.json).
Offline is the default and uses synthetic fixture profiles unless you select a catalog. No
credentials are needed:

```sh
go run ./cmd/threadz eval --suite internal/eval/testdata/default.json
go run ./cmd/threadz eval --suite internal/eval/testdata/default.json --out /tmp/eval-report.json
```

Defaults: seed `1`, one repeat, and per-case limits of 16 steps, 1024 output tokens per request and
USD 1 estimated cost; an explicit case or global limit uses the smaller positive value. To compare
configured profiles offline, pass `--llms <catalog>` and `--profiles <comma-separated IDs>` together;
only dummy credentials are resolved.

## Model catalog

The catalog is an eval input only; an agent's models are the inline `llm`, `writer.llm` and
`critic.llm` blocks of its template. Provider and profile IDs are arbitrary. Save this as `llms.yaml`
and replace every model ID and price:

```yaml
providers:
  primary:
    protocol: openai              # openai also covers compatible custom endpoints
    base_url: https://api.openai.com/v1
    api_key_env: LLM_API_KEY
  secondary:
    protocol: anthropic           # native Messages, also accepts a base ending /v1
    base_url: https://api.anthropic.com
    api_key_env: SECONDARY_LLM_API_KEY
models:
  fast:
    provider: primary
    model: your-fast-model-id
    price_per_mtok_in_usd: 0
    price_per_mtok_out_usd: 0
  reasoning:
    provider: primary
    model: your-reasoning-model-id
    price_per_mtok_in_usd: 0
    price_per_mtok_out_usd: 0
    reasoning_effort: none         # optional; openai protocol only, omitted when unset
  backup:
    provider: secondary
    model: claude-haiku-5-5        # the only Anthropic model allowed
    price_per_mtok_in_usd: 0
    price_per_mtok_out_usd: 0
```

Reproduce one failing case with `--repeat`, `--seed`, `--cases` and `--revision`:

```sh
go run ./cmd/threadz eval --suite internal/eval/testdata/default.json \
  --cases <case-id> --seed 1 --repeat 1 --out /tmp/eval-failing.json
```

The command exits `0` when every expectation passes and no comparison regresses; failed cases,
regressions, malformed suites, invalid flags or caps and unwritable report paths exit nonzero. The
summary goes to stdout and `--out` atomically writes the JSON report, which indexes failed cases by
suite digest, case/profile/repeat, seed and safe outcome. Reproduce from that fixture and the
report's seed, repeat and caps; do not copy raw prompts or outputs into failure artifacts.

## Compare

Save a baseline from one suite, then run the same suite/profile inputs again with `--compare`:

```sh
go run ./cmd/threadz eval --suite internal/eval/testdata/default.json --out /tmp/eval-baseline.json
go run ./cmd/threadz eval --suite internal/eval/testdata/default.json \
  --compare /tmp/eval-baseline.json --out /tmp/eval-current.json
```

A comparison needs the same suite version and digest and the same `inputs_fingerprint` (selected
cases, seed, repeat, mode and effective caps). Revision, wall-clock timing, model/profile names and
prices may differ. One selected profile per run lets different models be compared; runs with several
profiles need identical profile IDs, or the comparison fails as ambiguous. Correctness, safety and
rubric regressions are separate from token, estimated-cost and wall-clock deltas, and timing never
gates a comparison.

The comparison fixture gives one fake provider an incorrect answer and higher token usage. With
unchanged inputs and no credentials, the second command exits nonzero, names
`seeded_provider_regression`/`content_mismatch` and reports token and cost deltas separately:

```sh
go run ./cmd/threadz eval --suite internal/eval/testdata/comparison.json \
  --llms internal/eval/testdata/comparison-catalog.yaml --profiles baseline --out /tmp/eval-good.json
go run ./cmd/threadz eval --suite internal/eval/testdata/comparison.json \
  --llms internal/eval/testdata/comparison-catalog.yaml --profiles regressed \
  --compare /tmp/eval-good.json --out /tmp/eval-regressed.json
```

Inspect failed cases in the report:

```sh
jq -r '.results[] | select(.passed | not) | [.case_id,.profile_id,.repeat,.reason,.end_reason] | @tsv' /tmp/eval-report.json
```

## Report

| Field | Meaning |
|---|---|
| `suite_digest` | Fixture content digest |
| `mode` | `offline` or `live` |
| `revision` | Build revision when embedded, or explicit `--revision`; otherwise `unknown` |
| `seed`, `repeat` | Determinism inputs; result `repeat` is one-based |
| `config_fingerprint` | Safe provider/model/profile/price configuration; excludes credentials |
| `inputs_fingerprint` | Selected cases, seed, repeat, mode and effective caps; excludes models and prices |
| `timing_kind` | Timing label; offline uses wall-clock and excludes it from deterministic comparisons |
| `results` | One row per case/profile/repeat |

Each result carries `case_id`, `profile_id`, `provider_id`, `model`, `protocol`, `repeat`,
`passed`, a stable safe `reason` code, `end_reason`, `steps`, ordered executed known `tool_calls`,
`tokens_in`, `tokens_out`, `cost_usd` and `duration_ms`.

`reason` and `end_reason` are codes, never raw errors, prompts, message text, response bodies or tool
output; reports hold no final content, planted secrets, stranger usernames or credentials. `cost_usd`
uses configured prices (synthetic usage offline, provider-reported usage live), never provider
billing. `duration_ms` is wall-clock, reported separately and never a pass/fail threshold; offline it
measures harness overhead, not model latency. A modified build revision carries a `+dirty` suffix;
pass the tested commit with `--revision "$(git rev-parse HEAD)"` when build metadata is missing.

Quality checks are deterministic: exact final content, ordered tool names, required and forbidden
substrings, and max runes. They are not style judgments and say nothing about engagement.

## Optional live comparison

Live never runs in the default checks or CI. It needs real credentials in the environment named by
the catalog, explicit profiles **and** cases, a positive finite `--max-usd`, and positive step and
output caps. Replace the example catalog's placeholder model IDs and prices first.

```sh
go run ./cmd/threadz eval --live \
  --suite internal/eval/testdata/default.json \
  --llms llms.yaml --profiles fast,backup \
  --cases valid_two_tool_loop,deterministic_quality_rubric \
  --max-usd 0.05 --max-steps 8 --max-output-tokens 512 \
  --out /tmp/eval-live.json
```

Before each request, live admission reserves an input estimate (serialized request bytes plus 1024
tokens) and the bounded output, at configured rates. The cap is shared across cases, profiles and
repeats, and retries are checked too. A response whose reported usage exceeds its reservation is kept
as actual accounting, stops further requests and fails the cap; the report is still written and the
command exits nonzero. Reservations stay charged when billing is unknown (a transport error or
missing usage). The cost is an estimate, never an invoice.

## Local runtime cases

```sh
go run ./cmd/threadz eval --suite internal/eval/testdata/runtime.json --seed 1 --repeat 2 --out /tmp/runtime-eval.json
```

These offline cases run the production wake lifecycle, guarded dispatcher and temporary SQLite for
both protocols: note carryover, dry-run action, default, minimum and maximum scheduling, step and
stall stops, interrupted-no-replay and every external-result envelope. The `runtime` case contract
in `internal/eval/types.go` declares synthetic calls and persistent-state expectations; there is no
live mode. The logical soak command in [setup](setup.md) exports the same report with wake and event
evidence.

## Guard policy cases

```sh
go run ./cmd/threadz eval --suite internal/eval/testdata/guard.json \
  --seed 1 --repeat 2 --out /tmp/guard-eval.json
go test ./internal/guard ./internal/tools ./internal/eval -run 'Policy|Contact|Guard|Stop'
```

The `guard` contract drives the production dispatcher, guard and temporary SQLite directly. Its
`steps` declare nanosecond offsets, tool calls, owner status and contact changes, and addressed stop
observations with the fixed refusal codes each must return. No provider, browser or Telegram request
is made.

It covers daily and contact caps, UTC and rolling-window expiry, spacing, the 500 and 501 code
points, forbidden case variants, URL and mention rules, thread/day identity, permanent contacts,
configured and default stop requests, paused and killed tools, and mandatory construction. See also
`TestDailyActionLimitStopsWakeBeforeFurtherTools`, `TestPolicyReservesQuotaBeforeEffect` and
`TestConcurrentGuardsCannotSpendLastSlotTwice`.

`TestCapturedGuardPolicyEvents` reproduces the metadata-only trace
[`guard-events.jsonl`](../internal/eval/testdata/guard-events.jsonl) (minus the logger timestamp).
Rule IDs are in `reason` and in `blocked_by` on audit rows; the trace holds no usernames, text or
URLs. After reviewing an event-contract change, regenerate it with
`THREADZ_UPDATE_GUARD_TRACE=1 go test ./internal/eval -run TestCapturedGuardPolicyEvents`.

## Replay cases

A `replay` case replays one wake's tool calls through the production dispatcher, guard and
temporary SQLite. Its `replay.steps` list the calls in order.

| Step field | Meaning |
|---|---|
| `output` (read step) | What the real read returned, as the `untrusted` envelope the model saw. The fake backend serves it when that call is dispatched, so the guard issues the same short refs |
| `expected_blocked` (write step) | The guard reason the call must stop with; empty when it must pass |
| `expected_detail` | A substring the call's stored detail must contain |
| `writer`, `critic` | Scripted model outputs for the reviewing step, both or neither. Without them the agent's own text is published |
| `fail_write`, `expected_stage` | With `live`, the fake backend fails the write with a browser reason at a stage, and the stage, saved snapshot and failed intent are asserted |

The kind is offline only
and makes no provider, browser or Telegram request.

[`default.json`](../internal/eval/testdata/default.json) holds the regression cases of failures seen in
live wakes, each with data shaped like the real outputs:

| Case | Failure it keeps from returning |
|---|---|
| `replay_mangled_trend_ref` | A model copies a long Google News `trend_ref` with a few characters wrong: the near copy resolves, a far one is refused with the nearest refs in the stored detail, the short ref works |
| `replay_rough_draft_with_writer` | A rough draft over 500 characters is refused as `text_length` before the writer runs; the final text must be checked, and a writer text that is too long is rejected by the editor naming the length |
| `replay_reply_composer_missing` | A browser reply whose page shows no composer ends as `write_refused` at stage `composer_missing`, the intent is marked failed and the snapshot is saved |

The topic-tagged post whose reply composer was not found is a parser failure below the backend, so the
dispatcher cannot replay it: `TestPostPageWithTopicTagHasComposer` and
`TestAgentBrowserReplyOnTopicTaggedPost` in `internal/threads` drive the sanitized
`threads-live/post-tagged.ax.txt` through the real page parser and reply driver.

### From a live failure to a regression case

```sh
threadz ctl wake-diagnose 412 --agent viral          # find the call that went wrong
threadz ctl wake-export 412 --agent viral > case.json # the wake as a replay suite
```

The export keeps the reads, the agent's calls and the guard's verdicts, so it passes against the
build that ran the wake. To turn it into a regression test, edit `expected_blocked` to the outcome
that should hold, fix the code, then add the case to a suite.

It works within the 8 days transcripts are kept. It fails when a tool result was cut (a wake stored
before the 64 KiB cap) or the transcript hit its bounds.

Review an export before committing it. The text of other people's posts is blanked unless
`--keep-text` is set, but usernames, post URLs and headlines stay, and a `writer` or `critic`
script is never exported. Each write step carries a `brief` (below), which replay ignores.

State carried over from earlier wakes (quotas, recent posts for `repeat_topic`) is not part of a replay.

## Replaying briefs through the current writer and critic (live)

Before a prompt or rubric deploy, see what the current writer and critic would make of real briefs. A
`briefs` case holds writes recorded from live wakes: the angle, fact, hook and rough text, the post or
trend answered, the recent own texts the writer was given, the most-liked post that wake read, and
the text the wake posted. The run feeds each through the same `quality.Revise` path as the guard,
with the template's own writer and critic, and prints a plain text report per brief: brief, parent,
top-liked post, each draft, each verdict, and the result. **It calls the live model and costs money; it
never runs offline** (an offline run of a suite with a `briefs` case fails, and the default suite has
none). Nothing is published.

The guard records the brief and every judged draft in the call's detail (no schema change), redacted
and bounded in runes: angle and fact 600 each, rough text and draft parts 600 each, the parent 800, 20
recent texts of 300, 8 problems of 300, so one call stays under about 30 KiB. Detail lives as long as
transcripts (8 days). Wakes older than this change have no brief.

On the host, against the host database (reads only; no model call):

```sh
deploy/live-briefs.sh viral 10 live-briefs.json     # the last 10 wakes that used the writer; KEEP_TEXT=1 keeps other people's text
```

It prints the `--cases` list to use. It runs `threadz ctl wake-export <id> --agent viral --briefs` for each recent wake and joins the
cases with `jq`. Without `--keep-text` the parent and top-liked text are blank, so keep it for a useful
report and do not commit the file. Then, with the model catalog and keys of [Optional live comparison](#optional-live-comparison):

```sh
go run ./cmd/threadz eval --live --suite live-briefs.json \
  --llms llms.yaml --profiles fast --cases briefs_wake_412,briefs_wake_409 \
  --max-usd 1 --max-steps 8 --max-output-tokens 1024
```

The gate wants a profile and explicit cases, but the models are the template's `writer` and `critic`
(the profile is not called). `--max-usd` stops the run before the next brief once the scratch
ledger has spent it. Each brief is one writer call and one critic call, up to three rounds when the
critic refuses. At the template's rates that is about 2 to 6 calls per brief; expect roughly USD 0.01 to 0.03 per brief at the viral template's rates (an estimate, not an invoice)
and check the `spend:` line. The replay skips the guard's own content rules, which need the live database.

## Owner service cases

```sh
go run ./cmd/threadz eval --suite internal/eval/testdata/owner.json --seed 7 --repeat 2 --out /tmp/owner-eval.json
```

These offline scenarios use the real Bot API client against `httptest`, persisted owner commands,
guarded `ask_owner`/`notify_owner`, wake context and an injected clock. They cover owner-only
controls, pause, resume and killed state, permanent do-not-contact, questions answered in the next
wake, per-event notices, the daily report and failed delivery without replay. They cannot run live.

## Browser component cases

```sh
go run ./cmd/threadz eval --suite internal/eval/testdata/browser.json --seed 8 --repeat 2 --out /tmp/browser-eval.json
go test ./internal/agent ./internal/tools ./internal/eval -run Browser
```

The `browser` cases run the guarded dispatcher, SQLite intent boundary and wake runtime against a
scripted browser. They cover six operations, dry-run suppression, untrusted content and secret
redaction, logout and captcha pause with durable notices, unknown pages, uncertain-submit
termination, restart without replay, and addressed stop-request ingestion. Saved pages are in
[`browser-pages.json`](../internal/eval/testdata/browser-pages.json). Live mode rejects them.

The tagged Chromium tests run the real CDP extraction, six guarded operations, local HTTP
submissions and process cleanup against HTML fixtures; default evals need no Chrome binary. Neither
suite qualifies the current Threads DOM, account behavior or production memory.
`TestCapturedBrowserEvents` reproduces the event trace; regenerate it after an intentional change
with `THREADZ_UPDATE_BROWSER_TRACE=1 go test ./internal/eval -run TestCapturedBrowserEvents`.

## Metric polling and logical-time simulation

```sh
go run ./cmd/threadz eval --suite internal/eval/testdata/metrics.json --seed 19 --out /tmp/metrics-eval.json
go run ./cmd/threadz eval metrics --soak-hours 72 --evidence /tmp/metrics-evidence-new
go test ./internal/eval ./internal/metrics ./internal/store ./internal/tools ./internal/agent -run 'Metric|Pipeline|MultiDaySimulation'
go test -race ./internal/metrics ./internal/store ./internal/tools ./internal/agent
```

Use a fresh evidence directory. The offline `metrics` command reads no environment keys, creates and
deletes its own temporary DB, and writes `simulation.json`, `events.jsonl` and the shared `eval.json`.
Configured `run` polls metrics through the serial scheduler; this command only reproduces synthetic
evidence.

The `metrics` cases run the real processor, guarded dispatcher and SQLite jobs for all three windows,
restart and catch-up, agent and action isolation, cancellation, missing data, and transactional DB
failure and retry, plus the 72-hour simulation. No model is called, so usage and cost are zero.
Reports separate logical hours and stored sample/target counts from wall-clock duration. Live mode
rejects them.

At 72 logical hours the simulation has seven confirmed synthetic actions, 21 completed targets and
19 stored samples. An initially overdue action coalesces three targets into one actual observation.

One guarded read before a simulated crash is repeated safely after a file DB close and reopen with
an expired lease. That run audits 37 source reads: 19 action reads, the crash read, and 17 account
follower reads, one per batch that claimed targets. It takes one restart, with no duplicate
checkpoint or sample and no change to agent scheduling.

One `metrics.measured` event corresponds to one sample, and `metrics.target.completed` records each
target with its own lateness. This establishes local functional behavior, not elapsed runtime or
live Threads metrics support.

## Read-only dashboard

```sh
go run ./cmd/threadz eval --suite internal/eval/testdata/dashboard.json --out /tmp/dashboard-eval.json
go test ./internal/dashboard ./internal/store ./cmd/threadz -run 'Dashboard' -count=1
```

`dashboard` cases run the real read-only SQLite projection and HTTP handler with synthetic sensitive
strings. They check privacy across routes and methods, absence of writes and safe query failure, with
no model call. Live mode refuses them.

## Two-agent isolation

```sh
go run ./cmd/threadz eval --suite internal/eval/testdata/isolation.json --seed 12 --out /tmp/isolation-eval.json
go test ./internal/eval -run 'TestCapturedIsolation|TestIsolationScenarios|TestAgentScopedReport' -count=1
go test -race ./internal/config ./internal/guard ./internal/agent ./internal/eval ./cmd/threadz -run 'IsolationAcceptance|Promotion|OwnAccount|ConfirmedOwn|IsolationScenarios|CapturedIsolation|AgentScopedReport' -count=1
```

`isolation` cases exercise the guarded data tools, the own-action, contact and ledger
boundaries, independent owner and budget state, own-account and duplicate-link refusals, and a
48-logical-hour two-agent run with metric maintenance and a SQLite restart.

Each report row's `agents` field holds separate per-agent counts with local usage and cost
estimates. The soak uses a fixed synthetic OpenAI transport, so no provider is charged.

The redacted trace is `internal/eval/testdata/isolation-events.jsonl`, reproduced by
`TestCapturedIsolationEventsAreRedactedAndScoped` and regenerated with
`THREADZ_UPDATE_ISOLATION_TRACE=1`. Live mode refuses these cases before reading credentials.

## Draft quality

```sh
go run ./cmd/threadz eval --suite internal/eval/testdata/quality.json --out /tmp/quality-eval.json
go test ./internal/eval ./internal/quality -run 'Quality' -count=1
```

Each `quality` case seeds a store with the case's earlier actions of the day, builds the wake context
the running agent would receive from the real template (`templates/viral.agent.yaml`, the prompt that
`internal/agent/testdata/viral.system.golden` pins), asks the model for one draft, and scores it with
[`internal/quality`](../internal/quality). The scorer and its fixed rubric
(`internal/quality/rubric.json`) have no dependency on the eval harness, so other tooling can score drafts the same way.

| Check id | Meaning |
|---|---|
| `max_runes` | Draft within the rubric's limit for its kind (reply 120, post 300) |
| `max_lines` | Post within the rubric's line limit |
| `style_avoid` | No `style_avoid` entry of the template, case-folded |
| `repeat_image` | No rubric image word that today's own actions already used |
| `ungrounded_number` | Every number next to a rubric self-stat unit equals, at the precision written, a number on the wake context |
| `style_rule_<n>` | The judge did not flag the template's n-th `style_rules` entry |
| `bad_example` | The judge did not find the draft to share a flaw with a `bad_examples` entry |

The judge is a second model call whose prompt is the rubric's instructions plus the template's own
rules and rejected examples; it answers `{"violated_rules":[...],"like_bad_example":bool}`. The summary
prints `Quality <check>: passed/total` per check; each result's `quality` array holds the check
outcomes and no draft text.

Offline, the draft and the judge's verdict are recorded in the suite and replayed through the real
client. A case passes when the checks that fail are exactly its `fails` list, so seeded bad drafts
prove each check fires. The recorded drafts are synthetic: the pass rates describe the scorer, not
a model.

Live, the recorded outputs are ignored and a case passes once scored; the pass rates are the result.
It uses the same gating as every live run (explicit profiles and cases, environment credentials,
positive caps) and never runs in CI:

```sh
go run ./cmd/threadz eval --live --suite internal/eval/testdata/quality.json \
  --llms llms.yaml --profiles <profile> --cases clean_post,clean_reply \
  --max-usd 0.05 --max-steps 4 --max-output-tokens 512 --out /tmp/quality-live.json
```

The suite digest covers the template files and the rubric, so a persona or rubric edit makes
`--compare` refuse an older report.

## Coverage limits

The suites cover the client and protocol, configuration, accounting, the local runtime, owner-service
and browser boundaries with synthetic data, and metric polling. Real-bot behavior, live browser and Threads compatibility, and the two-agent
48-hour run on a real account are separate live measurements.
