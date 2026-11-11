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
| `LLM_API_KEY` | Key for the OpenAI-compatible provider selected in your template |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token |
| `TELEGRAM_OWNER_CHAT_ID` | Your numeric Telegram chat ID |

Load your file into the current shell before running commands:

```sh
set -a
. ./.env
set +a
```

The binary reads environment variables; it does not load `.env` itself.

## Agent template

Edit [`agents/solo-builder.example.yaml`](../agents/solo-builder.example.yaml). Keep `dry_run: true`.
Set `llm.base_url`, `llm.model` and both token prices for your chosen OpenAI-compatible provider;
the example currently points to DeepSeek. Put that provider's key in `LLM_API_KEY`.
Set `threads.profile_dir` to the saved browser profile on the machine where the agent will run.
The template's `*_env` fields can select different variable names when needed.

Once the template's selected LLM key and both Telegram entries are available:

```sh
go run ./cmd/threadwalker check -agents agents -db data/threadwalker.db
```

Expect `check passed`. This validates templates and SQLite migrations without calling external
services. Telegram entries are currently required even in dry run.

## Accounts and live checks

For the [browser spike (#2)](https://github.com/canhta/threadwalker/issues/2), provide SSH access
to the VPS and a disposable Threads account with an AI-experiment disclosure in its bio.
Complete login and 2FA yourself, and leave the logged-in browser profile on the VPS. Supply the
SSH target, saved profile path and approved reply URL when starting the spike; these are setup
details, not environment variables. Passwords and session cookies stay out of chat and Git.
The spike measures all six browser operations over three days before browser-backed work proceeds.

For [Telegram (#7)](https://github.com/canhta/threadwalker/issues/7), create a bot with
`@BotFather`, save its token, and send the bot a message from your own account. Supply that
conversation's numeric chat ID as `TELEGRAM_OWNER_CHAT_ID`.

The [seven-day live run (#9)](https://github.com/canhta/threadwalker/issues/9) needs your approval
before `dry_run` becomes `false`. The [second agent (#12)](https://github.com/canhta/threadwalker/issues/12)
needs another disposable account and saved login. Keep these issues open until their measured
acceptance criteria hold.

[`deploy/threadwalker.service`](../deploy/threadwalker.service) currently runs only `check`.
It expects environment variables in `/etc/threadwalker/threadwalker.env`; the root `.env`
is local setup storage. The unit does not yet start an agent loop.
