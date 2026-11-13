# Decisions: what is settled, what is out of scope, and what is not yet known?

Each row is one decision with a one-line reason. A change to a decision edits its row in the same commit. Details live in the doc each row links to.

## Settled

| Topic | Choice | Reason |
|---|---|---|
| Autonomy | Fully automatic; nobody approves posts | The experiment measures how far an agent runs, learns and improves with nobody approving its output |
| Owner contact | Telegram, only when needed | The owner stays out of the loop except for limits, logouts, budget and questions ([operations.md](operations.md)) |
| Template | Declares goal, persona and limits; the agent plans its own tactics | The owner sets ends and bounds; the agent finds the means ([template.md](template.md)) |
| Self-editing | The agent edits its playbook and notes in the DB, never code or its template | Hard limits must stay out of the agent's reach ([architecture.md](architecture.md)) |
| Playbook edits | Single entries only, no wholesale rewrite | Wholesale rewriting collapsed an agent's context from 18,282 to 122 tokens |
| Outcome counting | Code counts outcomes; the agent never counts its own results | An agent's own tally cannot be trusted as evidence ([learning.md](learning.md)) |
| Account | A disposable side account whose bio states it is an AI experiment | Limits the cost of a ban and keeps the account honest |
| Threads access | Official Threads API for every own-account operation; the pinned `agent-browser` CLI reads the home feed and replies to feed posts | The API has no home-feed endpoint and rejects ids decoded from permalinks; the app's tester role needs no Business Verification ([threads.md](threads.md)) |
| LLM | Named providers and model profiles; the operator chooses provider IDs, model IDs and key variable names; `openai` and `anthropic` adapters | No vendor or model allowlist; a vendor with another wire format is not compatible by configuring its key |
| Language | Go, single binary | One deployable artifact |
| Database | SQLite via the pure-Go `modernc.org/sqlite` | No cgo; one file |
| Host | Local development; a VPS is optional | The harness is verified with offline tests, synthetic fixtures and injected-time soaks |
| Several agents | Supported | Every table carries `agent_id`; wakes share one serial loop |
| Codebase | One version of each thing: a change that replaces code deletes the old code; no shims, legacy paths or second implementations | Keeps each decision stated once |
| Schema | No in-place upgrade; a database with another version is refused | A fresh file on deploy ([data.md](data.md)) |
| Crash safety | An interrupted wake is never replayed; an unknown publish outcome is recorded as sent and never retried | A crash must not double-post ([wake.md](wake.md)) |
| Telegram outbox | A claimed message is never retried after restart | A message may be lost but is never duplicated |
| Operator access | Unix socket, mode 0600, no network listener; remote use over SSH | Nothing is exposed for an attacker to reach |
| Rate limits | Spacing between actions is only a rate limit | See non-goals |
| Posterior and thresholds | A normal model with fixed prior weights; thresholds are implementation choices | Not a validated Threads learning algorithm; establishment is observational |
| Search tokenizer | `unicode61 remove_diacritics 0` | Measured: precision 0.875 vs 0.535 at equal recall on accented queries |
| Memory search | Hybrid BM25 plus embedding rank fusion | Measured: recall@5 0.917 vs 0.783 for FTS5 alone |
| Writer and critic | Tool-free calls before the guard; the critic gates every draft | Reasoning models can reason without tools; style is judged before reservation ([content.md](content.md)) |
| Content knowledge | Audience, language and style live in the template, not in code | One owner per fact ([architecture.md](architecture.md)) |
| Cadence | Code derives the next wake from the KPI gap; the agent has no scheduling tool | Pressure must not be negotiable by the agent ([wake.md](wake.md)) |
| Budget | The agent always sees its remaining budget; the harness blocks only at the limit | Spending is the agent's decision |

## Non-goals

| Non-goal | Reason |
|---|---|
| A multi-agent pipeline of tool-using agents; the only extra model calls are the tool-free `ideas`, writer and critic | Keeps one loop to audit |
| A vector database, an ORM, a web framework | Brute-force cosine over SQLite blobs and the standard library are enough at this scale |
| Delays that imitate a human | Spacing is a rate limit, not disguise |
| Feeding real revenue back into the budget | Out of scope for the experiment |
| Open X, Facebook or TikTok feeds | No stable no-login source was measured |
| Multimodal input, streaming, server-side tools | Unsupported by the adapters |
| Exposing any admin surface over the network | See operator access |

## Known limits

| Limit | Detail |
|---|---|
| Meta App Review | Without it `search` covers only the account's own posts and `profile_posts` serves a few official accounts; both return `limited_access` ([threads.md](threads.md)) |
| Link keys | Redirect aliases and tracking variants are not detected |
| Author reply | `author_replied` reads only the first page of replies |
| Local identity | Account and session binding are not measured |
| Cost model | Flat rates; no cache discount, tier pricing or billed dollars; flat subscriptions expose no balance |
| Soak evidence | Synthetic and logical-time; simulated time does not count as elapsed time |

## Unknowns

| Unknown | State |
|---|---|
| Whether Threads blocks or challenges logins from the VPS's datacenter IP | A conclusion needs measurement from that IP |
| How long a browser-automated account survives | Not measured |
| Whether a cheap model can sustain a long tool-calling loop without drifting | Not measured |
| How many actions a real hypothesis needs to be decided at the real lift spread | Synthetic: median 34 to 90 actions for a true lift of 0.5 to 0.3 ([learning.md](learning.md)) |
| Captcha and rate-limit banners, session portability, production memory | Not measured |
| Real-bot Telegram behaviour | Not measured |

Live measurements are separate evidence: real-account browser checks, a 48-hour single-agent dry run, real-bot controls, six real Threads operations, the seven-day live experiment and the two-agent 48-hour memory observation. They run on a local machine or an operator-chosen host. Live posting needs an owner-named scope, an owner-provided account or session and explicit approval.

## Measured stack facts

| Part | Fact |
|---|---|
| Threads API on the tester account (2026-11-12) | `/me`, account insights, `followers_count` and `threads_publishing_limit` were read live. Post, reply, per-post insights, replies, mentions, keyword search and token refresh ran against fixtures recorded from the documented response shapes: that account had no posts and its token lacked the keyword-search and mentions scopes |
| Browser | Synthetic transport and lifecycle, and the feed read, are tested against sanitized accessibility fixtures |
| LLM | Two-tool live behaviour, `gpt-6-luna` prices and the Chat Completions-versus-Responses choice were measured |
| Deploy | Local process; optional systemd on a Linux host ([setup.md](setup.md)) |
