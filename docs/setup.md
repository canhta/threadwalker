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
`anthropic` entries are valid configuration that `check` validates; the native
Anthropic runtime is [issue #15](https://github.com/canhta/threadwalker/issues/15),
so this configuration does not yet make Anthropic calls.

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

Expect `check passed`. This validates templates and SQLite migrations without
calling external services. Telegram entries are currently required even in dry
run.

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
