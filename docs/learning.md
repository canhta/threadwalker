# Learning: how does code measure outcomes?

Code counts outcomes; the agent never counts its own results. Feedback is delayed and noisy at tens of actions a day, so single posts teach little. The agent keeps no self-written store: it has no memory index, notes, playbook or strategy tools, and the template and system prompt are immutable to it. This doc is rewritten for the episodes and lessons design (Stage B).

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

Job DDL: [`schema.sql`](../internal/store/schema.sql); code: [`internal/metrics`](../internal/metrics); simulation cases: [evals.md](evals.md).
