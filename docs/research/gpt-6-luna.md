# gpt-6-luna as the first LLM: prices and reasoning-effort decision

Measured on 2026-11-12 for issue #28, after the owner chose OpenAI `gpt-6-luna` as the first live
LLM. The owner authorized the small paid checks below. No VPS, Threads, Telegram, Anthropic or
browser requests were made.

## Official prices

Checked on 2026-11-12 at <https://developers.openai.com/api/docs/pricing> and the model page
<https://developers.openai.com/api/docs/models/gpt-6-luna>:

| Item | USD per 1M tokens |
|---|---:|
| Standard short-context input (≤272K) | 0.10 |
| Standard short-context cached input | 0.01 |
| Standard short-context output | 0.50 |
| Standard long-context input (>272K) | 0.20 |
| Standard long-context output (>272K) | 0.75 |

Batch and Flex are half of Standard; Fast mode is double. Regional processing adds 10%. A prompt
above 272K input tokens is billed at the long-context rates for the whole request. The template and
catalog carry the two short-context Standard rates only, because the ledger has exactly one input
and one output price and does not model cached, Batch/Flex, Fast, regional or long-context tiers.
At these rates the owner's USD 2.00 per day cap equals 20M input tokens or 4M output tokens per day
(less when both are spent). These are configured flat-rate inputs for local accounting, not invoice
figures.

## Constraint that shaped the config

The model page states: "Use the Responses API for built-in tools and function calling. Chat
Completions supports function calling only with `reasoning_effort` set to `none`." A live probe
confirmed the provider's rejection:

```text
Function tools with reasoning_effort are not supported for gpt-6-luna in
/v1/chat/completions. To use function tools, use /v1/responses or set
reasoning_effort to 'none'.
```

The same request with `"reasoning_effort":"none"` returned a tool call. The client already had an
internal `llm.Options.ReasoningEffort`, but no template or catalog field reached it, so this issue
adds the optional `llm.reasoning_effort` (inline) and `models.<id>.reasoning_effort` (catalog)
fields. Unset omits the JSON key; the `anthropic` protocol rejects a nonempty value.

## Chat Completions (`none`) versus Responses (medium reasoning)

Both variants used the eval suite's realistic two-tool wake prompt (`valid_two_tool_loop`, tools
`lookup_seed` then `append_marker`), `gpt-6-luna`, a 512-token output cap, and the prices above.
Chat Completions sent `reasoning_effort: "none"`. Responses sent `reasoning.effort: "medium"` and the
same function tools in the Responses shape. Three runs each, tool results supplied locally:

| Variant | Runs OK | Tool calls | Avg input tok | Avg output tok | Avg cost USD | Avg latency |
|---|---:|---|---:|---:|---:|---:|
| Chat Completions, `none` | 3/3 | `lookup_seed`, `append_marker` | 591 | 51 | 0.0000846 | 4.9 s |
| Responses, `medium` | 3/3 | `lookup_seed`, `append_marker` | 348 | 59 | 0.0000643 | 7.6 s |

An earlier exploratory pair with an under-specified prompt ("Look up the seed...", no label) had
Chat Completions invent `label: demo` and finish, while Responses asked which label to use and made
no tool call. That difference is about ambiguity handling, not capability: Responses was slightly
faster there (5.4 s versus 5.8 s, one call each) and spent 73 reasoning tokens. The total cost of
all eight exploratory/measured calls was about USD 0.00058.

### Decision

Do not add an `openai_responses` protocol now. Ship `reasoning_effort: none` on Chat Completions.
The reasons, in order of weight:

1. No measured quality gain on the realistic wake prompt: both variants completed 3/3 with the same
   two tool calls and correct results; the only content difference was wording.
2. Responses was consistently slower (7.6 s versus 4.9 s average) and reported no reasoning tokens
   for this task, so the advertised reasoning budget did not buy better decisions here.
3. The adapter would be a second wire contract to keep correct and would not replace Chat
   Completions, because the owner's configured template deliberately stays on one protocol; not
   adding it keeps the surface small. Responses also reports some tokens differently (`input_tokens`
   lower than Chat Completions for the same history), which would need its own usage-normalization
   evidence and ledger tests.
4. The ambiguous-prompt observation is not evidence that medium reasoning is better for autonomous
   wakes; asking the owner a question inside a wake is a stall, not a successful action.

Revisit this if a future eval shows a content-quality gap that `none` cannot close, or if the owner
wants provider-hosted tools, which only Responses exposes. The comparison program was a throwaway
outside the repository; the exact request shapes are described above and the offline suite still
covers only the committed protocols.

## Template block

The owner chose an inline LLM block for the viral template, not a catalog profile or a `llms.yaml`.
The block sent to the template worker (and now in `templates/viral.agent.yaml`) is:

```yaml
llm:
  protocol: openai
  base_url: https://api.openai.com/v1
  model: gpt-6-luna
  api_key_env: OPENAI_API_KEY
  reasoning_effort: none
  price_per_mtok_in_usd: 0.10      # Standard short-context input, 2026-11-12
  price_per_mtok_out_usd: 0.50     # Standard short-context output, 2026-11-12
```

The catalog keeps the same optional field (`models.<id>.reasoning_effort`) for other operators, but
nothing in this change requires a catalog.

## Offline and live eval with luna

Offline, the committed suite passed 30/30 (15 cases × the two synthetic profiles), estimated
synthetic cost USD 0.00141, no network.

Live, a three-case selection ran against `gpt-6-luna` with a temporary catalog outside the
repository (provider `openai`, the prices above, `reasoning_effort: none`), `-max-usd 0.02`,
`-max-steps 8`, `-max-output-tokens 512`:

| Case | Result | End reason | Tools | Tokens in/out | Cost USD |
|---|---|---|---:|---:|---:|
| `step_stop` | pass | `step_limit` | `lookup_seed` | 130 / 20 | 0.0000230 |
| `valid_two_tool_loop` | fail (`content_mismatch`) | `completed` | `lookup_seed`, `append_marker` | 610 / 51 | 0.0000865 |
| `deterministic_quality_rubric` | fail (`content_mismatch`) | `completed` | none | 14 / 17 | 0.0000099 |
| **Total** | **1/3 pass** | | | **754 / 88** | **0.0001194** |

Both failures are expected and do not indicate a protocol fault: the fixtures assert exact
synthetic text (`DONE` and a fixed rubric sentence) that a live model does not echo. The tool
sequence, completion and step limit were correct in all three. This run says nothing about
long-loop drift, production wake memory or the quality of real Threads content. The three-case
live pass rate is 1/3 under deterministic exact-output checks; the separate two-tool comparison
above was 6/6 on tool calls. The live total was USD 0.0001194 of configured flat-rate accounting,
not an invoice, and the whole issue's paid checks stayed below USD 0.001.

