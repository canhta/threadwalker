# ThreadWalker

An open-source experiment: given only a goal, one Threads account and a budget, how far can an AI
agent run, learn and improve by itself with nobody approving its output?

Start with the [setup guide](docs/setup.md). Implementation and live-run gates are tracked in
[GitHub Issues](https://github.com/canhta/threadwalker/issues).

| Read | For |
|---|---|
| [`docs/setup.md`](docs/setup.md) | Local checks, credentials and owner setup |
| [`docs/spec.md`](docs/spec.md) | The spec: architecture, tools, guards, schema, order of work |
| [`docs/evals.md`](docs/evals.md) | Offline eval harness: reproduce, compare and inspect failures |
| [`GLOSSARY.md`](GLOSSARY.md) | Terms used in code and issues |
| [`AGENTS.md`](AGENTS.md) | Rules for coding agents working in this repo |
| [`docs/research/`](docs/research/) | The evidence behind the decisions, with sources |

## Warning

The agent drives Threads through browser automation. That is against the Threads terms of service
and can get the account removed. Use only a disposable side account whose bio states that it is an
AI experiment.
