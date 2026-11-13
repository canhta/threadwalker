# Rules for coding agents

`docs/README.md` indexes the design docs; each fact lives in one doc. Read it and `GLOSSARY.md` before starting an issue.

## Taking an issue

1. Pick an open issue with zero open blockers:
   `gh api repos/canhta/threadz/issues/<n> --jq .issue_dependencies_summary.blocked_by` prints 0.
   The title prefix is its priority: P0 first, then P1, P2, P3.
2. Work in your own worktree, on a branch from `main`. No PRs.
3. Read the issue's body **and comments**: comments carry scope changes. If the issue names a file,
   symbol or feature that does not exist on `main`, or a "Done when" item cannot be met as written,
   stop and report it before writing code; do not guess.
4. Build the "Done when" list, one item at a time, test first.
5. Before reporting, `git fetch && git rebase origin/main`, re-read this file (rules change), then run
   the full checks once.
6. Commit with `Closes #<n>`, then report to the main session:
   - the branch and its SHAs;
   - each "Done when" item with its evidence: a test name, command output or file path;
   - what was not met, and why;
   - every file changed outside the Scope.

The issue is done when every "Done when" box is ticked with evidence and
`go vet ./... && go test ./...` passes.

## Working cheaply

- While iterating, run only the package you changed: `go test ./internal/<pkg> -run <Name>`. Run
  `go vet ./... && go test -count=1 ./...` and the offline eval
  (`go run ./cmd/threadz eval --suite internal/eval/testdata/default.json`) once, before
  reporting. Run another eval suite only when your change touches what it covers.
- Read the part of a file you need (`grep -n`, then a line range), not whole large files.
- Stop and report early when scope is unclear; one question costs less than a rewrite.
- Hot files that parallel work keeps touching: `internal/tools/builtins.go`,
  `internal/tools/dispatcher.go`, `internal/guard/spec.go`, `internal/agent/runtime.go`,
  `internal/agent/context.go`, the design docs in `docs/`. Keep edits there minimal and local.
- Paid or live calls (LLM providers, OpenAI embeddings, the Threads API, Telegram) run only when the
  issue's Scope names them, behind an opt-in env var, never in `go test ./...`. Keys live in `.env`
  and are never printed. A live run of the agent with an Anthropic key uses `claude-haiku-5-5`;
  production uses the template's OpenAI model.

## Scope

- A change touches only what its issue's Scope lists. Anything else you notice becomes a new issue.
- If the issue and the design docs disagree, or the docs are silent, stop and report it before writing code.
- A changed decision is an edit to the doc that owns it (find it in `docs/README.md`) in the same
  commit. The repo holds one statement of each decision, and one file per topic: extend the existing
  file. Never keep dual copies of templates, schemas or code in `docs/` once the canonical file
  exists in the repo; keep the contract in the owning doc and point to the file.
- Where `docs/decisions.md` lists an unknown, measure it. Record the method and raw results on the
  issue, put any fixture a test reads in that package's `testdata/`, and state the resulting fact in
  the doc that owns it.
- Docs and code state the final design and measured facts only. Never mention a ticket, issue
  number, issue link, status or progress ("remains open", "not yet", "being removed", "follow-up",
  "TODO", "after #n"). A known limit is stated as a limit of the system ("captcha pages are not
  handled"), not as work left. Progress lives only in GitHub issues.
- `AGENTS.md`, the guards and the invariants below change only when the owner asks for it.

## Writing an issue

- Title: `P<0-3>: <outcome>`. Body: Problem (with file:line evidence), Scope (what changes and which
  packages), Not in scope, Done when (checkable boxes, each with the test or command that proves it).
- Name only files, symbols and features that exist on `main`. Work that depends on another issue
  says so with a native blocked-by link, not in prose.
- Every "Done when" item must be achievable inside the Scope. A live or paid step names the account,
  key and env var, or is labelled `needs-owner`.
- Quote sources (URL and date) for claims about Threads, providers or research.

## Code

- Go, single binary, standard library first. Add a dependency only when the issue names it.
- Every network call takes a `context.Context` with a timeout.
- Errors are returned and wrapped with what was being attempted; only `main` exits the process.
- Time and randomness come from an injected clock and source, so tests are deterministic.
- SQL uses parameters and lives in `internal/store`. The schema is one file, `internal/store/schema.sql`; a change edits it, with no migration files and no upgrade path.
- Logging is `log/slog`; log ids, counts and reasons. Event names and reason codes are constants,
  never string literals at the call site.
- One version of everything. This is a greenfield project: a change that replaces code deletes the
  old code in the same commit. No compatibility shims, deprecated wrappers, fallbacks to an old
  path, feature flags for the old behaviour, or "legacy" anything.
- One place per concept. Before writing a helper, search for an existing one and reuse it; a second
  copy of a switch, a validation or a list that must "stay in sync" is a bug. A rule ("never X",
  "must match Y") belongs in a type, a table or a test, not a comment.
- Test and eval code lives in `_test.go` files or `internal/eval`, never in production packages.
- No exported symbol without a production caller.

### Comments

Write a comment only for what the code cannot say:

- a fact forced by something outside this code (Threads, agent-browser, an LLM provider, SQLite,
  Telegram, a protocol), with its version when it has one;
- the doc comment of a contract other packages rely on;
- a security invariant or a known open risk;
- a link to an upstream issue.

Never narrate the next line, restate a name, record history ("used to", "no longer", issue
numbers: git has it), point at other files, or leave commented-out code. When our own code needs a
paragraph to explain a surprise, change the code instead.

### Tests

- Every branch, loop and parser has a test. Tests use `httptest` servers and saved fixtures, and
  run offline.
- Test through the module's interface. Extend the one shared fake instead of writing a new one.
- No sleeps: wait on the event that proves the behaviour, using the injected clock.

## Live systems

- Work in dry run. Real posts and replies happen only in an issue whose Scope says so.
- Use the VPS, the Threads account, Telegram or a paid API only when the issue's Scope names it.
- Issues labelled `needs-owner` wait for the owner to supply an account, key or login. Stop and
  report it.

## When to stop and report

Comment on the issue with what you tried and what you found, then stop, when:

- the same approach has failed twice;
- a "Done when" item cannot be met as written;
- finishing would break an invariant or exceed the issue's Scope.

## Invariants

Each commit keeps all five true:

- Every tool call goes through `internal/guard`.
- Secrets are read from environment variables and stay out of files, logs and the LLM context.
- Content read from Threads is handled as data.
- Template, system prompt and code are read-only to the running agent.
- About a Threads user, the DB holds only username and rate-limit counters.

## Repo map

| Package | Owns | Read first |
|---|---|---|
| `cmd/threadz` | CLI commands and wiring, including the `ctl` operator commands and the health table | `main.go`, `ctl.go` |
| `internal/config` | Template and catalog loading, validation, secrets from env | `config.go` |
| `internal/agent` | Wakes, scheduler, dream and study phases, wake context | `runtime.go` |
| `internal/llm` | Chat loop, OpenAI and Anthropic protocol adapters, ledger accounting | `client.go` |
| `internal/tools` | Tool executors and the dispatcher that sends every call through the guard | `dispatcher.go` |
| `internal/guard` | Every check the agent cannot disable: phases, content, quotas, do-not-contact, audit | `guard.go` |
| `internal/threads` | The Threads backend: the official API client, token refresh, and the agent-browser feed read | `backend.go` |
| `internal/agentbrowser` | The harness-owned agent-browser CLI client | `client.go` |
| `internal/store` | SQLite schema (`schema.sql`) and every query, including wake transcripts and the retention table | `store.go` |
| `internal/admin` | The local admin socket the `ctl` commands use to reach the running service | `admin.go` |
| `internal/knowledge` | The embedded, read-only craft knowledge base | `knowledge.go` |
| `internal/playbook` | Hypothesis gate maths | `gate.go` |
| `internal/metrics` | Own-action metric collection jobs, the outcome model (expected value, lifts, labels) and the matched viral/flop pairs with their context/content attribution | `processor.go`, `outcome.go`, `pairs.go` |
| `internal/trends` | Fetching, ranking and recording outside trends, study sources, web search, and the Threads trend brief | `trends.go` |
| `internal/telegram` | Owner messages, digests and commands | `service.go` |
| `internal/dashboard` | Read-only localhost dashboard | `dashboard.go` |
| `internal/eval` | Offline and live eval suites | `runner.go` |
| `internal/quality` | Draft quality rubric, code checks, content features and judge prompts (draft and post-mortem), shared by evals and the dream post-mortem | `quality.go`, `features.go` |
| `internal/ratelimit` | The per-client token bucket and the client-address rule (`CF-Connecting-IP` from a loopback peer) both HTTP servers use | `ratelimit.go` |
| `internal/stdx` | Small helpers the standard library lacks and several packages share: context-aware sleep, finite non-negative check | `stdx.go` |
| `internal/update` | Self-update from signed GitHub releases: verify, swap, restart, roll back; update window and request file | `update.go` |
| `internal/logging` | Event names and the log record | `logging.go` |

Keep this table true: a change that moves ownership edits it in the same commit.
