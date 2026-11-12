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

## Dreaming, study and trend signals

These are per-agent template fields, not flags or separate files. Omit them to disable dreaming
and study; normal wakes are unchanged.

```yaml
limits:
  max_usd_per_day: 2.00
  max_usd_per_dream: 0.30   # dream spend is outside max_usd_per_day
  max_steps_per_dream: 20
schedule:
  timezone: Asia/Ho_Chi_Minh
  dream_at: "23:30"          # once per local day; a missed one runs once on next start
  study_slots: ["09:00", "15:00", "20:30"]
trends:
  include_keywords: ["AI", "LLM", "agent", "benchmark"]
  avoid_keywords: ["crypto", "airdrop"]
study:
  sources: [google_news_vn, google_news_global, google_news_topics, vnexpress]
  threads_sample:
    max_reads: 4
    pause_ms: 1500
    keywords: ["AI thay dev", "agent tự động"]
  web_search:                # optional, study only, reuses OPENAI_API_KEY
    enabled: true
    model: gpt-6-luna
    max_results: 5
    max_output_tokens: 512
```

Dream and study are guard-enforced read-only: `post`, `reply`, owner messages and schedule changes
are refused with `phase_read_only`. Study is trend-hunting: it samples outside sources and Threads
read-only, then writes notes on hot stories, why they are hot and the roast angle. Study slots are
a minimum cadence; a deterministic score spike can add a session. Dream and study do not move the
agent's normal next wake.

`study.sources` accepts only these IDs: `google_news_vn`, `google_news_global`,
`google_news_topics`, `vnexpress`, `genk`, `tinhte`, `vietnamnet`, `the_verge`, `techcrunch`,
`arstechnica`, `hacker_news`, `product_hunt`, `google_trends`, `reddit_localllama`,
`reddit_openai`, `reddit_singularity`, `github_trending`, `youtube`. Omitted sources default to all
credential-free sources. `youtube_channels` defaults to the measured public channel list.

Reddit has no unauthenticated public feed. To enable the Reddit sources, create a Reddit app at
<https://www.reddit.com/prefs/apps> and register its app-only (client-credentials) OAuth client,
then set `REDDIT_CLIENT_ID` and `REDDIT_CLIENT_SECRET`. Without both variables those sources are
skipped without a network call.
Optional template fields `study.reddit_client_id_env`/`study.reddit_client_secret_env` rename
those variables. YouTube channel feeds need no key. `study.web_search` uses the selected
template's OpenAI key (for the live template, `OPENAI_API_KEY`) for one bounded Responses
web-search call in study, never the chat/tool loop.

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
never retried. Multi-line text is typed line by line with `Shift+Enter`; captcha, rate-limit banners
and account survival remain unmeasured in
[#18](https://github.com/canhta/threadwalker/issues/18). The authorized Orca Browser session is a
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
The owner must message the bot first. Optional `digest_hour` defaults to 18 UTC. Displayed times
use `telegram.timezone`, an IANA zone defaulting to `Asia/Ho_Chi_Minh`; the digest schedule itself
stays UTC. Every `getUpdates` sends `allowed_updates: ["message"]`; Telegram persists that filter
per bot, so omitting it can leave a stale filter that hides owner messages.

On service start the bot replaces its command menu with `/help`, `/status`, `/pause`, `/resume`,
`/kill` and `/dnc` (single-agent forms; agent-ID forms when one bot covers several agents). A
failed menu registration is logged as `menu_failed` and never stops polling.

Owner-facing text is plain text built from code-owned facts and the template persona. The speaker
name comes from `persona.name`, menu and fallback copy follow `persona.language`, and a persona's
own voice belongs in `telegram.messages` in the agent template: text/template strings rendered
with the documented fields (see `internal/telegram/copy.go`), where `{{nl}}` inserts a line break.
Missing kinds fall back to neutral vi/en copy. Counts, limits, spend and next-wake times are never
written by the model. Free text from the owner that is neither a command nor a reply is stored as
an owner note for the next wake and acknowledged once, with no extra model call.

With several agents, owner commands include the agent ID: `/status <agent_id>`,
`/pause <agent_id>`, `/resume <agent_id>`, `/kill <agent_id>` and `/dnc <agent_id> <username>`.
`/help` and `/start` list the commands. Kill is terminal. Reply directly to a delivered question
to answer it. Delivery, recovery and live evidence are documented in
[Telegram verification](research/telegram-local.md) and [live bot qualification](research/telegram-live.md).

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
