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

## Configured protocol adapters (issue #15)

Measured on 2026-11-11 after #4 and #14 closed with evidence. The runtime now
selects an internal wire adapter from resolved `LLMConfig.Protocol`; an empty
protocol preserves legacy OpenAI-compatible behavior. Provider/profile names,
model IDs, endpoints, key variable names and prices remain operator inputs.
Native Messages appends `/v1/messages` to the configured API root, or `/messages`
when the base already ends in `/v1`. OpenAI-compatible Chat Completions preserves
the configured base path and appends `/chat/completions`.

Official protocol references checked for this implementation:

- [OpenAI Chat Completions](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create):
  function tool calls carry JSON arguments in a string; results identify the call.
- [Anthropic Messages](https://platform.claude.com/docs/en/api/messages/create) and
  [tool use](https://platform.claude.com/docs/en/agents-and-tools/tool-use/overview):
  top-level system instructions, `input_schema`, assistant `tool_use` blocks and
  user `tool_result` blocks, with native input/output token usage.
- [Anthropic versions](https://platform.claude.com/docs/en/api/versioning):
  the adapter pins the documented `2023-06-01` Messages contract. It sends
  `x-api-key` and `anthropic-version` headers. The newer version is not required
  for the text/client-tool subset implemented here.

Both protocols use the same budget checks, bounded HTTP attempts, step counting,
transactional usage writes and caller-owned guarded dispatcher. Native output
uses `max_tokens`, defaulting to 1024 when no explicit output bound is supplied.
OpenAI-specific reasoning effort is rejected for native requests. This subset
supports text and client-side function tools; it does not claim support for
extended thinking, multimodal content, streaming or provider-hosted tools.
Unsupported native response blocks stop the loop after recording known usage.
Truncated tool responses stop before dispatch even if their partial arguments
happen to parse as a JSON object. Malformed arguments and unknown tools produce
normalized tool errors; native
errors have `is_error: true`. Consecutive native tool results are grouped in one
user message. HTTP response bodies and transport errors are not echoed into
errors; dispatcher failures use a generic tool error. Returned content and tool
results redact the configured credential before becoming model context.

### Per-protocol live evidence

The owner authorized one small two-tool check for each provisioned protocol.
Both ran the configurable `TestLiveTwoToolLoop`, with exactly the two fake local
tools described above, then final `DONE`. The three-step, 256-output-token,
25-second-request, 90-second-wake and USD 0.01 ledger limits were retained.
Each check recorded three responses; the SQLite ledger matched their usage sum.
No VPS, Threads or Telegram access occurred.

| Protocol | Exact configured model | Started UTC | Elapsed | Input rate USD/MTok | Output rate USD/MTok |
|---|---|---|---:|---:|---:|
| `openai` | `gpt-5.6-luna` | 2026-11-11T12:08:39Z | 5.59 s | 0.20 | 1.20 |
| `anthropic` | `claude-haiku-4-5-20251001` | 2026-11-11T12:08:45Z | 3.04 s | 1.00 | 5.00 |

The chosen models/rates were checked against the official
[OpenAI model page](https://developers.openai.com/api/docs/models/gpt-5.6-luna),
[Anthropic model page](https://platform.claude.com/docs/en/models/haiku-4-5/overview)
and [Anthropic pricing](https://platform.claude.com/docs/en/about-claude/pricing).
They are run-specific inputs, not runtime or test defaults. Both protocols
returned valid JSON object arguments and accepted the correlated tool results.

| Protocol | Response | Provider input tokens | Provider output tokens | Computed ledger USD |
|---|---|---:|---:|---:|
| `openai` | `lookup_seed` | 196 | 17 | 0.0000596 |
| `openai` | `append_marker` | 233 | 21 | 0.0000718 |
| `openai` | `DONE` | 275 | 4 | 0.0000598 |
| **OpenAI total** | | **704** | **42** | **0.0001912** |
| `anthropic` | `lookup_seed` | 681 | 66 | 0.0010110 |
| `anthropic` | `append_marker` | 770 | 73 | 0.0011350 |
| `anthropic` | `DONE` | 868 | 5 | 0.0008930 |
| **Anthropic total** | | **2319** | **144** | **0.0030390** |

Combined locally computed cost was **USD 0.0032302**, below the authorized
USD 0.10 total. Neither provider returned billed dollar amounts; these are flat
rate ledger calculations, not invoice evidence. Native input normalization sums
`input_tokens`, `cache_read_input_tokens` and `cache_creation_input_tokens` when
reported. The ledger has only two configured prices, so it does not reproduce
cache discounts/surcharges, long-context tiers or other special billing.

A local Python launcher parsed only the selected credential entries from the
owner's private root `.env` using `shlex`, without evaluating shell code. It
passed the original `OPENAI_API_KEY` and `ANTHROPIC_API_KEY` into the respective
child environments and set the nonsecret live settings separately. It printed
only test status, token counts, computed costs, models and fake tool names. No
credential contents entered model context, output, tracked files or command-line
arguments. No private file was copied and no persistent launcher was created.

The live test now requires explicit protocol, base URL, model, key environment
variable name and positive price settings; it has no hardcoded provider/model
selection. Repetition settings and limits are documented in
[setup.md](../setup.md#opt-in-llm-protocol-checks). The earlier #4 command above
remains the test entrypoint, with these new required settings. Default tests
exclude the live file and run offline.


Additional offline adapter evidence:

- `TestNativeProtocolSelectionAndAccounting`: native endpoint/headers and usage
  normalization through the unchanged public constructor and run loop.
- `TestNativeCacheUsageAndMalformedContentAccounting`: all reported native input
  categories are counted, negative/overflow usage is rejected, and valid usage
  is retained before malformed or unsupported content is rejected.
- `TestTransportAndToolResultsRedactCredentials`: reflected tool output and
  caller-supplied message content cannot send the configured credential into
  either protocol's model context; transport errors suppress credential text.
- `TestTruncatedToolCallsNeverDispatch`: both protocols account for usage and
  refuse incomplete tool responses before any caller/guard dispatch.

Independent protocol acceptance tests were integrated from helper commit
`6ba44a605d836a9ec93f06ab19c61cd87b6c3c0c` (integration commit `6b4e09d`).
The helper first observed failure on the pre-adapter baseline, then passed
against the implemented adapters. The final integrated offline
`go vet ./... && go test ./...` passed for all packages.

- `TestProtocolSelectionAcrossEndpoints`: distinct arbitrary providers and
  multiple model IDs, both wire protocols, configured custom base paths/headers,
  legacy empty protocol and native API-root/already-versioned endpoints.
- `TestProtocolOpenAISystemMessageStaysInMessages` and
  `TestAnthropicRequestSchemaSystemAndHeaders`: separate native system prompt,
  native tool schema/output limits and protocol-specific authentication.
- `TestAnthropicTwoToolConversationUsesReturnedIDs`: saved synthetic fixture
  `internal/llm/testdata/protocol/anthropic-two-tool-loop.json`, two correlated
  tool calls/results, and final response.
- `TestAnthropicToolErrorsBecomeNativeToolResults` and
  `TestAnthropicMalformedResponses`: malformed arguments, unknown tools,
  dispatcher rejection, invalid response shapes and missing/duplicate IDs.
- `TestAnthropicUsageSumsIntoExistingLedger`: usage across native responses at
  the configured flat prices.
- `TestAnthropicRetryThenSuccessAndExhaustion`,
  `TestAnthropicTimeoutAndCancellation` and
  `TestAnthropicOutputStepAndBudgetLimits`: bounded retries, injected backoff,
  context cancellation, request timeouts, output bounds and pre-request limits.
- `TestAnthropicErrorsDoNotLeakSecrets`: provider errors and dispatcher errors
  remain generic, without credential text in errors or subsequent messages.
