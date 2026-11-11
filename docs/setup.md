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

Fill the entries you need; leave the rest empty. Quote values containing spaces or shell characters.

| Variable | Value to supply |
|---|---|
| `OPENAI_API_KEY` | OpenAI API key |
| `ANTHROPIC_API_KEY` | Anthropic API key, if needed by a later integration |
| `TW_SOLO_LLM_KEY` | DeepSeek key, only if keeping the example provider |
| `TW_VPS_SSH_TARGET` | SSH host or alias, such as `threadwalker-vps` |
| `TW_VPS_THREADS_PROFILE_DIR` | Absolute path to the saved browser profile on the VPS |
| `TW_SPIKE_REPLY_TARGET_URL` | Post URL you approve for the browser spike's one reply |
| `TW_TELEGRAM_TOKEN` | Telegram bot token |
| `TW_OWNER_CHAT_ID` | Your numeric Telegram chat ID |

Load your file into the current shell before running commands:

```sh
set -a
. ./.env
set +a
```

The binary reads environment variables; it does not load `.env` itself. The VPS/profile/reply
variables above record setup details for the operator; they do not configure the binary.

## Agent template

Edit [`agents/solo-builder.example.yaml`](../agents/solo-builder.example.yaml). Keep `dry_run: true`.
Set `llm.base_url`, `llm.model`, both token prices and `llm.api_key_env` for your chosen
OpenAI-compatible provider. For the supplied OpenAI key, use `api_key_env: OPENAI_API_KEY`;
the example currently points to DeepSeek and `TW_SOLO_LLM_KEY`. Set `threads.profile_dir` to the
profile path on the machine where the agent will run.

Once the template's selected LLM key and both Telegram entries are available:

```sh
go run ./cmd/threadwalker check -agents agents -db data/threadwalker.db
```

Expect `check passed`. This validates templates and SQLite migrations without calling external
services. Telegram entries are currently required even in dry run.

## Accounts and live checks

For the [browser spike (#2)](https://github.com/canhta/threadwalker/issues/2), provide SSH access
to the VPS and a disposable Threads account with an AI-experiment disclosure in its bio.
Complete login and 2FA yourself, and leave the logged-in browser profile on the VPS. Fill the
profile path and approved reply URL above. Passwords and session cookies stay out of chat and Git.
The spike measures all six browser operations over three days before browser-backed work proceeds.

For [Telegram (#7)](https://github.com/canhta/threadwalker/issues/7), create a bot with
`@BotFather`, save its token, and send the bot a message from your own account. Supply that
conversation's numeric chat ID as `TW_OWNER_CHAT_ID`.

The [seven-day live run (#9)](https://github.com/canhta/threadwalker/issues/9) needs your approval
before `dry_run` becomes `false`. The [second agent (#12)](https://github.com/canhta/threadwalker/issues/12)
needs another disposable account and saved login. Keep these issues open until their measured
acceptance criteria hold.

[`deploy/threadwalker.service`](../deploy/threadwalker.service) currently runs only `check`.
It expects environment variables in `/etc/threadwalker/threadwalker.env`; the root `.env`
is local setup storage. The unit does not yet start an agent loop.
