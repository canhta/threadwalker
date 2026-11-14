# Setup

Use the Go version in [go.mod](../go.mod) and run commands from the repository root.

## Run locally without credentials

```sh
go vet ./... && go test ./...
go run ./cmd/threadz run --once --offline
```

Build with `go build -o threadz ./cmd/threadz`; the commands below use that binary.
`--help` prints usage, `version` the build version. Exit codes: 0 success, 1 runtime failure, 2
usage error. Events go to stdout; diagnostics and usage errors go to stderr.

The smoke run uses a temporary SQLite DB and synthetic services, reads no keys and cannot publish.
A two-agent, 48-hour logical-time soak with one restart (use a fresh evidence directory; drop
`--soak-agents 2` for one agent):

```sh
threadz eval soak --soak-hours 48 --soak-agents 2 --evidence /tmp/threadz-isolation
```

## Credentials and models

Keep `.env` at the repository root (Git ignores it):

```sh
test -f .env || cp .env.example .env
chmod 600 .env
```

Fill the variables your integrations use, quoting values that contain shell characters.

| Variable | Used for |
|---|---|
| `LLM_API_KEY` | The selected provider |
| `SECONDARY_LLM_API_KEY` | A second provider, if needed |
| `OPENAI_API_KEY` | The viral template's OpenAI blocks (`llm`, `writer`, `critic`) and its `web_search` |
| `THREADS_ACCESS_TOKEN`, `THREADS_USER_ID` | Threads API (`threads.backend: api`): the long-lived token and the numeric account id |
| `THREADS_APP_ID`, `THREADS_APP_SECRET` | `run --api-listen`: the Meta app id and secret, for the OAuth code exchange and the webhook signature |
| `THREADS_WEBHOOK_VERIFY_TOKEN` | `run --api-listen`: the token Meta echoes in the webhook handshake |
| `THREADZ_API_URL` | `run --api-listen`: the public https origin the Meta app points at; `--api-url` overrides it |
| `THREADZ_SECRET_KEY` | `threads.backend: api`: base64 of 32 bytes (`openssl rand -base64 32`); seals the refreshed token in the database |
| `TELEGRAM_BOT_TOKEN` | Telegram |
| `TELEGRAM_OWNER_CHAT_ID` | Owner's numeric chat ID |

The template's inline `llm` block sets `protocol` (`openai` for Chat Completions compatible
endpoints, `anthropic` for native Messages), `base_url`, `model`, `api_key_env` and per-million-token
input and output prices; prices give local cost estimates only. To compare model profiles with
`threadz eval`, copy the [model catalog](evals.md#model-catalog); templates never reference it.

At startup the binary loads `./.env` (or `THREADZ_ENV_FILE`) without overriding variables that
are already set. To inject it yourself:

```sh
set -a
. ./.env
set +a
```

## Agent templates

`templates/` holds every template; the shipped one is the [viral](../templates/viral.agent.yaml) agent.
Files named `*.agent.yaml` are templates. `check` and `run` read `templates/`; `--agents` (or `-a`)
picks another directory.

Set each agent's goal, persona, limits and LLM, and keep `dry_run: true`. Agents need distinct IDs,
`threads.username` values and browser profile paths. Replace the synthetic usernames before using a
real account.

## Dreaming, trends and web search

Per-agent template fields:

```yaml
limits:
  max_usd_per_day: 2.00
  max_usd_per_dream: 0.30   # dream spend is outside max_usd_per_day
  max_steps_per_dream: 20
schedule:
  timezone: Asia/Ho_Chi_Minh
  dream_at: "23:30"          # once per local day; a missed one runs once on next start
trends:
  sources:                   # wake's fetch_trends; omit for Hacker News + Product Hunt + Google Trends VN
    - google_trends
    - google_news_vn
  include_keywords: ["AI", "LLM", "agent", "benchmark"]
  avoid_keywords: ["crypto", "airdrop"]
web_search:                  # optional; reuses the template's OpenAI key
  enabled: true
  model: gpt-6-luna
  max_results: 5
  max_output_tokens: 512
```

The dream is read-only: `post`, `reply`, owner messages and schedule changes are refused with
`phase_read_only`. Omit `schedule.dream_at` to turn it off.

`trends.sources` accepts `google_news_vn`, `google_news_global`, `google_news_topics`, `vnexpress`,
`genk`, `tinhte`, `vietnamnet`, `the_verge`, `techcrunch`, `arstechnica`, `hacker_news`,
`product_hunt`, `google_trends` and `github_trending`; none needs credentials. `web_search` makes one
bounded Responses web-search call per call, at most 3 per normal wake, with the template's OpenAI key.

## Check and run

`check` validates a directory without external calls:

```sh
threadz check --agents /path/to/templates
threadz run --agents /path/to/templates --socket data/admin.sock
```

`--agents` and `--db` default to `templates/` and `data/threadz.db`;
`THREADZ_AGENTS` and `THREADZ_DB` override the defaults and a flag overrides both.
`run` may call the selected LLM and trend sources. `--once` runs one due wake, `--offline` forces fake
transports and disables Telegram (explicit templates still require dry run). Startup never replays
interrupted actions.

## Deploy to a Linux host

A `v*` tag builds a bundle with `.github/workflows/release.yml`: the `linux/amd64` binary, the
prepared viral template, the systemd units, the AppArmor profile and a detached ed25519 signature
of the tarball. The host needs no Go or Node. Install the first release by hand; later ones use
`threadz update`.

```sh
curl -fsSL https://github.com/canhta/threadz/releases/download/v<version>/threadz_<version>_linux_amd64.tar.gz \
  -o /tmp/threadz.tgz
mkdir -p /tmp/threadz && tar -xzf /tmp/threadz.tgz -C /tmp/threadz
sudo install -o root -g root -m 0755 /tmp/threadz/threadz /opt/threadz/threadz
sudo install -o root -g root -m 0644 /tmp/threadz/threadz*.{service,timer,path} /etc/systemd/system/
sudo install -o root -g root -m 0644 /tmp/threadz/apparmor-chrome /etc/apparmor.d/chrome
sudo apparmor_parser -r /etc/apparmor.d/chrome
```

Then install the agent template (already prepared for `/opt/threadz`) and the env file:

```sh
sudo install -o root -g threadz -m 0640 /tmp/threadz/viral.agent.yaml \
  /opt/threadz/agents/viral.agent.yaml
scp .env <host>:/tmp/threadz.env
ssh <host> sudo install -o root -g root -m 0600 /tmp/threadz.env \
  /etc/threadz/threadz.env
```

The env file holds only the LLM key (`OPENAI_API_KEY` for the viral template), `TELEGRAM_BOT_TOKEN` and `TELEGRAM_OWNER_CHAT_ID`. The
service runs as `threadz` with `/opt/threadz/{data,profiles,bin}`; install the pinned
Chrome and agent-browser there. [deploy/threadz.service](../deploy/threadz.service) caps
memory at `MemoryMax=2400M` (3.6 GiB host).

Log in to Threads with cookies: sign in on threads.com in your own browser, export the cookies with an
extension such as Cookie-Editor (Export, JSON), and send them to the bot as `/cookie <paste>`, or run
`threadz ctl threads cookies --agent ID < cookies.json` on the host (the value comes from stdin, never
from arguments). The bot deletes your message right away. The same applies whenever a logged-out notice
arrives ([threads.md](threads.md#cookie-login)); no extra host packages are needed.

The dashboard is a separate process on loopback:
`threadz dashboard --agents /opt/threadz/agents --db /opt/threadz/data/threadz.db`.
The service accepts only a numeric loopback `--listen` address.

### Updates

`sudo threadz update` installs the latest release from GitHub. It:

1. verifies the tarball's sha256 and ed25519 signature against the public key embedded in the binary
   (the release workflow signs with the `RELEASE_SIGNING_KEY` repository secret);
2. runs the new binary's `check` against the live agents directory and database;
3. waits up to 10 minutes for a running wake (an open wake row younger than 30 minutes);
4. renames the new binary over the old one (kept as `<binary>.prev`) and restarts
   `threadz.service`;
5. restores `.prev` if the service is not active and `ctl doctor` has not passed within 2 minutes.

Each step is an `update.step` event. The owner gets "updated vA→vB" or "update to vB failed, rolled
back" on Telegram. The update refuses a downgrade, a major-version jump and a release whose notes
have a line starting with `Manual step:` (for example a schema change that needs a fresh database;
`check` also fails on a schema `user_version` mismatch); install those by hand. Flags: `--check`
only reports, `--version vX.Y.Z` pins a tag, `--channel stable`.

Automatic updates are off until `/etc/threadz/threadz.env` sets:

```sh
THREADZ_UPDATE_AUTO=true
THREADZ_UPDATE_WINDOW=2-5   # optional: local hours [2,5) in which updates may run
```

Then run `sudo systemctl enable --now threadz-update.timer threadz-update.path`. The timer
runs `threadz update --scheduled` hourly with jitter, as root, inside the window. An update on
demand ignores both: Telegram `/update` and `ctl update` as the service user write
`/run/threadz/update-request`, which `threadz-update.path` turns into a root update run
and a Telegram reply. As root, `ctl update` is `threadz update`. Unit and template changes ship
in the bundle and are installed by hand.

## Threads API, feed browser and Telegram

The default backend is `stub`, which requires dry run. `backend: api` uses the official Threads
API with the long-lived token and `THREADS_USER_ID`. The token is seeded from
`THREADS_ACCESS_TOKEN`, which becomes optional once OAuth has stored a sealed one.

Required scopes: `threads_basic`, `threads_content_publish`, `threads_manage_insights`,
`threads_manage_replies` and `threads_read_replies`. `search` also needs `threads_keyword_search`,
and mentions need `threads_manage_mentions`.

Two reads depend on Meta App Review rather than on the scope alone. Without review, `search` covers
only the account's own posts, and `profile_posts` serves only a few official Meta accounts. Both
return a `limited_access` reason, and the agent works from `fetch_trends`, `web_search` and
`read_feed` instead. Apply for `threads_keyword_search` and `threads_profile_discovery` at advanced
access to open public search and profile reads.

With `THREADZ_SECRET_KEY` set, the harness refreshes the token a week before expiry and stores it
sealed in the database. A failed refresh queues an owner notice.

### OAuth and webhooks

`run --api-listen 127.0.0.1:8090` serves the Meta app's endpoints. They need a public https origin
of your own; this guide writes it `api.example.com`, with the dashboard on a second hostname
`dash.example.com`. Route the API hostname to that port with an ingress entry in
`/etc/cloudflared/config.yml` and `cloudflared tunnel route dns <tunnel> api.example.com`. Pass the
origin as `--api-url https://api.example.com`, or set `THREADZ_API_URL` in the env file; there
is no default.

Meta sends `replies` and `mentions` webhooks only when the app is Live (or has advanced access) and
the Threads account is public. Otherwise the webhook verifies but no event arrives, and the agent
sees replies and mentions only when it polls.

In the Meta app's Threads use case, set:

| Setting | URL |
|---|---|
| Redirect callback | `https://api.example.com/threads/oauth/callback` |
| Webhook callback | `https://api.example.com/threads/webhooks` (verify token: `THREADS_WEBHOOK_VERIFY_TOKEN`; subscribe to `replies` and `mentions`) |
| Privacy policy | `https://api.example.com/privacy` |
| Data deletion | `https://api.example.com/data-deletion` |

`threadz ctl threads connect --agent ID` sends the owner a one-time link (valid 15 minutes) on
Telegram. It signs in with all eight scopes and stores the long-lived token, sealed, in place of the
environment seed.

### Feed browser

The API cannot read the home feed, so `api` also needs these `threads` fields: `base_url` (an HTTPS
`threads.com` or `threads.net` origin), `executable_path` (Chrome), a logged-in per-agent
`profile_dir`, and `agent_browser_path`, the absolute path to the pinned **agent-browser 0.42.0**
CLI (`npm install agent-browser@0.42.0`; other versions are rejected). `no_sandbox` defaults to
false. The browser starts only when a wake reads the feed.

```yaml
threads:
  backend: api
  base_url: https://www.threads.com
  executable_path: /path/to/chrome
  agent_browser_path: /path/to/agent-browser
  profile_dir: /path/to/agent-profile
```

With `dry_run: false` every write passes the guard's audit row, reserved intent and limits, and the
API's publishing quota. A publish with an unknown outcome is recorded as unknown and never retried.
Feed posts carry no post id, so the agent replies only to posts returned by search, mentions or
replies. Captcha and rate-limit banners are not handled, and the feed profile's account survival is
not measured.

Opt-in live API check (`/me`, insights, `followers_count`, publishing limit; never publishes):

```sh
THREADZ_LIVE_THREADS=1 THREADS_ACCESS_TOKEN=... THREADS_USER_ID=... \
  go test -run TestLiveSmokeReadOnly -v ./internal/threads/
```

Opt-in browser check (needs the pinned binary and Chrome; opens public Threads logged out and expects the refusal):

```sh
AGENT_BROWSER_BIN=/path/to/agent-browser CHROME_BIN=/path/to/chrome \
  go test -tags agent_browser_integration -run TestAgentBrowserLiveLoggedOutFailsClosed ./internal/threads/
```

### Telegram

Set `telegram.enabled: true`, `bot_token_env` and `owner_chat_id_env` (names of the variables
holding the bot token and the owner's numeric chat ID), then message the bot first. The daily report
arrives at `report_hour` (default 21) in `telegram.timezone` (IANA, default `Asia/Ho_Chi_Minh`),
which is also the zone of every displayed time. Every `getUpdates` sends `allowed_updates: ["message"]`, because
Telegram persists the filter per bot.

The bot registers its menu with `setMyCommands` at start: `/status`, `/report`, `/pause`, `/resume`,
`/kill`, `/dnc`, `/cookie`, `/update`, `/help` and `/start`. With several agents the
per-agent commands take the agent ID first: `/status <agent_id>`, `/dnc <agent_id> <username>`.
`/help` lists them and `/start` adds one line on what the chat receives. Kill is terminal. Reply to a
delivered question to answer it. A menu registration failure is logged as `menu_failed`.

All bot text is English plain text built in `internal/telegram/copy.go`; there is no per-template
copy. Free owner text that is neither a command nor a reply is stored as a note for the next wake and
acknowledged once.

## Operator commands

The service binds `/run/threadz/admin.sock` (mode 0600, service user; the directory is mode
0700 and created by the unit). Reach it over SSH, as the service user; the command tables and rules
are in [operations.md](operations.md):

```sh
ssh vps threadz ctl status
ssh vps threadz ctl wake --agent viral --phase dream --dry-run
ssh vps threadz ctl wake-show 42 --agent viral
```

`ctl` resolves templates, database and env file like every command, so on the documented layout
export:

```sh
export THREADZ_AGENTS=/opt/threadz/agents THREADZ_DB=/opt/threadz/data/threadz.db \
  THREADZ_ENV_FILE=/etc/threadz/threadz.env
```

On a laptop pass the same writable `--socket` to `run` and `ctl`.

## Dashboard, logs and evals

```sh
go run ./cmd/threadz dashboard --agents /path/to/templates --db data/threadz.db
```

Open `http://127.0.0.1:8080/` (`--listen` selects another loopback port). The DB must exist. It
needs no credentials, opens SQLite read-only and accepts only `?agent=<id>` and `?range=7|14|30`.

Behind the Cloudflare tunnel the limiter keys visitors by `CF-Connecting-IP`; from any other peer
that header is ignored. `-rate`, `-burst` and `-max-concurrent` tune the limits.

Logs are JSON on stdout; `--log-level debug` and `--log-format text` work on `check`, `run` and
`dashboard`. Debug output still excludes prompts, credentials, private content and raw tool
payloads. Filter a saved trace:

```sh
jq -c 'select(.agent_id == "viral" and .wake_id == 42)' events.jsonl
```

[Evals](evals.md) covers the eval suites.
