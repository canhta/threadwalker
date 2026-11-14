# Operations: how do I run, inspect and control a live agent?

Install, credentials, deploy, self-update steps, Cloudflare routing and the Meta app settings are procedures in [setup.md](setup.md). This doc holds the command and behaviour tables for a running service.

## Control surfaces

| Surface | Reaches | Auth |
|---|---|---|
| `threadz ctl` | The running process over a Unix socket | File mode 0600 of the service user |
| Telegram | The owner chat | Configured owner chat only |
| Dashboard | A read-only page over the database | None |
| Journald, `wake-show`, `wake-diagnose` | Logs, stored transcripts and the audit of one wake | Host access |

Nothing is exposed over the network from the harness except the loopback API behind the Cloudflare tunnel ([threads.md](threads.md)) and the dashboard listener. Remote use is `ssh <host> threadz ctl ...`.

## Admin socket and ctl

The service listens on a Unix socket (`--socket`, `THREADZ_ADMIN_SOCKET`, default `/run/threadz/admin.sock`), mode 0600, owned by the service user, HTTP on that socket only. `run` binds it unless `--once` is set (with `--offline` only when `--socket` is given). A socket file left by a dead process is replaced; a live one or any other file stops the start. A manual trigger shares the scheduler's serial loop, guards, limits and spend caps and never opens a second writer on the database. Every command is audited as `admin.command` with the command in `operation` and `success`, `busy`, `failed` or `unknown_command` in `outcome`/`reason`.

| Command | Runs | Effect |
|---|---|---|
| `status [--agent]` | live process | Per agent: state, dry-run mode, next work and its kind (`agent`, `metrics`, `dream`), today's spend against the daily and dream caps, last wake and end reason, pending metric targets, live health rows |
| `wake --agent --phase agent\|dream [--dry-run]` | live process, scheduler loop | One wake now. The agent must be `running`; the schedule is skipped, every guard, limit and cap applies. `--dry-run` makes this wake's writes dry-run writes and lifts nothing |
| `pause`, `resume`, `kill` (`--agent`) | live process | The path Telegram uses. Kill is terminal |
| `metrics run --agent` | live process, scheduler loop | Measures targets due now (a window is never measured early) |
| `trends refresh --agent` | live process, scheduler loop | Replaces the trend snapshot as `fetch_trends` does |
| `prune` | live process, scheduler loop | Applies the retention table ([data.md](data.md)) |
| `threads connect --agent` | live process | Sends the owner a one-time authorization link over Telegram ([threads.md](threads.md)) |
| `threads cookies --agent` | live process | Imports a cookie export (JSON, or a `Cookie:` header) read from stdin into the agent's browser profile, verifies the feed and resumes the agent, then prints one line. The same import as Telegram `/cookie` ([threads.md](threads.md#cookie-login)); `threadz ctl threads cookies --agent ID < cookies.json` |
| `context --agent [--phase]` | live process | The exact messages the next wake of that phase sends, redacted, no model call. |
| `wakes --agent [--since] [--limit]` | database, read-only | Wake list |
| `wake-show <id> --agent` | database, read-only | One wake's stored transcript with its guard decisions |
| `wake-diagnose <id> --agent` | database, read-only | One wake as a funnel; see [Diagnose a wake](#diagnose-a-wake) |
| `lessons --agent` | database, read-only | Active and retired lessons with their use |
| `lessons forget <id> --agent` | admin socket | Retire a lesson for good; the agent will not add it again |
| `wake-export <id> --agent [--keep-text] [--briefs]` | database, read-only | The wake's reads, calls and guard verdicts as a `replay` eval suite on stdout ([evals.md](evals.md#replay-cases)); the text of other people's posts is blanked unless `--keep-text`; `--briefs` writes the writer and critic briefs for a live replay ([evals.md](evals.md#replaying-briefs-through-the-current-writer-and-critic-live)) |
| `logs [--agent] [--wake] [--event] [--follow] [--source journal\|db]` | journald, or `db` for one wake's transcript | Structured log filtered by agent, wake or event; `--unit` selects another journald unit |
| `draft --agent --input FILE` | host | Writer and critic on fixed items, nothing published ([content.md](content.md)) |
| `capture-api --agent [--out DIR]` | host, read-only Threads calls | Saves redacted responses (me, an own post and reply with their insights, the reply's `/replies`, mentions) to `--out` (default `internal/threads/testdata/live/`) for the API contract tests. Other usernames, all free text, query strings and tokens are removed; ids become synthetic of the same length; field names, value types and timestamp formats are kept. Uses the token from the environment, never writes it |
| `doctor` | host | Exits non-zero when any check fails |
| `update` | host | Installs the latest release; as root it is `threadz update`, else it asks the root updater |

Rules: every command takes `--json`. Commands that change anything need an explicit `--agent`; there is no default target. At most one operator job is queued or running; a second trigger is refused as busy, not queued. A job waits for the scheduler loop, so it starts after a running wake. Exit status: 0 success, 1 failure or failed check, 2 usage error. On a host, `ctl` resolves templates, database and env file like every command (`THREADZ_AGENTS`, `THREADZ_DB`, `THREADZ_ENV_FILE`).

## Health rows

Status runs the rows marked live for each agent; `doctor` runs all of them. One table in `cmd/threadz/health.go`.

| Row | Checks |
|---|---|
| `config` | Templates load and validate |
| `env_keys` | Variable names present, never values |
| `db_schema` | Stored schema version equals the one this build requires |
| `disk` | At least 512 MiB free beside the database |
| `retention` | No row past its limit plus the day between prunes |
| `browser:<id>` | Executable and profile directory the feed read uses |
| `telegram:<id>` | `getMe`, when the integration is enabled |

## Wake transcripts and logs

| Item | Fact |
|---|---|
| Transcript | Each wake stores, redacted and bounded, in `wake_transcripts`: the hash of the rendered messages, the data message and the system prompt text once per run (`context`); each injected nudge or stop-check message (`user`); each model turn's text and tool calls (`assistant`); each tool result cut to 64 KiB with a `truncated` flag, so `fetch_trends` and `find_posts` results stay whole (outside data keeps the `untrusted` marker); each request's tokens and USD (`usage`); the end reason and steps (`end`) |
| Bounds | At most 400 entries and 2 MiB per wake. The entry that crosses a bound becomes one `truncated` marker and later entries are dropped, except `end` |
| Guard decisions | Not copied; `wake-show` reads them from `tool_calls`, where `blocked_by` holds the reason |
| Call arguments and detail | `tool_call_details` holds each call's arguments (redacted, every string cut to 2 KiB; a call whose arguments held a secret keeps only its key names) and, once the call ended, its detail: the refusal's detail, how a `trend` or `target` resolved (the ref given and the item it resolved to, or the number of refs noted this wake when it resolved to none), every draft the writer and critic produced (source, first line, length in runes, scores, problems), the length of each list a read returned, the failing browser stage and the saved snapshot path, and the action id. 8 days ([data.md](data.md)) |
| Retention | 8 days, because transcripts hold the same outside text as Threads observations |
| Log stream | One `log/slog` JSON stream with `agent_id`, `wake_id`, `phase`, `request` and `tool_call_id` where they apply. Event names are constants in `internal/logging` |
| Browser events | `browser.launch` and `browser.close`, each with `wake_id`, `phase`, `outcome` and `duration_ms` |
| Browser failure evidence | A browser step that fails keeps the stage that failed (`navigate`, `read_page`, `composer_missing`, `type`, `submit`, `acknowledgement`) beside the unchanged reason code, and the page snapshot it last read (accessibility structure and text, untrusted page data, no cookies), redacted, in `evidence/<agent>/` beside the database with mode 0600. It is logged as `browser.evidence.saved` with the path. An agent keeps its newest 20 snapshots, none older than the transcripts' 8 days; pruning runs when a snapshot is saved |
| Journald | `deploy/threadz.service` raises the rate limit so a wake's records are not dropped |

Filtering a saved trace: [setup.md](setup.md).

## Diagnose a wake

`threadz ctl wake-diagnose <id> --agent ID` (add `--json` for the structured form) prints one wake from the audit rows alone, within 8 days of the wake. The wake ids are in `ctl wakes`.

| Part | Shows |
|---|---|
| Funnel | Calls, reads and how many failed, writes (posts and replies) with the blocked and failed counts, published actions |
| Every call, in order | Tool, outcome (`ok`, `blocked`, `failed`, `unfinished`) and reason code; for reads the length of each list returned |
| Every post and reply | Its arguments that matter: first line and length of the text, `hook`, `trend` or `target` (the refs as the model sent them), number of later parts |
| Ref resolution | The ref given and the item it resolved to; when it resolved to none, how many refs the wake issued |
| Editor drafts | Each draft in order: `agent` (the rough draft sent in), then each `writer` revision, with its length in runes, first line, critic scores and problems. A `text_length` refusal names the draft it measured by listing it as an `agent` draft; a writer text that is too long appears as a problem of its draft and ends as `critic_rejected` |
| Browser | The failing stage, the reason code and the path of the saved snapshot |
| Outcome | The action id, its kind, whether it is a dry run or failed, and its published URL |

A call older than the details' retention shows its outcome and guard reason without arguments. To keep a failed wake as a regression test, `wake-export` writes it as a replay case.

## Telegram

| Item | Fact |
|---|---|
| Agent to owner | When it meets something outside its permissions, is logged out or hits a captcha (the notice says how to send cookies with `/cookie`), has no Threads token (carries the OAuth link, once a day), or is near its budget limit; once per local day, in waking hours, when a running live agent has published nothing for longer than `kpi.max_hours_without_publish` plus 3 hours (the notice gives the hours without a publish and says to send `/status`); a dry-run agent never sends it, and a failed check is logged and does not stop the scheduler; plus the daily report |
| Owner commands | `/help`, `/start`, `/status`, `/report`, `/lessons`, `/forget <id>`, `/pause`, `/resume`, `/kill`, `/dnc <username>`, `/cookie <cookies>`, `/update`. With a shared bot, per-agent commands take the ID: `/pause <agent_id>`, `/dnc <agent_id> <username>`, `/status <agent_id>`, `/report <agent_id>`, `/lessons <agent_id>`, `/forget <agent_id> <id>`. Ambiguous or unknown agents fail safely. `/kill` is terminal; `/resume` cannot revive it |
| Replies | Replying to a delivered `ask_owner` question answers it. Free text that is neither a command nor a reply becomes an owner note for the next wake with one acknowledgement and no model call |
| Access | One long poller per bot; conflicting owner chats for a bot are rejected. Only the configured owner chat can command or answer |
| Persistence | Poll offsets, command receipts, question delivery IDs and reply correlation survive restarts |
| Bot avatar | Set by hand, the Bot API cannot set it: `@BotFather` `/setuserpic` with [`docs/images/brand/threadz-avatar-1024.png`](images/brand/threadz-avatar-1024.png) |
| Owner tools | `ask_owner` and `notify_owner` enqueue locally and return immediately. The agent's next wake sees only the latest ten answers, secrets redacted; raw updates and chat identifiers never enter its context |
| Menu | On start the bot replaces its default-scope menu with `status`, `report`, `lessons`, `forget`, `pause`, `resume`, `kill`, `dnc`, `cookie`, `update`, `help`, `start`, from the same table `/help` prints; a failure is logged as `menu_failed` and does not stop polling. Each long poll sends `allowed_updates: ["message"]` |
| Copy | English plain text (no `parse_mode`, so nothing is escaped) built from code-owned facts; the model never writes counts, limits or times. A message starts with a title line: `Status`, `Daily report, <date>`, `Notice`, `Question from the agent` or `Message from the agent`, then short `Label: value` lines. With a shared bot the title is prefixed with `persona.name`. Times are `HH:MM` in `telegram.timezone`, money is USD with two decimals. `ask_owner` and `notify_owner` text is the model's, asked for in English |
| Notices | Daily notices use the local day in `schedule.timezone` for action limits and the 80% and 100% budget thresholds, and give the reset time in the owner's zone; killed notices use the terminal state identity. The browser's `BrowserStopped` seam pauses the agent and dedupes its notice |
| Report | Once per owner-local date from `telegram.report_hour` (default 21) in `telegram.timezone`; `/report` sends the same text on demand. It covers the 24 hours up to now, so consecutive reports leave no gap: state and next run, posts and replies, the latest metric sample of each action measured in that window, the top post by views with its engagements and permalink (never its text), followers with the change since the last count before the window, spend of the local day (resets at 00:00 in `schedule.timezone`) against the cap with the wake and dream split, failed runs with their top 3 end reasons, and the top 5 blocked reasons (`tool_calls.blocked_by`). A missing metric or follower count says "not measured", never 0 |
| Outbox | Claims a message before HTTP and never retries a failed, in-flight or uncertain delivery after restart: a message may be lost, never duplicated. Static failure reasons are in `telegram.operation.finished`. Correlation uses local message row IDs and counters; credentials, chat IDs, bodies, usernames and raw HTTP errors are excluded |
| Storage | Delivery DDL in [`schema.sql`](../internal/store/schema.sql); code in [`internal/telegram`](../internal/telegram). Real-bot behaviour is not measured |

## Dashboard

`threadz dashboard` serves one server-rendered page on `127.0.0.1:8080` (`--listen`). Alongside `/`
it serves `/dashboard.css`, four Be Vietnam Pro woff2 fonts and the icons `/favicon.svg`,
`/favicon.ico` and `/apple-touch-icon.png`, all embedded in the binary.

A request runs no LLM call, agent action, schema change or agent initialization.

| Rule | Behaviour |
|---|---|
| Methods | GET and HEAD. `/unlock` also accepts POST |
| Unknown path | 404, before the database is opened |
| `/unlock` | The authentication form. Content mode only; never reads the database |
| `?agent`, `?range` | The only parameters read. Defaults: first agent, 7 days. Ranges: 7, 14, 30, 90 |
| Bad `agent` or `range` | 400, before any database read |
| Any other parameter | Ignored, so `fbclid` and `utm_*` on a shared link still reach the page. The cache key is the agent and range alone, so they neither read nor fill a second entry |
| Query over 1024 bytes | Refused |

The page reads top to bottom in priority order. First a hero: views, followers and engagement,
each with its change on the previous period, over trend charts that go weekly beyond 30 days. Then
the latest 6 posts and replies, with the 5 best performers beside them. Learning, held-back
drafts, cost, runs, tools and trends follow as collapsed rows.

No list is unbounded. At most the 42 latest posts are kept; the rest sit in an "Earlier" fold.

Sections run in this order; missing values show `—`.

| Section | Shows |
|---|---|
| Top bar | Agent, status, test mode, last and next run in the owner's zone (`telegram.timezone`, else schedule zone, else UTC), range links |
| KPI cells | Posts, replies, followers with change, views with change against the previous period, spend today against the daily limit. Counts cover every real publication under the agent's own username, from a SQL aggregate, never truncated |
| Posts | Own posts and replies ranked by engagement: text linked to the post, age, best/worst mark, Deleted label, views, likes, replies, reposts, engagement rate; blocked posts counted by rule. |
| Growth | Inline-SVG charts of views, engagement rate and followers per day; changes against the previous period; followers per post and per 1,000 views |
| Cost | Stacked daily spend by runs and planning against the daily limit |
| Unit cost | Total spent and cost per post, follower and 1,000 views; `$0.04`, `<$0.01` below a cent |
| Tools | Ok, failed and blocked counts per tool |
| Trends | Public headlines from `trend_stories`, ranked by recent days kept, with a source tag |
| Runs | Latest five of at most twenty: time, type, steps, cost, plain stop reason; a run with failed or blocked steps expands to list them |

| Rule | Fact |
|---|---|
| Blocked vs failed | Blocked = a tool call with a non-empty `blocked_by`, or a failed call with a `lesson_*` reason. Failed = the tool ran and errored; its `result_json` carries a `reason` (backend reason, lesson code, or `operation_failed`); unknown codes show as "failed". Outcomes are derived in Go; no query uses SQLite JSON functions |
| Layout | Phones (320 to 420 px) first, no horizontal overflow. One spacing scale (4, 8, 12, 16, 24, 32 px) and one type scale (12, 14, 16, 24 px). Charts are inline SVG from the standard library with a caption and a visually hidden text table; no JavaScript or chart library in charts; labels stay at least 12 px at 320 px |
| Times | Clocks and dates for moments (posts, runs, stats read) are `<time datetime>` and show in the viewer's browser zone through one same-origin script (`script-src 'self'`); the configured zone is the no-script fallback, and chart day buckets and labels stay in it |
| Public view | Post text, whitespace collapsed and cut to 220 characters, linked to the post on Threads. Excludes the reasons behind a post, prompts, raw tool arguments and results, free-text errors, foreign usernames and arbitrary URLs. A link appears only for a confirmed own HTTP(S) publication passing the canonical path check in [`internal/dashboard/privacy.go`](../internal/dashboard/privacy.go); model in [`model.go`](../internal/dashboard/model.go). Trend headlines are public |
| Data access | `store.DashboardSnapshot` returns one agent's rows in a single read-only transaction with a 3 s timeout; the package reads no database itself. Times are compared as instants. Queries use parameters and bounded row limits except the posted and draft totals. Public responses: `Cache-Control: public, max-age=10`, `ETag`, 304 on `If-None-Match` |
| Abuse limits | Response cache per path and canonical query, one render per 10 s through a singleflight, stale copy for up to 2 minutes after a database error, 256 entries. Per-client token bucket 40 requests/s, burst 80, at most 4096 buckets, 429 with `Retry-After`; failed token attempts 5 per minute. Render semaphore of 16 returns 503 with `Retry-After`; 64 connections; headers 16 KiB; bounded bodies and timeouts |
| Client address | `internal/ratelimit` holds the one limiter and rule both servers use: `CF-Connecting-IP` when the peer is loopback (the Cloudflare tunnel), else the peer address |

## Self-update

`threadz update` (root) installs a GitHub release only after the tarball's sha256 and detached ed25519 signature verify against `update.ReleasePublicKey`. It refuses a downgrade, a major-version jump and release notes with a `Manual step:` line, and restores `.prev` when the restarted unit is not active and `ctl doctor` does not pass within 2 minutes. `THREADZ_UPDATE_AUTO` (default false) and `THREADZ_UPDATE_WINDOW` (local hours `H-H`) gate only the timer; Telegram `/update` and `ctl update` write `/run/threadz/update-request` for a root path unit. Steps, flags and install: [setup.md](setup.md#updates).

## Manual deploy of v0.3.0

v0.3.0 changes the schema (v12 to v13) and the tool surface, so its release notes carry a `Manual step:` line and `threadz update` refuses it. Install by hand, in this order:

1. Stop both services: `systemctl stop threadz threadz-dashboard`. The dashboard runs the same binary on the same database.
2. Back up the database and the yaml: `cp /opt/threadz/data/threadz.db /opt/threadz/data/threadz.db.v12` and `cp /opt/threadz/agents/viral.agent.yaml /opt/threadz/agents/viral.agent.yaml.v12`.
3. Migrate the database (one transaction, refuses any schema other than v12; it only adds the `seen_posts` table): `sqlite3 -bail /opt/threadz/data/threadz.db < deploy/migrate-13.sql`
4. Patch the live template (rewrites the sentences that name a removed tool or argument, then replaces the whole `persona:` block with the repo template's, but only when the live block is exactly the v0.2.x repo persona (sha256); an edited persona is kept with a warning to merge by hand. Writes a `.bak`, prints each change, never touches `dry_run`; a second run changes nothing): `python3 deploy/patch-template-v13.py /opt/threadz/agents/viral.agent.yaml` (run it from the repo checkout: it reads `templates/viral.agent.yaml`)
5. Install the v0.3.0 binary over `/opt/threadz/threadz`.
6. Check with the service env: `set -a; . /etc/threadz/threadz.env; set +a; /opt/threadz/threadz check --agents /opt/threadz/agents --db /opt/threadz/data/threadz.db`
7. Start the services: `systemctl start threadz`, then `systemctl start threadz-dashboard`, then `threadz ctl doctor`.

To roll back, stop both services and undo the migration: `sqlite3 -bail /opt/threadz/data/threadz.db 'DROP TABLE seen_posts; PRAGMA user_version=12;'`. Then restore `viral.agent.yaml.v12` over the patched yaml, install the previous binary and start the services as in step 7.
