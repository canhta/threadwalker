# Docs index: which doc answers my question?

Each doc answers one question, stated in its first line, and holds each fact once; other docs link to it. When a decision changes, edit the doc that owns the fact in the same commit. Code is the truth when a doc disagrees: fix the doc.

| Question | Doc |
|---|---|
| What are the parts, who owns what (code, template, agent-learned), how does data flow? | [architecture.md](architecture.md) |
| What happens in one wake, per phase; how do the KPI scoreboard and cadence work? | [wake.md](wake.md) |
| How is a post or reply made: ideas, writer, critic, guard, publish; what does each prompt own? | [content.md](content.md) |
| Which tools exist, in which phase, with which arguments and checks? | [tools.md](tools.md) |
| Which refusal reason codes exist and where is each enforced? | [guards.md](guards.md) |
| What does the agent remember, how are outcomes measured, what changes the playbook? | [learning.md](learning.md) |
| Which operations use the API or the browser; permissions, App Review limits, reply routing? | [threads.md](threads.md) |
| Which template keys exist, with type, default, owner and effect? | [template.md](template.md) |
| Which tables exist, how long is each kept, what is stored about a Threads user? | [data.md](data.md) |
| How do I run, inspect and control a live agent (ctl, Telegram, dashboard, update)? | [operations.md](operations.md) |
| What is settled, out of scope, or unknown, and why? | [decisions.md](decisions.md) |
| How do I install, configure credentials and deploy? | [setup.md](setup.md) |
| How do I run and compare evals? | [evals.md](evals.md) |
| What do the terms mean? | [../GLOSSARY.md](../GLOSSARY.md) |

## Reading order for a new agent

1. [architecture.md](architecture.md), then [wake.md](wake.md).
2. The doc for the area your issue touches.
3. [guards.md](guards.md) before changing anything a tool can do.
