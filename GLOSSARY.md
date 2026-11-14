# Glossary

Use these terms and spellings in code, issues and docs.

| Term | Meaning |
|---|---|
| `harness` | The fixed Go code: scheduling, tools, guards, ledger. The agent cannot change it. |
| `agent` | One LLM loop bound to one template, one Threads account and one budget. |
| `template` | The YAML file declaring an agent's goal, persona, limits and LLM. Immutable to the agent. |
| `wake` | One agent session, from being woken until it ends itself or hits a limit. |
| `tool` | An operation the agent can call. Every tool call passes the guards before it executes. |
| `guard` | A check in code the agent cannot disable: limits, blocked phrases, safety categories, do-not-contact. |
| `action` | One post or reply that was published, or logged in a dry run. |
| `posted` | A real action whose stored `thread_url` passes the canonical own-account check, so it exists on Threads under the agent's own username. A dry run, draft or unconfirmed attempt is not posted. |
| `ledger` | Tokens and cost per agent, day and phase (`ledger_phases`), computed locally; a daily total is their sum. |
| `dry_run` | Mode in which `post` and `reply` only log. |
| `owner` | The human operator, reached over Telegram. |
| `do_not_contact` | Usernames the agent may no longer reply to. |
| `transcript` | The redacted, bounded record of one wake kept 8 days: context hash and data message, model turns, tool results, usage and end reason. |
| `admin socket` | The local Unix socket through which `threadz ctl` reaches the running service. |
| `post-mortem` | The code-built block of the dream context: matched viral and flop pairs of the last 14 days with content features, an independent judge's scores and the split of the gap between context and content. |
| `judge` | The model set in the template's `review.llm`, never the agent's own, that scores published actions in the post-mortem. It approves and blocks nothing. |
| `hook type` | The kind of first line an action opens with (`number`, `contrarian`, `curiosity_gap`, `personal_stake`, `trend_detail`), stored on the action. |
| `series` | A post of 2 to 5 parts: part 1 is the hook and each later part is published as a reply to the part before it. It is one action and counts as one post. |
| `trend anchor` | The item (a trend, search or feed result read this wake) a post names in `trend_ref`; the guard refuses a post anchored to nothing it read. |
