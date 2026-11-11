# Chat Completions tool loop and accounting

Measured on 2026-11-11 for issue #4. The owner authorized this small paid check;
no VPS, Threads, Telegram, or Anthropic requests were made.

## Provider and protocol

The live run used OpenAI's `https://api.openai.com/v1` endpoint and
`gpt-5.6-luna`. The model is supplied through `config.LLMConfig`; the client
contains no provider or model default. Official documentation checked on the
measurement date lists Chat Completions and function calling support, with
standard text rates of USD 0.20 per million input tokens and USD 1.20 per million
output tokens. See the [model documentation](https://developers.openai.com/api/docs/models/gpt-5.6-luna)
and [pricing](https://developers.openai.com/api/docs/pricing).

The [function calling guide](https://developers.openai.com/api/docs/guides/function-calling)
describes function arguments as JSON encoded inside a string, and tool responses
as messages with `role: tool` and the corresponding `tool_call_id`. Both worked
in this run. The schemas used `strict: true`, required string arguments, and
`additionalProperties: false`. Optional request settings were
`reasoning_effort: none` and `max_completion_tokens: 256`.

## Observed live run

`TestLiveTwoToolLoop` in `internal/llm/live_test.go` passed in 6.38 seconds at
2026-11-11T11:38:40Z. It used two fake local tools, with no external side effects:

1. `lookup_seed` received JSON arguments `{"label":"demo"}` and returned a fixed seed.
2. After receiving that result, `append_marker` received JSON arguments containing
   exactly that seed and returned a fixed marked value.
3. The model returned `DONE` with `finish_reason: stop`.

Each tool was called once, in order, across three responses and three steps.
The request timeout was 25 seconds, the overall timeout 90 seconds, the wake
limit three steps, and the daily ledger budget USD 0.01. The run needed no retries.

| Response | Provider `prompt_tokens` | Provider `completion_tokens` | Computed ledger USD |
|---|---:|---:|---:|
| First tool call | 196 | 17 | 0.0000596 |
| Second tool call | 233 | 21 | 0.0000718 |
| Final answer | 275 | 4 | 0.0000598 |
| Total | 704 | 42 | 0.0001912 |

The client computed each row as
`prompt_tokens / 1e6 * 0.20 + completion_tokens / 1e6 * 1.20` and persisted it
through `Store.RecordLLMUsage`. Reading the SQLite daily ledger returned the
same token sums and USD 0.0001912. The offline integration test also checks the
wake totals against the daily ledger.

The provider response supplied token usage, not a billed dollar amount. These
USD figures are the local ledger calculation from the configured flat rates,
not a claim about an invoice or account balance. The template has no separate
cached-input price; this ledger does not apply provider discounts, cache-write
charges, or other pricing tiers. This small run demonstrates two dependent
calls; it does not resolve the spec's unknown about long-loop drift.

## Offline evidence and integration boundary

Normal `go test ./...` excludes the live test through the `llm_live` build tag.
All default HTTP tests use `httptest` servers or an injected fake transport.
Saved multi-step responses are in `internal/llm/testdata/tool-loop.json`.

- `TestMultiStepToolLoop`: two tool calls, final stop, message IDs and configured
  model/endpoint/key, and summed usage at configured prices.
- `TestStepLimitAndMalformedTools`: batched calls cannot exceed the wake step
  limit; unknown tools, invalid JSON, non-object arguments and invalid call types
  consume steps without being dispatched.
- `TestMalformedToolErrorsReachModel`: unknown tools, invalid JSON and dispatcher
  errors return tool error messages, allowing model recovery.
- `TestBudgetAndAccounting`, `TestBudgetRecheckedAfterResponseAndBeforeRetry`:
  spent daily budgets block HTTP requests, including retries, and the injected
  clock supplies the UTC ledger day.
- `TestLoopPersistsLedgerAndWakeTotals`: real SQLite daily and wake totals equal
  450 input tokens, 35 output tokens and USD 0.00052 for the saved fixture.
- `TestRetryBackoff`: HTTP 429/5xx retry after one and two seconds, success after
  retry, exhaustion at three attempts, and no retry on HTTP 400. Timing is
  injected; transport failures do not trigger an uncertain replay.
- Additional client tests cover usage retained before malformed choice rejection,
  missing/invalid usage, ledger failures, cancellation, timeouts, output bounds,
  redirect refusal and response size/read failures.
- `TestCancelledWakeStillAccountsCompletedResponse`: cancellation after the
  HTTP response still records known usage through a separately bounded write,
  and prevents tool execution.
- Store tests in `internal/store/ledger_test.go` cover atomic accumulation, agent
  and day isolation, missing/wrong-agent wakes, rollback after either write
  fails, invalid usage, and cancellation.

A step is each tool call, including a malformed call, or a final assistant
response with no calls. Retries do not consume additional model/tool steps.
Accounting happens before interpreting choices or dispatching tools, including
reported usage on retryable error responses. The budget is checked before each
HTTP attempt; a response can cross the budget because the spec blocks at the
limit rather than reserving a predicted response cost. A request is charged to
the UTC day on which that attempt started.

The caller supplies the dispatcher. Production callers must wrap it in
`internal/guard`; this issue introduces no production tool registry or tool
implementations. All dispatchers in these tests are fake local operations.
Wake assembly, audit records, stall detection and scheduling remain later issues.

Final validation passed with Go 1.27.3:

```text
go vet ./... && go test ./...              PASS
go test -race ./internal/llm ./internal/store   PASS
git diff --check                            PASS
```

## Repeating the live check

Only repeat with explicit authorization for paid API use. The default offline
suite never loads credentials or calls a provider. The opt-in command is:

```sh
go test -tags llm_live ./internal/llm -run '^TestLiveTwoToolLoop$' -count=1 -v
```

It additionally requires `THREADWALKER_LIVE_LLM=1` and `OPENAI_API_KEY` in the
child process environment. For this measurement, a local Python launcher read
only the `OPENAI_API_KEY` entry from the owner-authorized
`/home/ubuntu/Projects/threadwalker/.env`, parsed its quoting without evaluating
shell code, and passed that value directly to the child environment. It never
printed the value, copied the credential file, or passed secrets in command-line
arguments. The test converts the environment value to `config.Secret` and prints
only safe counts, computed costs, timestamps and fake tool names.
