# Setup

Use the Go version in [go.mod](../go.mod). Run commands from the repository root.

## Run locally without credentials

```sh
go vet ./... && go test ./...
go run ./cmd/threadwalker run --once --offline
```

Build the binary with `go build -o threadwalker ./cmd/threadwalker`; the commands below use it.
`threadwalker --help` and `threadwalker <command> --help` print usage, `threadwalker version` prints
the build version, and exit codes are 0 success, 1 runtime failure, 2 usage error. Structured
operational events stay on stdout; human diagnostics and usage errors go to stderr.

The smoke run uses a temporary SQLite DB and in-process synthetic services. It reads no keys
and cannot publish. To export a two-agent **48-hour logical-time** soak with one restart:

```sh
threadwalker run --offline --soak-hours 48 --soak-agents 2 --evidence /tmp/threadwalker-isolation
```

Use a fresh evidence directory. It contains `isolation.json`, `events.jsonl` and `eval.json`.
Omit `-soak-agents 2` for the one-agent soak. Logical time advances in seconds; it is not an
elapsed-time or real-account qualification. See [isolation evidence](research/isolation-local.md).

## Configure credentials and models

Keep `.env` at the repository root; Git ignores it. Preserve an existing file:

```sh
test -f .env || cp .env.example .env
chmod 600 .env
```

Fill the variables your selected integrations use; quote values containing shell characters.

| Example variable | Used for |
|---|---|
| `LLM_API_KEY` | Selected provider |
| `SECONDARY_LLM_API_KEY` | Another selected provider, if needed |
| `TELEGRAM_BOT_TOKEN` | Optional Telegram integration |
| `TELEGRAM_OWNER_CHAT_ID` | Owner's numeric chat ID |

Provider IDs, model IDs and key variable names are configurable. Edit a copy of
[llms.example.yaml](../llms.example.yaml): set each provider's `base_url`, `protocol` and
`api_key_env`, then add model profiles with their model IDs and input/output prices per million
tokens. Fill placeholder IDs and prices before real use. Supported wire protocols are `openai`
(Chat Completions compatible endpoints) and `anthropic` (native Messages).

Select a profile in the agent template by replacing the entire inline `llm` block:

```yaml
llm:
  profile: fast
```

`fast` is an example profile ID; use any profile in your catalog. A template can instead keep
its inline LLM settings. Do not mix inline fields with `profile`. Only selected providers need
keys. Prices produce local estimates, not provider balances or invoices.

The binary reads environment variables and loads `./.env` at startup when that file exists. It never
overrides a variable that is already set and never logs a value; `THREADWALKER_ENV_FILE` points at a
different file. To inject the file yourself instead:

```sh
set -a
. ./.env
set +a
```

## Choose agent templates

The `agents/` directory contains [builder](../agents/solo-builder.example.yaml) and
[teasing](../agents/teasing.example.yaml) examples. The `templates/` directory holds the live account
template, [viral](../templates/viral.agent.yaml) (`*.agent.yaml` files are templates). `check` and
`run` use `templates/` when it exists and fall back to the shipped `agents/` examples; `--agents`
(or `-a`) selects any directory. Copy only the templates you want into your own directory.

Set each agent's goal, persona, limits and LLM selection. Keep `dry_run: true`. For multiple
agents, use distinct IDs, `threads.username` values and browser profile paths. Replace the
synthetic usernames before real-account operation; usernames are not inferred from agent IDs.

Validate your directory and optional catalog without making external calls:

```sh
threadwalker check
threadwalker check --agents /path/to/templates --llms /path/to/llms.yaml
```

`--db` defaults to `data/threadwalker.db`; `THREADWALKER_AGENTS`, `THREADWALKER_DB` and
`THREADWALKER_LLMS` set the same defaults. A flag beats the environment variable, which beats the
built-in default. Omit `--llms` for inline LLM settings. Once configured, `run` uses the same paths:

```sh
threadwalker run
threadwalker run --agents /path/to/templates --llms /path/to/llms.yaml
```

This command may call the selected LLM and trend sources. `-once` limits it to one due wake.
`-offline` forces fake transports and disables Telegram; explicit templates still require dry run.
Startup never replays interrupted actions. The optional [systemd unit](../deploy/threadwalker.service)
runs the same scheduler on a chosen host; a VPS is optional.

## Browser and Telegram

The default Threads backend is `stub`, which requires dry run. Browser configuration lives under
`threads`: `backend: browser` or `backend: agent_browser`, `base_url`, `executable_path` (Chrome)
and an agent-specific `profile_dir`. Chromium must already be installed; `no_sandbox` defaults to
false. `agent_browser` additionally requires an absolute `agent_browser_path` to the pinned
**agent-browser 0.42.0** CLI; install it with the upstream instructions or `npm install
agent-browser@0.42.0`. The adapter rejects any other version.

```yaml
threads:
  backend: agent_browser
  base_url: http://127.0.0.1:18080
  executable_path: /path/to/chrome
  agent_browser_path: /path/to/agent-browser
  profile_dir: /path/to/agent-profile
```

For measured Threads pages and live posts/replies, add `page_profile: threads`:

```yaml
threads:
  backend: agent_browser
  page_profile: threads
  base_url: https://www.threads.com
  executable_path: /path/to/chrome
  agent_browser_path: /path/to/agent-browser
  profile_dir: /path/to/agent-profile
```

The live profile requires `agent_browser` and an HTTPS `threads.com`/`threads.net` origin. It maps
feed, search and post reads from the measured accessibility tree, plus measured post/reply
submission and acknowledgement. Own metrics remain unavailable because the measured Insights
overview lacks all four counters. Keep `dry_run: true` to read without publishing; `dry_run: false`
publishes with `page_profile: threads`, and every write still goes through the Go guard's audit
row, reserved intent and limits before the click. An unacknowledged click is recorded as unknown and
never retried. Multi-line fill, captcha, rate-limit banners and account survival remain unmeasured
in [#18](https://github.com/canhta/threadwalker/issues/18). The authorized Orca Browser session is a
measurement surface, not a portable VPS profile.

**The default fixture profile supports saved synthetic contracts, not the current Threads site.**
Real selectors, login/session behavior and action-bound insights remain to be qualified in
[#2](https://github.com/canhta/threadwalker/issues/2) and
[#18](https://github.com/canhta/threadwalker/issues/18). Local reproduction commands are in
[browser evidence](research/browser-local.md), [agent-browser evidence](research/agent-browser.md)
and [Threads live mapping](research/threads-live-mapping.md).

Run the opt-in local CLI integration (needs the pinned binary and Chrome; every page is a
loopback fixture):

```sh
AGENT_BROWSER_BIN=/path/to/agent-browser CHROME_BIN=/path/to/chrome \
  go test -tags agent_browser_integration -run TestAgentBrowserLocalIntegration ./internal/threads/
```

Telegram stays disabled unless `telegram.enabled: true`. When enabled, set `bot_token_env` and
`owner_chat_id_env` to the variable names containing the bot token and owner's numeric chat ID.
The owner must message the bot first. Optional `digest_hour` defaults to 18 UTC.

With several agents, owner commands include the agent ID: `/status <agent_id>`,
`/pause <agent_id>`, `/resume <agent_id>`, `/kill <agent_id>` and `/dnc <agent_id> <username>`.
Kill is terminal. Reply directly to a delivered question to answer it. Delivery and recovery
behavior are documented in [Telegram verification](research/telegram-local.md).

## Dashboard, logs and evals

```sh
go run ./cmd/threadwalker dashboard -agents /path/to/templates -db data/threadwalker.db
```

Open `http://127.0.0.1:8080/`; `-listen` selects another loopback port. The DB must exist.
The dashboard needs no provider or Telegram credentials, opens SQLite read-only and shows
selected public metadata. Add `-llms` when templates use catalog profiles.

Operational logs are JSON to stdout by default. Use `-log-level debug` or `-log-format text`
on `check`, `run` or `dashboard`. Even debug output excludes prompts, credentials, private
content and raw tool payloads. Filter a saved trace by agent and wake:

```sh
jq -c 'select(.agent_id == "solo-builder" and .wake_id == 42)' events.jsonl
```

[Eval commands](evals.md) cover offline suites, report comparison, failure inspection and bounded
opt-in provider checks. [Metric evidence](research/metrics-local.md) and
[playbook evidence](research/playbook-local.md) explain collection windows, gate simulations and
known limitations. No real metrics are inferred from dry-run actions.

## Remaining real-account qualification

Supply logged-in disposable Threads profiles with the AI-experiment disclosure and their actual
usernames on a chosen host. Complete login/2FA yourself. The remaining measured work is:

- [#2](https://github.com/canhta/threadwalker/issues/2): real browser compatibility and observation.
- [#18](https://github.com/canhta/threadwalker/issues/18): real bot controls and single-agent soak.
- [#21](https://github.com/canhta/threadwalker/issues/21): two-account 48-hour run and memory measurement.
- [#9](https://github.com/canhta/threadwalker/issues/9): seven-day live experiment, with explicit approval before publishing.

Local fixture and logical-time results do not close these qualifications.
