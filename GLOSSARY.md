# Glossary

Use these terms, with these spellings, in code, issues and docs.

| Term | Meaning |
|---|---|
| `harness` | The fixed Go code: scheduling, tools, guards, ledger. The agent cannot change it. |
| `agent` | One LLM loop bound to one template, one Threads account and one budget. |
| `template` | The YAML file declaring an agent's goal, persona, limits and LLM. Immutable to the agent. |
| `wake` | One agent session, from being woken until it ends itself or hits a limit. |
| `tool` | An operation the agent can call. Every tool call passes the guards before it executes. |
| `guard` | A check in code the agent cannot disable: limits, forbidden phrases, do-not-contact. |
| `action` | One post or reply that was published, or logged in a dry run. |
| `playbook` | The agent's list of hypotheses about what works. |
| `hypothesis` | One playbook entry: `testing`, `established` or `rejected`. |
| `gate` | The code that changes a hypothesis's status from counters the code itself keeps. |
| `ledger` | Tokens and cost per agent per day, computed locally. |
| `dry_run` | Mode in which `post` and `reply` only log. |
| `owner` | The human operator, reached over Telegram. |
| `do_not_contact` | Usernames the agent may no longer reply to. |
