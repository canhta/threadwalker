# Setup

Use the Go version declared in [`go.mod`](../go.mod). From the repo root:

```sh
go vet ./... && go test ./...
```

## Private configuration

Keep credentials in `.env` at the repo root, which Git ignores. Create it from
the blank [example](../.env.example) if it does not exist:

```sh
test -f .env || cp .env.example .env
chmod 600 .env
```

Quote values containing spaces or shell characters.

| Variable | Value to supply |
|---|---|
| `LLM_API_KEY` | Key for the provider that names it in your LLM catalog |
| `SECONDARY_LLM_API_KEY` | Key for the second provider, when a selected profile uses it |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token |
| `TELEGRAM_OWNER_CHAT_ID` | Your numeric Telegram chat ID |

These names are examples, not fixed requirements: each provider's `api_key_env`
chooses the variable it reads. A provider whose profiles are never selected may
have an empty key.

Load your file into the current shell before running commands:

```sh
set -a
. ./.env
set +a
```

The binary reads environment variables; it does not load `.env` itself.

## LLM providers and model profiles

Providers and models live in the catalog, and the agent template selects one
model profile from it. The canonical catalog example is
[`llms.example.yaml`](../llms.example.yaml). Provider IDs and model profile IDs
are arbitrary. Each provider declares a `protocol` (`openai` or `anthropic`),
`base_url` and `api_key_env`. Each model profile references a provider, names the
provider's model ID and sets input and output prices per million tokens.

Edit the example, or a copy of it, and fill every placeholder model ID and price
before real use. The `openai` protocol also covers compatible custom endpoints.
`anthropic` selects native Messages: `base_url` may be the API root or end in
`/v1`, and the adapter appends the Messages path once. The catalog's `primary`,
`secondary`, `fast`, `reasoning` and `backup` names are examples; you can rename
them and use any model ID supported by the selected endpoint. Changing
`api_key_env` selects your own environment-variable name. Credentials stay in
HTTP authentication headers, outside model messages. Neither protocol implies
support for every vendor API.

Both protocols use the same bounded tool loop, caller-supplied guarded dispatcher
and usage ledger. Native Messages supports text and client-side function tools,
with a 1024-token output limit unless the caller sets `MaxCompletionTokens`.
OpenAI-specific `ReasoningEffort` is rejected for native Messages; extended
thinking, streaming, images and provider-hosted tools are outside this contract.
Reported native cache input is counted in total input; the catalog's two flat
prices do not reproduce cache/tier pricing or provider invoices.

A template selects a profile by replacing its entire inline `llm` block with
`llm.profile`:

```yaml
llm:
  profile: fast
```

`fast` is a profile ID from the catalog; choose any profile defined there. Do not
combine `profile` with inline `base_url`, `model`, `api_key_env` or price fields.
Templates with the existing inline `llm` block keep working when no profile is
selected.

Check a catalog together with the templates:

```sh
go run ./cmd/threadwalker check -agents agents -db data/threadwalker.db -llms llms.example.yaml
```

Without `-llms`, `check` validates legacy inline templates only. With it, `check`
validates the catalog structure and every template reference, and resolves keys
only for selected profiles. Errors name the offending field without printing key
values.

## Agent template

Edit [`agents/solo-builder.example.yaml`](../agents/solo-builder.example.yaml).
Keep `dry_run: true`. Select a model profile as shown above, or set the inline
`llm` fields. Put the selected provider's key in the environment variable named
by its `api_key_env`. Set `threads.profile_dir` to the saved browser profile on
the machine where the agent will run. The template's `*_env` fields can select
different Telegram variable names when needed.

Once the selected LLM key and both Telegram entries are available, run `check`.
Add `-llms llms.example.yaml` when the template selects a profile; omit it for a
legacy inline template:

```sh
go run ./cmd/threadwalker check -agents agents -db data/threadwalker.db
```

`check` validates templates and SQLite migrations without calling external
services. At its default `info` level it emits JSON lifecycle records to stdout.
Telegram entries are currently required even in dry run.

## Operational logs

Choose the log level and format with flags. The canonical event names and
metadata allowlist are in [`internal/logging/logging.go`](../internal/logging/logging.go);
the contract is in [`docs/spec.md`](spec.md).

```sh
go run ./cmd/threadwalker check -agents agents -db data/threadwalker.db -log-format json -log-level debug
```

Default is `-log-format json -log-level info`. Use `-log-format text` for local
reading. Even at `debug`, records carry allowlisted metadata only: no credentials,
headers, endpoint URLs, prompts, notes, Threads text, stranger usernames, tool
arguments/results or raw provider bodies.

`journalctl` receives the same JSON lines. Filter retries by wake and HTTP status:

```sh
journalctl -u threadwalker -o cat --since '15 min ago' \
  | jq -c 'select(.event == "llm.request.retry") | {time, agent_id, wake_id, request, attempt, http_status, reason}'
```

Correlate one wake's requests with its outcome and locally computed usage:

```sh
journalctl -u threadwalker -o cat --since '15 min ago' \
  | jq -c 'select(.agent_id == "solo-builder" and .wake_id == 42) | {event, request, attempt, http_status, tokens_in, tokens_out, cost_usd, outcome, reason}'
```

Replay the saved synthetic failure trace offline. Its first request gets HTTP 503
and records usage; retry 2 records usage before an invalid assistant choice ends
the run. The two accounting records sum to 300 input tokens, 30 output tokens
and USD 0.00036 at the configured synthetic flat rates:

```sh
go test ./internal/llm -run '^TestSyntheticRetryFailureTrace$' -count=1
jq -c 'select(.agent_id == "synthetic-agent" and .wake_id == 42) | {event, request, attempt, http_status, tokens_in, tokens_out, cost_usd, outcome, reason}' internal/llm/testdata/retry-failure-trace.jsonl
jq -s '[.[] | select(.event == "llm.usage.recorded" and .outcome == "success")] | {tokens_in: (map(.tokens_in) | add), tokens_out: (map(.tokens_out) | add), cost_usd: (map(.cost_usd) | add)}' internal/llm/testdata/retry-failure-trace.jsonl
```

Filter `.reason == "retry_exhausted"`, `"accounting_write_failed"`,
`"budget_limit"` or `"step_limit"` to find the corresponding failures, then use
`agent_id`, `wake_id`, `request` and `attempt` to inspect the preceding trace.
Tool IDs are local `wake/step` values, rather than arbitrary provider IDs.

The `wake_id 42` above and any durations or costs are synthetic example values,
not production evidence. `llm.tool.finished` covers only the client dispatcher
boundary. [`deploy/threadwalker.service`](../deploy/threadwalker.service) still
runs `check` only, so journald will not contain wake-loop events until a
scheduler issue implements them.

## Accounts and live checks

For the [browser spike (#2)](https://github.com/canhta/threadwalker/issues/2),
provide SSH access to the VPS and a disposable Threads account with an
AI-experiment disclosure in its bio. Complete login and 2FA yourself, and leave
the logged-in browser profile on the VPS. The SSH target, saved profile path and
approved reply URL are setup inputs, not environment variables. Passwords and
session cookies stay out of chat and Git.

For [Telegram (#7)](https://github.com/canhta/threadwalker/issues/7), create a bot
with `@BotFather`, save its token, and send the bot a message from your own
account. Supply that conversation's numeric chat ID as
`TELEGRAM_OWNER_CHAT_ID`.

The [seven-day live run (#9)](https://github.com/canhta/threadwalker/issues/9)
needs your approval before `dry_run` becomes `false`. The
[second agent (#12)](https://github.com/canhta/threadwalker/issues/12) needs
another disposable account and saved login. Keep these issues open until their
measured acceptance criteria hold.

[`deploy/threadwalker.service`](../deploy/threadwalker.service) currently runs
only `check`. It expects environment variables in
`/etc/threadwalker/threadwalker.env`; the root `.env` is local setup storage. The
unit does not yet start an agent loop.

## Opt-in LLM protocol checks

Default tests run offline. The paid two-tool check requires the `llm_live` build
tag and `THREADWALKER_LIVE_LLM=1`. Configure `THREADWALKER_LIVE_PROTOCOL`
(`openai` or `anthropic`), `THREADWALKER_LIVE_BASE_URL`,
`THREADWALKER_LIVE_MODEL`, `THREADWALKER_LIVE_KEY_ENV` (the name of an existing
key environment variable), and positive `THREADWALKER_LIVE_PRICE_IN` /
`THREADWALKER_LIVE_PRICE_OUT` rates per million tokens. Optionally set
`THREADWALKER_LIVE_REASONING_EFFORT` for the OpenAI protocol. The test has no
provider/model defaults and invokes only fake local tools.

```sh
go test -tags llm_live ./internal/llm -run '^TestLiveTwoToolLoop$' -count=1 -v
```

Run only with authorization and verified current model rates. The check caps
outputs at 256 tokens per response, three steps, three HTTP attempts per step,
a 25-second request timeout, a 90-second wake timeout and a USD 0.01 local daily
ledger budget. A response can cross that ledger threshold; the small fixed
prompt and output cap bound this spike, rather than promising a provider billing
cap. See [the measured evidence](research/spike-llm.md) for exact models, usage,
computed costs and limitations.
