# Data: which tables exist, how long is each kept, and what is stored about a Threads user?

The whole schema is [`internal/store/schema.sql`](../internal/store/schema.sql); read columns, checks and indexes there. It is applied to an empty database by `store.Open` and stamped with `PRAGMA user_version` (currently 13, the `schemaVersion` constant in `internal/store/store.go`). A database with any other version is refused with a message to start a fresh file; there is no in-place upgrade. A schema change edits `schema.sql` and raises the version constant. Every table carries `agent_id`, even while only one agent runs.

## Tables

| Table | Holds | Written by |
|---|---|---|
| `agents` | State (`running`, `paused`, `killed`) and `next_wake_at` | Scheduler, owner controls |
| `wakes` | One row per agent session, metric scope or phase run: `kind` (`agent`, `metrics`, `dream`), `phase_key`, steps, tokens, cost, `end_reason` | Runtime |
| `wake_transcripts` | Redacted, bounded record of one wake for the operator (entries `context`, `assistant`, `tool_result`, `usage`, `end`, `truncated`) | Runtime |
| `tool_calls` | Audit log: tool, argument size, `result_json` outcome stub, `blocked_by` guard reason | Guard |
| `tool_call_details` | Per audited call: redacted arguments (strings cut to 2 KiB) and the detail it ended with: refusal detail, ref resolution (the ref given and what it resolved to), every writer and critic draft, read list lengths, browser stage and snapshot path, action id | Guard |
| `actions` | Published or dry-run posts and replies: text, target, `dry_run`, `thread_url`, `media_id`, `hook`, `rationale`, `trend` (the anchor item's URL or title), `answers_note`, `critic_json`, `browser` flag, posting context (target score parts, `followers_at_post`, `parent_replies`, `parent_age_minutes`) | Guard intent, dispatcher |
| `lessons` | One sentence of at most 240 characters on how to write, with `kind`, `polarity`, evidence (cited action ids), `confirmed_at`, and a retirement (`retired_at`, `retired_by` of `dream`, `owner` or `expiry`, reason) ([learning.md](learning.md#lessons)) | Dream, owner `/forget` |
| `action_lessons` | The lessons an action said it applied (`lesson_ids`) | Dispatcher |
| `action_parts` | Later parts (2 to 5) of a series: text, `thread_url`, `media_id`. A part with an empty `thread_url` was never published | Dispatcher |
| `metric_jobs` | Durable +1h/+6h/+24h targets: lease, fence, attempts, `completed_at`, `reason` | Metrics |
| `metrics` | Repeated measurements per action (views, likes, replies, reposts, quotes, shares, followers, `author_replied`; the last four nullable) | Metrics |
| `account_followers` | Latest own follower count per agent, read from the own profile page at the start of each agent wake; a metrics batch stamps its samples with this row | `read_own_metrics` |
| `contacts` | Username, `reply_count_7d`, `do_not_contact`, `reason`. Rate limiting only; no description of the person | Guard |
| `ledger_phases` | The only spend record: per agent, local day (date in `schedule.timezone`) and phase (`agent`, `dream`, `critic`), token and USD totals. A daily total is a SUM over phases | LLM client |
| `trend_stories` | Ranked external stories from `fetch_trends` | Trends |
| `seen_posts` | Every post a read returned, under a durable per-agent id (shown to the model as `p<id>`): canonical permalink (unique per agent), API `media_id` (empty for a post only a browser read showed), `author`, `text` (at most 500 characters), `likes`, `replies` and `age_minutes` at `read_at` (null when the page did not show them), `read_at`. A later read of the same permalink keeps the id, fills a missing `media_id` and refreshes the figures; ids are never reused. The reply tool resolves its `target` here, and the open reply candidates are read from here | Dispatcher, guard |
| `refused_tools` | Read tools and `find_posts` sources (`find_posts:search`, `find_posts:profile`) that returned `limited_access`, hidden for 24 hours | Dispatcher |
| `owner_msgs` | Telegram exchange and outbox (`direction`, `delivery`, `event_key`); an inbound note stays open until an action names it | Telegram |
| `owner_poll` | Poll offset per agent and bot | Telegram |
| `secrets` | AES-256-GCM sealed Threads token and expiry ([threads.md](threads.md)) | Token refresh |
| `webhook_events` | Webhook field, object id and time only | `internal/api` |

## Retention

`store.Prune` applies one table to every agent. The scheduler runs it at startup and once per local day, between wakes, and logs deleted rows per table as `retention.pruned`. It is idempotent. After a prune that frees pages it runs `PRAGMA incremental_vacuum`; `store.Open` switches a database created without incremental auto_vacuum by a `VACUUM`, which cannot run inside a migration transaction. `ctl prune` runs it now.

| Table | Kept |
|---|---|
| `seen_posts` | 7 days after the last read that showed an age |
| `trend_stories` | 7 days, and the newest 500 rows per agent |
| `wake_transcripts` | 8 days: tool results carry other people's text |
| `tool_call_details` | 8 days, like `wake_transcripts`: arguments may hold the same outside text |
| `tool_calls` | 90 days |
| `wakes` | 90 days after they ended; a running wake is never pruned |
| `owner_msgs` | 90 days |
| `webhook_events` | 30 days |
| `contacts` | Rows with `do_not_contact = 0` until no reply to that username falls in the last 7 days; `do_not_contact` rows forever |

`actions`, `metrics`, completed `metric_jobs` (an episode is a completed job joined to its metrics) and `ledger_phases` are the experiment's record and are kept. A dashboard panel never shows more days than its table keeps: the trending panel shows at most 7 days whatever range is selected.

## What is stored about a Threads user

| Data | Stored | Not stored |
|---|---|---|
| Reply targets | Normalized username and a 7-day reply counter; permanent `do_not_contact` flag with a fixed reason code | Description of the person, inferred traits |
| Inbound replies and mentions | Webhook: field and media id only. Stop-request matching: transient text, not saved. The conversation inbox and every read store the post in `seen_posts` (below) | Any other webhook field |
| Posts read from Threads (`seen_posts`) | Per post a read returned, including the inbox: permalink, API id, author username, up to 500 characters of its text, likes, replies and age at read time; 7 days after the last read that showed an age | Profile fields, follower counts, anything after 7 days |
| Reply target scoring | Numeric score parts (age, replies, likes, likes per hour, window flags) on the reply action. Author size tier is computed, never stored | Follower counts of strangers |
| Wake transcripts | Redacted, bounded tool results that may hold outside text; 8 days ([operations.md](operations.md)) | Secrets, unredacted payloads |
| Call details | Redacted arguments of a call, which name a target's username and post URL, and the ref given beside the item it resolved to (a post URL or headline); 8 days. Browser snapshots on disk hold the page text the failed step read; 20 per agent, 8 days ([operations.md](operations.md)) | Secrets, cookies, profile fields |

Base rule: Meta Platform Terms 3.a.v ([guards.md](guards.md)).
