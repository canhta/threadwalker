# Data: which tables exist, how long is each kept, and what is stored about a Threads user?

The whole schema is [`internal/store/schema.sql`](../internal/store/schema.sql); read columns, checks and indexes there. It is applied to an empty database by `store.Open` and stamped with `PRAGMA user_version` (currently 10, the `schemaVersion` constant in `internal/store/store.go`). A database with any other version is refused with a message to start a fresh file; there is no in-place upgrade. A schema change edits `schema.sql` and raises the version constant. Every table carries `agent_id`, even while only one agent runs.

## Tables

| Table | Holds | Written by |
|---|---|---|
| `agents` | State (`running`, `paused`, `killed`) and `next_wake_at` | Scheduler, owner controls |
| `wakes` | One row per agent session, metric scope or phase run: `kind` (`agent`, `metrics`, `dream`, `study`), `phase_key`, steps, tokens, cost, `end_reason` | Runtime |
| `wake_transcripts` | Redacted, bounded record of one wake for the operator (entries `context`, `assistant`, `tool_result`, `usage`, `end`, `truncated`) | Runtime |
| `tool_calls` | Audit log: tool, argument size, `result_json` outcome stub, `blocked_by` guard reason | Guard |
| `tool_call_details` | Per audited call: redacted arguments (strings cut to 2 KiB) and the detail it ended with: refusal detail, ref resolution with the nearest noted refs, every writer and critic draft, read list lengths, browser stage and snapshot path, action id | Guard |
| `actions` | Published or dry-run posts and replies: text, target, hypotheses, `explore`, `dry_run`, `thread_url`, `media_id`, `hook`, `rationale`, `trend_ref`, `answers_note`, `critic_json`, `browser` flag, posting context (target score parts, `delay_minutes`, `topic_heat`, `followers_at_post`, `parent_replies`, `parent_age_minutes`) | Guard intent, dispatcher |
| `action_parts` | Later parts (2 to 5) of a series: text, `thread_url`, `media_id`. A part with an empty `thread_url` was never published | Dispatcher |
| `action_hypotheses` | The testing revision snapshotted per tagged action | Guard |
| `metric_jobs` | Durable +1h/+6h/+24h targets: lease, fence, attempts, `completed_at`, `reason` | Metrics |
| `metrics` | Repeated measurements per action (views, likes, replies, reposts, quotes, shares, followers, `author_replied`; the last four nullable) | Metrics |
| `account_followers` | Latest own follower count per agent | `read_own_metrics` |
| `outcome_models` | Every fit of the expected-value model: version, row count, coefficients | Metrics |
| `action_outcomes` | One code-owned outcome per measured action: rates, lifts, label, `model_version` | Metrics only, never a tool |
| `playbook` | Hypotheses: status, `revision`, `n`, `kind`, `knowledge_id` | Tools (text), code (status, counters) |
| `playbook_decisions` | Each decision with `n` and reason code | Gate |
| `playbook_outcomes` | Per action and hypothesis: counted or not, with reason | Gate |
| `playbook_initializations` | Durable marker that the seed playbook was initialized | Runtime |
| `strategy_revisions` | Strategy history with citations | `strategy_write` |
| `memory_index` | Live entries (`archived_at` null) loaded into every wake; archived entries kept, never loaded | `memory_add`, code |
| `memory_journal` | Every change to the index (`added`, `overflow`, `expired`, `superseded`, `restored`) | Code only |
| `notes` (+ `notes_fts`) | Topic notes; a topic is unique per agent; rows with an empty topic are not addressable by name | `note_write` |
| `actions_fts` | FTS5 over action text, kept by triggers | Store |
| `embeddings` | One float32 vector per embedded note or action, keyed by source, row id and text hash; deleted with its row or when text changes; holds no text | Embedder |
| `knowledge_candidates` | Study techniques, always `practitioner`, never part of the curated file | `knowledge_candidate` |
| `contacts` | Username, `reply_count_7d`, `do_not_contact`, `reason`. Rate limiting only; no description of the person | Guard |
| `ledger_phases` | The only spend record: per agent, UTC day and phase (`agent`, `study`, `dream`, `critic`), token and USD totals. A daily total is a SUM over phases | LLM client |
| `trend_stories` | Ranked external stories per phase (`agent`, `study`) | Trends |
| `threads_observations` | Bounded public Threads sampling for deterministic trend analysis | Study |
| `refused_tools` | Read tools that returned `limited_access`, hidden for 24 hours | Dispatcher |
| `owner_msgs` | Telegram exchange and outbox (`direction`, `delivery`, `event_key`); an inbound note stays open until an action names it | Telegram |
| `owner_poll` | Poll offset per agent and bot | Telegram |
| `secrets` | AES-256-GCM sealed Threads token and expiry ([threads.md](threads.md)) | Token refresh |
| `webhook_events` | Webhook field, object id and time only | `internal/api` |

## Retention

`store.Prune` applies one table to every agent. The scheduler runs it at startup and once per local day, between wakes, and logs deleted rows per table as `retention.pruned`. It is idempotent. After a prune that frees pages it runs `PRAGMA incremental_vacuum`; `store.Open` switches a database created without incremental auto_vacuum by a `VACUUM`, which cannot run inside a migration transaction. `ctl prune` runs it now.

| Table | Kept |
|---|---|
| `trend_stories` | 7 days, and the newest 500 rows per agent |
| `threads_observations` | 8 days: the brief's 7-day baseline plus one day. Other people's snippets live no longer than the brief that reads them |
| `wake_transcripts` | 8 days, like `threads_observations`: tool results carry the same other-people text |
| `tool_call_details` | 8 days, like `wake_transcripts`: arguments and nearest refs may hold the same outside text |
| `tool_calls` | 90 days |
| `wakes` | 90 days after they ended; a running wake is never pruned |
| `owner_msgs` | 90 days |
| `metric_jobs` | Completed jobs 30 days after completion; seeding covers only actions of the last 29 days, so a pruned job is never recreated |
| `webhook_events` | 30 days |
| `contacts` | Rows with `do_not_contact = 0` until no reply to that username falls in the last 7 days; `do_not_contact` rows forever |

`actions`, `metrics`, `ledger_phases`, `notes`, `knowledge_candidates` and the playbook tables are the experiment's record and are kept. A dashboard panel never shows more days than its table keeps: the trending panel shows at most 7 days whatever range is selected.

## What is stored about a Threads user

| Data | Stored | Not stored |
|---|---|---|
| Reply targets | Normalized username and a 7-day reply counter; permanent `do_not_contact` flag with a fixed reason code | Description of the person, inferred traits |
| Inbound replies and mentions | Webhook: field and media id only. Stop-request matching: transient text, not saved | Text, author, any other webhook field |
| Study observations | Canonical post URL, public author username, text hash, snippet of at most 280 runes, nullable visible counters, source keyword, observation time; 8 days | Profile fields, inferred traits; never rendered publicly |
| Reply target scoring | Numeric score parts (age, replies, likes, likes per hour, window flags) and `delay_minutes` on the reply action; `threads_observations` keep reply, like and repost counts when a sampled page showed them. Author size tier is computed, never stored | Follower counts of strangers |
| Wake transcripts | Redacted, bounded tool results that may hold outside text; 8 days ([operations.md](operations.md)) | Secrets, unredacted payloads |
| Call details | Redacted arguments of a call, which name a target's username and post URL, and the noted refs nearest a mistyped one (post URLs, headlines); 8 days. Browser snapshots on disk hold the page text the failed step read; 20 per agent, 8 days ([operations.md](operations.md)) | Secrets, cookies, profile fields |
| Memory and notes | Strategy and facts about the agent's own content | Details about a Threads user |

Base rule: Meta Platform Terms 3.a.v ([guards.md](guards.md)).
