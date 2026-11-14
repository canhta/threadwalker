# Learning: how are outcomes measured, and what does the agent learn from them?

Code measures outcomes and ranks them; the agent never counts its own results. Measurement that exists within hours reaches the agent as episodes. One daily pass, the dream, turns episodes into lessons. Lessons are the only text the agent learns and keeps; the template and system prompt are immutable to it.

## Metric jobs

| Rule | Fact |
|---|---|
| Seeding | Each own confirmed publication seeds durable targets at `posted_at` + 1 h, + 6 h, + 24 h. Dry-run and unconfirmed or unknown intents seed none; only actions of the last 29 days are seeded |
| Scheduling | Independent of the agent's chosen wake, serialized with agent sessions, owner state rechecked. Audit scope `wakes.kind='metrics'` ([wake.md](wake.md)) |
| Coalescing | Targets are approximate. Late or restarted work coalesces all ready windows of an action into one read and one sample at the actual time; each completed target references that sample time. Completed targets are never re-read |
| Claims | Default 10 action groups per batch, five-minute lease, five-minute retry. Atomic claims exclude active leases; expiry increments a fencing version. Only the complete fresh claim set can insert the sample and complete its targets. A failed transaction keeps no partial sample |
| Deferral | Unavailable reads defer with a fixed reason and no sample. The delay doubles with each deferral that counts (not `cancelled`, `auth_required`, `source_unavailable`); the eighth ends the job with its reason |
| Deleted post | A read failing with `400 code 100` while the account's thread listing no longer holds the post ends every open job with `media_gone`; the dashboard marks it Deleted |
| Failure | Authentication stops pause the agent and queue the owner notice. Cancellation, audit and persistence failures surface as errors; remaining claims defer or recover after expiry. Each maintenance backend closes on every exit |
| Events | `metrics.scheduled`, `metrics.measured`, `metrics.target.completed`, `metrics.deferred`, `metrics.finished`: local IDs, counts, times, lateness and safe reasons only |

## Episodes

An episode is one live, confirmed own action with what is known about it. It is computed on demand from `actions` and the metric samples; there is no table.

| Part | Fact |
|---|---|
| Content | Kind, text, hook, `angle`, `fact`, `topic_tag`, `posted_at`, and for a reply the target's figures at read time (replies, age, likes) |
| Window | The newest completed metric target: 1, 6 or 24 hours. Rank uses the target window, never the time since posting, so a 6 h score is never compared with a late 9 h one |
| Score | `likes + 2 * replies` at the window |
| Rank | Percentile of the score among the agent's last 50 live actions of the same kind and window; ties go by views |
| Band | `top` at rank 0.75 or more, `bottom` at 0.25 or less, else `mid`. With fewer than 8 comparable actions the band is `unranked` and the numbers still show |

## Lessons

A lesson is one sentence of at most 240 characters about how the account should write: one thing to do or avoid. It is the only learned text and lives in the `lessons` table with its evidence (action ids). The agent cannot write one in a wake.

| Rule | Fact |
|---|---|
| Who writes | Only the dream, through `lesson_add`, `lesson_confirm` and `lesson_retire` ([tools.md](tools.md)) |
| Add | `kind` post, reply or any; `polarity` do or avoid; 2 to 5 own action ids, all ranked at 6 h or 24 h and of that kind unless `any`. `do` cites only `top` actions, `avoid` only `bottom` ones |
| Text gate | At most 240 characters; no secret, no URL that is not an own action URL, and any number must be a counter measured on a cited action |
| Limits | At most 10 active lessons (`lessons_full`: retire one first). A lesson that normalizes equal to an active one is refused (`lesson_duplicate`) |
| Confirm | At least one new cited action meeting the same band rule; it renews the lesson and adds to its evidence |
| Expiry | At the start of each dream, a lesson not confirmed for 14 days and not applied in the last 7 retires with `retired_by=expiry` |
| Use | A post or reply may name up to 3 active lessons it applied (`lesson_ids`), stored in `action_lessons`. Use stats are computed on demand: applied count, median rank of the applied actions and median rank of the other same-kind actions |
| Forgotten | An owner `/forget` retires the lesson with `retired_by=owner`; the dream cannot add a lesson that says the same (`lesson_forgotten`) |

## Dream

| Rule | Fact |
|---|---|
| Schedule | Once per local day at `schedule.dream_at`. Own caps `limits.max_usd_per_dream` and `limits.max_steps_per_dream` (default 8); its spend also counts against `limits.max_usd_per_day` |
| Gate | Runs only when at least 5 episodes reached a 6 h or 24 h window since the last completed dream. Otherwise the wake ends as `too_few_episodes` with no model call and still uses up its day |
| Context | Active lessons with age and use stats, the new episodes since the last dream, and the best and worst 5 of each kind from the last 7 days. Code-owned role prompt, no persona |
| Tools | `lesson_add`, `lesson_confirm`, `lesson_retire` only. No Threads backend or browser is opened |
| Result | Leaving the lessons unchanged is valid. A lesson whose applied actions rank worse than the others is retired |

## What the agent sees in a wake

The data message of an agent wake carries `lessons` (active, with id, kind and polarity), `examples` (best and worst 3 of each kind from the last 48 hours, with text, numbers, band and target figures), `recent` (the last 10 episodes as compact rows) and `said` (the texts of the last 10 own actions, measured or not). The writer gets the same examples ([wake.md](wake.md), [content.md](content.md)).

## Owner controls

| Surface | Action |
|---|---|
| Telegram | `/lessons` lists active lessons with their use; `/forget <id>` retires one for good. The daily report is written from episodes and lessons |
| `ctl` | `lessons --agent ID` lists active and retired lessons; `lessons forget <id> --agent ID` retires one ([operations.md](operations.md)) |
| Dashboard | A lessons panel: active and retired lessons with their use |

Job DDL and lessons DDL: [`schema.sql`](../internal/store/schema.sql); code: [`internal/metrics`](../internal/metrics), [`internal/store`](../internal/store); simulation cases: [evals.md](evals.md).
